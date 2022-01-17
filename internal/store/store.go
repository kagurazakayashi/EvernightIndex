// Package store 封裝 SQLite 檔案：開啟、格式識別、交易邊界、一致性備份與完整性檢查。
//
// 本步驟只建立後續掃描、查詢與去重共用的地基，尚未定義條目、根目錄、
// 執行與事件的業務 schema；那些表結構與查詢留在後續步驟實作。
//
// 選型依據（modernc.org/sqlite）：
//   - 純 Go 實作，CGO_ENABLED=0 即可交叉編譯 Windows/macOS/Linux；
//   - 支援交易提交與回滾、context 取消（中斷查詢後連線仍可用）；
//   - 具備一致性備份途徑（VACUUM INTO 在同一個讀交易內產生快照，
//     不需要裸複製可能遺漏 WAL 內容的主檔）。
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

const (
	// ApplicationID 是寫入 PRAGMA application_id 的值，用來識別本程式建立的資料庫。
	// 0x45564E49 對應 ASCII 的 "EVNI"。
	ApplicationID int64 = 0x45564E49
	// SchemaVersion 是目前支援的格式版本，同時寫入 PRAGMA user_version。
	SchemaVersion int64 = 1
)

// storeMetaDDL 是唯一的基礎表：記錄格式版本與資料庫 UUID。
//
// 後續步驟會在此加入根目錄、執行、條目與變化事件；本步驟只確保識別與版本
// 資訊有地方可放，不建立空的業務表。
const storeMetaDDL = `CREATE TABLE IF NOT EXISTS store_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID`

// Options 控制開啟行為。
type Options struct {
	// Create 允許在檔案不存在時建立資料庫。
	Create bool
	// ReadOnly 以唯讀模式開啟：查詢類命令不得建立或修改資料庫。
	ReadOnly bool
	// BusyTimeout 是等待其他寫入者釋放鎖的最長時間，0 表示不等待。
	BusyTimeout time.Duration
}

// Store 是開啟後的資料庫句柄。
//
// 連線池固定為單一連線：CLI 每次只做一項任務，單連線讓寫入序列化、
// 交易邊界與 SQLite 的單寫者語義一致，也避免多連線同時持有讀交易。
type Store struct {
	db   *sql.DB
	path string
}

// Open 開啟或建立資料庫，並在必要時寫入識別與版本資訊。
//
// 流程：檔案是否存在 → 建立連線 → 識別 application_id 與 user_version →
// 新檔則初始化，既有檔則驗證。非本程式建立、版本過新或含未知結構的檔案
// 一律拒絕，不覆蓋。
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errDatabaseIO(errors.New("empty database path"))
	}
	if opts.ReadOnly && opts.Create {
		return nil, errDatabaseIO(errors.New("read-only open cannot create a database"))
	}
	exists := false
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		if info.IsDir() {
			return nil, errDatabaseIO(fmt.Errorf("database path is a directory: %s", path))
		}
		exists = true
	case errors.Is(statErr, os.ErrNotExist):
		exists = false
	default:
		return nil, errDatabaseIO(statErr)
	}
	if !exists && !opts.Create {
		return nil, model.New(model.CodeDatabaseMissing, "err.databaseMissing", model.ExitFatal, map[string]string{"path": path})
	}

	db, err := sql.Open("sqlite", dataSourceName(path, opts))
	if err != nil {
		return nil, errDatabaseIO(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errDatabaseIO(err)
	}
	s := &Store{db: db, path: path}
	if !exists {
		if err := s.initialize(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
		return s, nil
	}
	if err := s.verify(ctx, opts.Create); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Path 回傳開啟時的資料庫路徑。
func (s *Store) Path() string { return s.path }

// Close 關閉資料庫連線。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// WithTx 在單一交易內執行 fn：fn 回傳錯誤時回滾，否則提交。
//
// 取消、崩潰或錯誤都不得留下半次更新；因此提交失敗時同樣嘗試回滾，
// 讓呼叫端只看到「已提交」或「未提交」兩種狀態。
func (s *Store) WithTx(ctx context.Context, fn func(context.Context, *sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errDatabaseIO(err)
	}
	succeeded := false
	defer func() {
		if succeeded {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) && err == nil {
			err = errDatabaseIO(rollbackErr)
		}
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errDatabaseIO(err)
	}
	succeeded = true
	return nil
}

// Backup 以一致性快照把目前資料庫寫成 dst。
//
// 使用 VACUUM INTO：SQLite 在單一讀交易內產生目標檔，因此不含未提交的變更，
// 也不會像裸複製主檔那樣遺漏仍留在 WAL 的資料。dst 不得已存在；
// 先寫入同目錄暫存檔再更名，避免中途失敗留下半成品。
func (s *Store) Backup(ctx context.Context, dst string) error {
	if strings.TrimSpace(dst) == "" {
		return errDatabaseIO(errors.New("empty backup path"))
	}
	if _, err := os.Stat(dst); err == nil {
		return errDatabaseIO(fmt.Errorf("backup target already exists: %s", dst))
	} else if !errors.Is(err, os.ErrNotExist) {
		return errDatabaseIO(err)
	}
	suffix, err := randomHex(8)
	if err != nil {
		return errDatabaseIO(err)
	}
	tmp := dst + ".tmp-" + suffix
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", filepath.ToSlash(tmp)); err != nil {
		_ = os.Remove(tmp)
		return errDatabaseIO(err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return errDatabaseIO(err)
	}
	return nil
}

// IntegrityCheck 對目前資料庫執行完整性檢查。
//
// 完整檢查會讀過整個檔案，成本與資料庫大小成正比，因此不在每次 Open 執行，
// 而是留給寫回正式庫前的驗證與 recovery 使用。
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return errDatabaseIO(err)
	}
	if result != "ok" {
		return model.New(model.CodeDatabaseCorrupt, "err.databaseCorrupt", model.ExitFatal, map[string]string{"path": s.path})
	}
	return nil
}

