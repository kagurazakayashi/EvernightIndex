package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔實作正式資料庫目標的跨行程寫入鎖。
//
// 設計要點：
//   - 鎖綁定「正式資料庫路徑」，不是工作副本路徑；臨時模式寫回時必須使用同一把鎖；
//   - 使用作業系統的檔案鎖（Windows LockFileEx、Unix flock），行程結束時由系統釋放，
//     因此崩潰不會留下無法解除的鎖；
//   - 等待有逾時且可被 context 取消，回報穩定錯誤碼；
//   - 不刪除鎖檔：刪除會讓「已開啟舊檔案控制代碼的等待者」與「新建檔案的新持有者」
//     同時以為自己持鎖（分裂鎖）；鎖檔只是少量固定狀態，列在掃描排除規則中。
//
// 擴充點：本程式的讀連線協調與臨時模式發佈屏障留給後續步驟實作，
// 但都必須在持有本鎖的前提下進行，不能以工作副本鎖代替。

// LockSuffix 是正式目標鎖檔在資料庫路徑後的固定後綴。
//
// 值的唯一來源在 model 套件：掃描層要排除鎖檔，但不該依賴儲存層。
const LockSuffix = model.LockFileSuffix

// DefaultLockPoll 是等待鎖時的輪詢間隔。
const DefaultLockPoll = 50 * time.Millisecond

// 鎖定的位元組範圍刻意遠離中繼資料。
//
// Windows 的位元組範圍鎖會連讀取一起擋住，若鎖在偏移 0，其他行程就讀不到
// 持有者資訊，逾時訊息只能回報 unknown。把範圍移到 1 MiB 之後，
// 中繼資料（偏移 0 起）仍可被讀取，鎖的互斥語義不變。
const (
	lockRangeOffset int64 = 1 << 20
	lockRangeLength int64 = 1
)

// LockOptions 控制取得寫入鎖的行為。
type LockOptions struct {
	// Timeout 是最長等待時間；0 表示不等待，立即失敗。
	Timeout time.Duration
	// Poll 是輪詢間隔；0 使用 DefaultLockPoll。
	Poll time.Duration
	// RunID 寫入鎖資訊，方便診斷目前的持有者。
	RunID string
	// Generation 是已知的已發佈代次，寫入鎖資訊供診斷。
	Generation int64
	// Notify 在開始等待時被呼叫一次，讓上層輸出「正在等待」訊息。
	Notify func(waited time.Duration)
}

// lockInfo 是鎖檔內容，僅供診斷：真正的互斥由作業系統檔案鎖保證。
type lockInfo struct {
	PID        int
	RunID      string
	Database   string
	Generation int64
	Host       string
	AcquiredAt string
}

// MarshalJSON 輸出固定英語鍵，讓駭客排查與工具讀取不受欄位命名影響。
func (i lockInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"pid":             i.PID,
		"run_id":          i.RunID,
		"database":        i.Database,
		"generation":      i.Generation,
		"host":            i.Host,
		"acquired_at_utc": i.AcquiredAt,
	})
}

// UnmarshalJSON 讀回鎖檔內容；缺欄位時保留零值。
func (i *lockInfo) UnmarshalJSON(data []byte) error {
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if value, ok := fields["pid"].(float64); ok {
		i.PID = int(value)
	}
	if value, ok := fields["run_id"].(string); ok {
		i.RunID = value
	}
	if value, ok := fields["database"].(string); ok {
		i.Database = value
	}
	if value, ok := fields["generation"].(float64); ok {
		i.Generation = int64(value)
	}
	if value, ok := fields["host"].(string); ok {
		i.Host = value
	}
	if value, ok := fields["acquired_at_utc"].(string); ok {
		i.AcquiredAt = value
	}
	return nil
}

// Lock 是已取得的正式目標寫入鎖。
type Lock struct {
	dbPath   string
	path     string
	file     *os.File
	runID    string
	mu       sync.Mutex
	released bool
}

// LockPath 回傳資料庫對應的鎖檔路徑（與資料庫同目錄）。
func LockPath(dbPath string) string {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		abs = filepath.Clean(dbPath)
	}
	return abs + LockSuffix
}

