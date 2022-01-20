package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
	"github.com/kagurazakayashi/EvernightIndex/internal/store"
)

// 執行生命週期（後續 index 命令與臨時副本模式都必須沿用它）：
//
//	BeginRun ──► running ──► Publish ──► published（代次 +1，唯一提交點）
//	                     └─► Abandon ──► failed / interrupted / aborted（舊代次不變）
//
// 關鍵保證：
//   - 取得正式目標寫入鎖之後才開啟資料庫與建立執行記錄，鎖一路持有到 Close；
//   - 發佈前讀到的已發佈代次（baseline）會隨發佈一起比對，
//     期間被其他結果覆蓋就拒絕發佈，不覆蓋他人結果；
//   - 提交後才收到的取消不會改寫執行狀態，也不回報「已回滾」。

// sqliteBusyTimeout 是 SQLite 自身的等待上限。
//
// 跨行程互斥由正式目標鎖負責；這裡只是避免短暫的內部競爭直接失敗。
const sqliteBusyTimeout = 5 * time.Second

// ScanParams 是本次掃描的參數，會以穩定 JSON 存進執行記錄。
//
// 欄位順序固定（JSON 物件鍵名排序）以便比對兩次執行的設定差異。
type ScanParams struct {
	SHA256        bool
	Deep          bool
	OneFileSystem bool
	ChangeLog     bool
	Workers       int
	ReadRetries   int
	Excludes      []string
}

// JSON 把參數序列化成穩定鍵序的物件。
func (p ScanParams) JSON() (string, error) {
	excludes := p.Excludes
	if excludes == nil {
		excludes = []string{}
	}
	encoded, err := json.Marshal(map[string]any{
		"sha256":          p.SHA256,
		"deep":            p.Deep,
		"one_file_system": p.OneFileSystem,
		"change_log":      p.ChangeLog,
		"workers":         p.Workers,
		"read_retries":    p.ReadRetries,
		"excludes":        excludes,
	})
	if err != nil {
		return "", model.Wrap(model.CodeInternal, "err.internal", model.ExitFatal,
			map[string]string{"detail": err.Error()}, err)
	}
	return string(encoded), nil
}

// RunSpec 描述一次索引執行。
type RunSpec struct {
	// DBPath 是正式資料庫路徑（鎖綁定的目標）。
	DBPath string
	// Root 是掃描根目錄的綁定資訊。
	Root model.RootSpec
	// Params 是掃描參數，會存進執行記錄。
	Params ScanParams
	// LockTimeout 是等待正式目標鎖的上限；0 表示不等待。
	LockTimeout time.Duration
}

// Run 是一次進行中的索引執行。
type Run struct {
	id        string
	storage   *store.Store
	lock      *store.Lock
	root      model.Root
	baseline  int64
	mode      model.RunMode
	params    ScanParams
	stats     *output.Stats
	reporter  *reporter
	committed bool
	closed    bool
}

// BeginRun 開始一次索引執行：取得寫入鎖、開啟資料庫、綁定根目錄、建立執行記錄。
//
// 任一步失敗都會釋放已取得的資源，避免留下「有鎖沒有連線」或反向的殘留。
func BeginRun(ctx context.Context, deps Deps, spec RunSpec) (*Run, error) {
	dbPath := strings.TrimSpace(spec.DBPath)
	if dbPath == "" {
		return nil, internalError("database path is required")
	}
	runID, err := store.NewRunID()
	if err != nil {
		return nil, err
	}
	stats := output.NewStats()
	rep := newReporter(deps, stats)

	lock, err := store.AcquireLock(ctx, dbPath, store.LockOptions{
		Timeout: spec.LockTimeout,
		RunID:   runID,
		Notify: func(waited time.Duration) {
			rep.note("note.lockWait", map[string]string{
				"path":   store.LockPath(dbPath),
				"waited": formatElapsed(waited),
			})
		},
	})
	if err != nil {
		return nil, err
	}

	storage, err := store.OpenTarget(ctx, dbPath, store.Options{Create: true, BusyTimeout: sqliteBusyTimeout}, lock)
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	root, err := storage.BindRoot(ctx, spec.Root)
	if err != nil {
		_ = storage.Close()
		_ = lock.Release()
		return nil, err
	}
	baseline, err := storage.PublishedGeneration(ctx)
	if err != nil {
		_ = storage.Close()
		_ = lock.Release()
		return nil, err
	}
	paramsJSON, err := spec.Params.JSON()
	if err != nil {
		_ = storage.Close()
		_ = lock.Release()
		return nil, err
	}
	mode := model.RunModeUpdate
	if baseline == 0 {
		// 沒有已發佈代次代表這是首次建立：取消時資料庫不得被當成完整索引。
		mode = model.RunModeCreate
	}
	abandoned, err := storage.StartRun(ctx, store.RunSpec{
		RunID:              runID,
		RootID:             root.ID,
		Mode:               mode,
		ParamsJSON:         paramsJSON,
		BaselineGeneration: baseline,
	})
	if err != nil {
		_ = storage.Close()
		_ = lock.Release()
		return nil, err
	}
	if abandoned > 0 {
		rep.note("report.abandonedRuns", map[string]string{"count": strconv.FormatInt(abandoned, 10)})
	}
	run := &Run{
		id:       runID,
		storage:  storage,
		lock:     lock,
		root:     root,
		baseline: baseline,
		mode:     mode,
		params:   spec.Params,
		stats:    stats,
		reporter: rep,
	}
	rep.start(ctx)
	return run, nil
}