// Meta 讀取 store_meta 中的單一值。
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key = ?", key).Scan(&value)
	if err != nil {
		return "", errDatabaseIO(err)
	}
	return value, nil
}

// initialize 為新檔寫入識別、版本與 UUID。
func (s *Store) initialize(ctx context.Context) error {
	uuid, err := randomUUID()
	if err != nil {
		return errDatabaseIO(err)
	}
	return s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			"PRAGMA application_id = " + strconv.FormatInt(ApplicationID, 10),
			"PRAGMA user_version = " + strconv.FormatInt(SchemaVersion, 10),
			storeMetaDDL,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return errDatabaseIO(err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO store_meta(key, value) VALUES (?, ?), (?, ?)",
			"schema_version", strconv.FormatInt(SchemaVersion, 10),
			"db_uuid", uuid,
		); err != nil {
			return errDatabaseIO(err)
		}
		return nil
	})
}

// verify 檢查既有資料庫是否為本程式支援的檔案。
func (s *Store) verify(ctx context.Context, allowInitialize bool) error {
	var applicationID, userVersion int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return errDatabaseIO(err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return errDatabaseIO(err)
	}
	var userTables int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&userTables); err != nil {
		return errDatabaseIO(err)
	}
	switch {
	case applicationID == ApplicationID:
	case applicationID == 0 && userTables == 0 && allowInitialize:
		return s.initialize(ctx)
	default:
		return model.New(model.CodeDatabaseForeign, "err.databaseForeign", model.ExitFatal, map[string]string{"path": s.path})
	}
	switch {
	case userVersion < SchemaVersion:
		return model.New(model.CodeDatabaseVersion, "err.databaseVersion", model.ExitFatal, map[string]string{
			"path":      s.path,
			"found":     strconv.FormatInt(userVersion, 10),
			"supported": strconv.FormatInt(SchemaVersion, 10),
		})
	case userVersion > SchemaVersion:
		return model.New(model.CodeDatabaseTooNew, "err.databaseTooNew", model.ExitFatal, map[string]string{
			"path":      s.path,
			"found":     strconv.FormatInt(userVersion, 10),
			"supported": strconv.FormatInt(SchemaVersion, 10),
		})
	}
	var hasMeta int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'").Scan(&hasMeta); err != nil {
		return errDatabaseIO(err)
	}
	if hasMeta == 0 {
		return model.New(model.CodeDatabaseCorrupt, "err.databaseCorrupt", model.ExitFatal, map[string]string{"path": s.path})
	}
	return nil
}

// dataSourceName 組出 SQLite URI：保留磁碟機代號與分隔符，只轉義會破壞 URI 的字元。
func dataSourceName(path string, opts Options) string {
	values := url.Values{}
	values.Add("_pragma", "journal_mode(WAL)")
	values.Add("_pragma", "busy_timeout("+strconv.FormatInt(opts.BusyTimeout.Milliseconds(), 10)+")")
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "synchronous(NORMAL)")
	if opts.ReadOnly {
		values.Set("mode", "ro")
	}
	escaper := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	return "file:" + escaper.Replace(filepath.ToSlash(path)) + "?" + values.Encode()
}

// errDatabaseIO 建立資料庫讀寫錯誤；detail 保留原始系統訊息以便診斷。
func errDatabaseIO(cause error) error {
	return model.Wrap(model.CodeDatabaseIO, "err.databaseIO", model.ExitFatal, map[string]string{"detail": cause.Error()}, cause)
}

// randomHex 產生 n 個隨機位元組的十六進位字串，用於暫存檔名。
func randomHex(n int) (string, error) {
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// randomUUID 產生 RFC4122 第 4 版格式的識別碼（純十六進位，無外部相依）。
func randomUUID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	canonical := hex.EncodeToString(buffer)
	return canonical[0:8] + "-" + canonical[8:12] + "-" + canonical[12:16] + "-" + canonical[16:20] + "-" + canonical[20:32], nil
}