// AcquireLock 取得正式目標的跨行程寫入鎖。
//
// 逾時回報 E_DATABASE_LOCKED 並附上目前持有者資訊；等待期間被取消回報
// E_INTERRUPTED。呼叫端必須在整個寫入階段（含資料庫開啟、提交、寫回）持有它。
func AcquireLock(ctx context.Context, dbPath string, opts LockOptions) (*Lock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, model.Wrap(model.CodeInterrupted, "err.interrupted", model.ExitInterrupted, nil, err)
	}
	path := LockPath(dbPath)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errDatabaseIO(fmt.Errorf("open lock file %s: %w", path, err))
	}
	poll := opts.Poll
	if poll <= 0 {
		poll = DefaultLockPoll
	}
	started := time.Now()
	notified := false
	for {
		acquired, lockErr := tryLockExclusive(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, errDatabaseIO(fmt.Errorf("lock %s: %w", path, lockErr))
		}
		if acquired {
			lock := &Lock{dbPath: dbPath, path: path, file: file, runID: opts.RunID}
			if err := lock.writeInfo(opts); err != nil {
				_ = lock.Release()
				return nil, err
			}
			return lock, nil
		}
		waited := time.Since(started)
		if opts.Timeout <= 0 || waited >= opts.Timeout {
			owner := describeLockOwner(path)
			_ = file.Close()
			return nil, model.New(model.CodeDatabaseLocked, "err.databaseLocked", model.ExitFatal, map[string]string{
				"path":    path,
				"timeout": opts.Timeout.String(),
				"owner":   owner,
			})
		}
		if opts.Notify != nil && !notified {
			notified = true
			opts.Notify(waited)
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, model.Wrap(model.CodeInterrupted, "err.interrupted", model.ExitInterrupted, nil, ctx.Err())
		case <-timer.C:
		}
	}
}

// writeInfo 把持有者資訊寫入鎖檔，供逾時訊息與人工診斷使用。
func (l *Lock) writeInfo(opts LockOptions) error {
	host, _ := os.Hostname()
	info := lockInfo{
		PID:        os.Getpid(),
		RunID:      opts.RunID,
		Database:   l.dbPath,
		Generation: opts.Generation,
		Host:       host,
		AcquiredAt: model.NowTimestamp().String(),
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return errDatabaseIO(err)
	}
	encoded = append(encoded, '\n')
	if err := l.file.Truncate(0); err != nil {
		return errDatabaseIO(err)
	}
	if _, err := l.file.WriteAt(encoded, 0); err != nil {
		return errDatabaseIO(err)
	}
	return l.file.Sync()
}

// Path 回傳鎖檔路徑。
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// RunID 回傳取得鎖時記錄的執行 ID。
func (l *Lock) RunID() string {
	if l == nil {
		return ""
	}
	return l.runID
}

// matches 判斷鎖是否對應指定的資料庫路徑。
func (l *Lock) matches(dbPath string) bool {
	if l == nil {
		return false
	}
	policy := model.CaseSensitive
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		policy = model.CaseInsensitive
	}
	left, err := filepath.Abs(l.dbPath)
	if err != nil {
		left = filepath.Clean(l.dbPath)
	}
	right, err := filepath.Abs(dbPath)
	if err != nil {
		right = filepath.Clean(dbPath)
	}
	return model.PathKey(left, policy) == model.PathKey(right, policy)
}

// Release 釋放寫入鎖；重複呼叫安全。
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	if l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return errDatabaseIO(unlockErr)
	}
	if closeErr != nil {
		return errDatabaseIO(closeErr)
	}
	return nil
}

// describeLockOwner 盡力讀出目前持有者資訊，讀不到時回報 unknown。
func describeLockOwner(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	var info lockInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return "unknown"
	}
	if info.PID == 0 && info.RunID == "" {
		return "unknown"
	}
	if info.RunID != "" {
		return fmt.Sprintf("pid %d, run %s", info.PID, info.RunID)
	}
	return fmt.Sprintf("pid %d", info.PID)
}

// errLockUnsupported 表示目前平台沒有實作跨行程檔案鎖。
var errLockUnsupported = errors.New("cross-process file locking is not implemented on this platform")