// ID 回傳本次執行 ID。
func (r *Run) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

// Mode 回傳本次執行是首次建立還是更新。
func (r *Run) Mode() model.RunMode {
	if r == nil {
		return ""
	}
	return r.mode
}

// Baseline 回傳開始時讀到的已發佈代次（0 表示尚無）。
func (r *Run) Baseline() int64 {
	if r == nil {
		return 0
	}
	return r.baseline
}

// Root 回傳綁定的根目錄。
func (r *Run) Root() model.Root {
	if r == nil {
		return model.Root{}
	}
	return r.root
}

// Stats 回傳執行統計；多個 worker 可安全累加。
func (r *Run) Stats() *output.Stats {
	if r == nil {
		return nil
	}
	return r.stats
}

// Storage 回傳底層儲存，供讀取已發佈資料與寫入暫存區。
//
// 呼叫端不得直接改寫 entry、root_dir 或 run：正式結果只能經由 Publish 產生。
func (r *Run) Storage() *store.Store {
	if r == nil {
		return nil
	}
	return r.storage
}

// Stage 暫存一批條目變更；此時查詢仍看不到任何改動。
func (r *Run) Stage(ctx context.Context, entries []model.Entry) error {
	if r == nil || r.storage == nil {
		return internalError("run is not open")
	}
	if r.committed {
		return internalError("run is already published")
	}
	return r.storage.StageEntries(ctx, r.id, entries)
}

// StageDeletions 暫存一批待刪除路徑；實際刪除只發生在發佈交易內。
func (r *Run) StageDeletions(ctx context.Context, pathKeys []string) error {
	if r == nil || r.storage == nil {
		return internalError("run is not open")
	}
	if r.committed {
		return internalError("run is already published")
	}
	return r.storage.StageDeletions(ctx, r.id, pathKeys)
}

// Publication 是一次發佈要提交的內容。
type Publication struct {
	Coverage     model.Coverage
	CoverageJSON string
	Events       []model.Event
	RecordExport bool
	MessageKey   string
	ErrorCode    string
	// BeforePublish 是發佈前的協調擴充點，在交易開始前被呼叫。
	//
	// 臨時工作副本模式在此再次核對正式資料庫的身份、UUID 與基線代次，
	// 並讓本程式的讀取連線退出（發佈鎖的協調階段）。回傳錯誤即中止發佈，
	// 舊代次與舊條目保持不變；直接更新模式目前不使用它。
	BeforePublish func(ctx context.Context) error
}

// Published 是發佈結果。
//
// Partial 為真表示結果已提交但有覆蓋缺口；ExportPending 為真表示變化日誌
// 仍待匯出。兩者都會讓命令以部分失敗（退出碼 3）結束，但都不影響已提交的事實。
type Published struct {
	Generation    int64
	Entries       int64
	Events        int64
	Partial       bool
	ExportPending bool
}

// ExitError 把發佈結果轉成退出碼錯誤；完全成功時回傳 nil。
func (p Published) ExitError() error {
	if p.ExportPending {
		return model.New(model.CodeExportPending, "err.exportPending", model.ExitPartial, map[string]string{
			"count": strconv.FormatInt(p.Events, 10),
		})
	}
	if p.Partial {
		return model.New(model.CodePartial, "report.partial", model.ExitPartial, map[string]string{
			"gaps": "coverage",
		})
	}
	return nil
}

// Publish 在單一交易內發佈本次結果。
//
// 回傳的 Published 只有交易成功才會超過 0 代次；失敗時舊代次與舊條目完全不變，
// 呼叫端應改用 Abandon 記錄收尾狀態。
func (r *Run) Publish(ctx context.Context, publication Publication) (Published, error) {
	if r == nil || r.storage == nil {
		return Published{}, internalError("run is not open")
	}
	if r.committed {
		return Published{}, internalError("run is already published")
	}
	if publication.BeforePublish != nil {
		// 協調失敗時不進入交易：舊代次必須完全不受影響。
		if err := publication.BeforePublish(ctx); err != nil {
			r.reporter.halt()
			return Published{}, err
		}
	}
	snapshot := r.stats.Snapshot()
	result, err := r.storage.Publish(ctx, store.PublishRequest{
		RunID:              r.id,
		ExpectedGeneration: r.baseline,
		Coverage:           publication.Coverage,
		CoverageJSON:       publication.CoverageJSON,
		Counts: model.RunCounts{
			Discovered:  snapshot.Discovered,
			Processed:   snapshot.Processed,
			Succeeded:   snapshot.Succeeded,
			Failed:      snapshot.Failed,
			ErrorEvents: snapshot.Errors,
		},
		Events:       publication.Events,
		RecordExport: publication.RecordExport,
		MessageKey:   publication.MessageKey,
		ErrorCode:    publication.ErrorCode,
	})
	if err != nil {
		r.reporter.halt()
		return Published{}, err
	}
	r.committed = true
	r.stats.SetPublished(result.Entries)
	final := r.stats.Snapshot()
	r.reporter.halt()
	r.reporter.summary(final, "stats.summary")
	r.reporter.note("report.published", map[string]string{
		"generation": strconv.FormatInt(result.Generation, 10),
		"run":        r.id,
		"entries":    strconv.FormatInt(result.Entries, 10),
	})
	return Published{
		Generation: result.Generation,
		Entries:    result.Entries,
		Events:     result.Events,
		Partial:    publication.Coverage == model.CoveragePartial,
		// 索引與事件已提交，但 CSV 只有在後續匯出成功後才算完成。
		ExportPending: publication.RecordExport || len(publication.Events) > 0,
	}, nil
}

// AbandonCause 說明未發佈收尾的原因。
type AbandonCause struct {
	// Err 是造成中止的錯誤（可為 nil）。
	Err error
	// PublishFailed 表示已嘗試發佈，但交易失敗並回滾。
	PublishFailed bool
	// Interrupted 表示中止來自使用者取消。
	Interrupted bool
	// Coverage 與 CoverageJSON 記錄當時的覆蓋狀態與缺口。
	Coverage     model.Coverage
	CoverageJSON string
}

// Abandon 在沒有發佈的情況下結束本次執行，並輸出最終統計。
//
// 若已經提交（例如取消在提交之後才抵達），不改變任何狀態，也不回報已回滾，
// 只補記「取消在提交後抵達」。回滾與否以是否提交為準，而不是以收到取消與否為準。
func (r *Run) Abandon(ctx context.Context, cause AbandonCause) error {
	if r == nil {
		return nil
	}
	if r.committed {
		r.reporter.note("report.cancelAfterCommit", map[string]string{
			"generation": strconv.FormatInt(r.baseline+1, 10),
		})
		return nil
	}
	if r.closed {
		return nil
	}
	status := model.RunStatusFailed
	summaryKey := "stats.failed"
	messageKey := "report.failed"
	switch {
	case cause.Interrupted:
		status = model.RunStatusInterrupted
		summaryKey = "stats.interrupted"
		messageKey = "report.interrupted"
	case cause.PublishFailed:
		status = model.RunStatusAborted
		summaryKey = "report.abortedSummary"
		messageKey = "report.aborted"
	}
	coverage := cause.Coverage
	if !coverage.Valid() {
		coverage = model.CoverageUnknown
	}
	snapshot := r.stats.Snapshot()
	errorCode := ""
	if cause.Err != nil {
		errorCode = errorCodeOf(cause.Err)
	}
	err := r.storage.FinishRun(ctx, store.FinishSpec{
		RunID:        r.id,
		Status:       status,
		Coverage:     coverage,
		CoverageJSON: cause.CoverageJSON,
		Counts: model.RunCounts{
			Discovered:  snapshot.Discovered,
			Processed:   snapshot.Processed,
			Succeeded:   snapshot.Succeeded,
			Failed:      snapshot.Failed,
			ErrorEvents: snapshot.Errors,
		},
		MessageKey: messageKey,
		ErrorCode:  errorCode,
	})
	r.reporter.halt()
	r.reporter.summary(snapshot, summaryKey)
	if status == model.RunStatusAborted {
		r.reporter.note("report.aborted", map[string]string{
			"generation": strconv.FormatInt(r.baseline, 10),
		})
	}
	return err
}

// Close 關閉資料庫並釋放寫入鎖；重複呼叫安全。
//
// 順序固定：先停止節拍輸出，再關閉連線，最後釋放鎖，
// 讓其他行程接手時看到的是已關閉且已提交（或已明確未發佈）的資料庫。
func (r *Run) Close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	r.reporter.halt()
	var firstErr error
	if r.storage != nil {
		if err := r.storage.Close(); err != nil {
			firstErr = err
		}
		r.storage = nil
	}
	if r.lock != nil {
		if err := r.lock.Release(); err != nil && firstErr == nil {
			firstErr = err
		}
		r.lock = nil
	}
	return firstErr
}

// errorCodeOf 取出穩定錯誤碼；非 model.Error 時回傳空字串。
func errorCodeOf(err error) string {
	if err == nil {
		return ""
	}
	var appErr *model.Error
	if errors.As(err, &appErr) {
		return string(appErr.Code)
	}
	return ""
}

// internalError 建立內部錯誤。
func internalError(detail string) error {
	return model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{"detail": detail})
}
