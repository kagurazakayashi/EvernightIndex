package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/fsmeta"
	"github.com/kagurazakayashi/EvernightIndex/internal/hash"
	"github.com/kagurazakayashi/EvernightIndex/internal/index"
	"github.com/kagurazakayashi/EvernightIndex/internal/match"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/scan"
	"github.com/kagurazakayashi/EvernightIndex/internal/store"
)

// 本檔是 index 命令的編排層：解析根目錄與資料庫位置、在寫入前拒絕尚未實作的
// 選項與既存索引，然後依「開啟執行 → 建索引 → 發佈或放棄」的固定順序完成工作。
//
// 邊界：
//   - 所有拒絕都發生在任何掃描、檔案內容讀取與資料庫變更之前；
//   - 只支援首次建立；已含已發佈代次的資料庫一律拒絕，不重建也不覆蓋；
//   - 發佈是唯一提交點，取消或失敗都不會留下半次更新。

// indexTarget 把一次執行包成索引層的寫入目標。
type indexTarget struct {
	run *Run
}

// RunID 回傳本次執行 ID。
func (t indexTarget) RunID() string { return t.run.ID() }

// RootID 回傳綁定的根目錄 ID。
func (t indexTarget) RootID() int64 { return t.run.Root().ID }

// Stage 暫存一批條目。
func (t indexTarget) Stage(ctx context.Context, entries []model.Entry) error {
	return t.run.Stage(ctx, entries)
}

// runIndex 執行 index 命令，並把「取消造成的錯誤」統一成使用者中斷。
//
// 回傳的錯誤決定退出碼：參數問題 2、既存索引或資料庫問題 1、部分完成 3、中斷 130。
func (s *Service) runIndex(ctx context.Context, request Request) error {
	err := s.runIndexOnce(ctx, request)
	if err == nil || model.ExitCodeOf(err) == model.ExitInterrupted {
		return err
	}
	// 取消可能發生在任何階段（解析路徑、唯讀檢查、等鎖、掃描、發佈）。
	// 只要 context 已取消且錯誤就是它，就一律回報使用者中斷，不冒充內部錯誤。
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return interruptedError(err)
	}
	return err
}

// runIndexOnce 是 index 命令的實作主體。
func (s *Service) runIndexOnce(ctx context.Context, request Request) error {
	options := request.Options
	// 尚未實作的請求必須明確拒絕，不能忽略參數繼續執行。
	if options.ChangeLog {
		return model.New(model.CodeNotImplemented, "err.changeLogNotImplemented", model.ExitFatal, nil)
	}
	if strings.TrimSpace(options.TempDir) != "" {
		return model.New(model.CodeNotImplemented, "err.tempDirNotImplemented", model.ExitFatal, nil)
	}
	// --deep 必然重算並保存摘要，與關閉 SHA256 互斥；在任何工作之前拒絕。
	if options.Deep && !options.SHA256 {
		return model.New(model.CodeFlagConflict, "err.conflictDeepNoSha256", model.ExitUsage, nil)
	}
	if len(request.Args) < 1 {
		return model.New(model.CodeMissingArgument, "err.missingArgument", model.ExitUsage, map[string]string{
			"command":  "index",
			"argument": "<dir>",
		})
	}

	callCWD, err := os.Getwd()
	if err != nil {
		return model.Wrap(model.CodeInternal, "err.internal", model.ExitFatal,
			map[string]string{"detail": "cannot read the calling working directory"}, err)
	}
	rootPath, err := scan.ResolvePath(callCWD, request.Args[0])
	if err != nil {
		return err
	}
	root, err := scan.ResolveRoot(rootPath)
	if err != nil {
		// 根目錄不存在或不可存取時連資料庫都不建立：絕不發佈空索引。
		return err
	}
	casePolicy, _, err := root.CasePolicy()
	if err != nil {
		return err
	}
	volume, fileID, err := fsmeta.RootIdentity(root.AbsPath)
	if err != nil {
		return err
	}
	dbPath, err := resolveDBPath(callCWD, options.DB, root.AbsPath)
	if err != nil {
		return err
	}
	generation, exists, err := existingPublishedGeneration(ctx, dbPath)
	if err != nil {
		return err
	}
	if exists {
		// 增量更新尚未實作：不重建、不覆蓋，明確說明原因與目前代次。
		return model.New(model.CodeUpdateNotSupported, "err.updateNotSupported", model.ExitFatal, map[string]string{
			"generation": strconv.FormatInt(generation, 10),
			"path":       dbPath,
		})
	}
	excludes, err := compileExcludes(options.Excludes, casePolicy)
	if err != nil {
		return err
	}

	run, err := BeginRun(ctx, s.deps, RunSpec{
		DBPath: dbPath,
		Root: model.RootSpec{
			AbsPath:        root.AbsPath,
			SourcePlatform: runtime.GOOS,
			CasePolicy:     casePolicy,
			VolumeIdentity: volume,
			RootFileID:     fileID,
		},
		Params: ScanParams{
			SHA256:        options.SHA256,
			Deep:          options.Deep,
			OneFileSystem: options.OneFileSystem,
			ChangeLog:     options.ChangeLog,
			Workers:       options.Workers,
			ReadRetries:   options.ReadRetries,
			Excludes:      options.Excludes,
		},
		LockTimeout: options.LockTimeout,
	})
	if err != nil {
		return err
	}
	defer func() { _ = run.Close() }()

	// 種族防線：另一個行程可能剛好在檢查與取鎖之間發布了首個代次。
	// 這時本次執行已經建立執行紀錄，仍必須拒絕覆蓋，並以未發佈狀態收尾。
	if baseline := run.Baseline(); baseline > 0 {
		cause := AbandonCause{
			Err: model.New(model.CodeUpdateNotSupported, "err.updateNotSupported", model.ExitFatal, map[string]string{
				"generation": strconv.FormatInt(baseline, 10),
				"path":       dbPath,
			}),
			Coverage: model.CoverageUnknown,
		}
		_ = run.Abandon(ctx, cause)
		return cause.Err
	}

	s.note(planKey(options.SHA256), map[string]string{
		"root":         root.AbsPath,
		"database":     dbPath,
		"workers":      strconv.Itoa(hashWorkers(options.Workers)),
		"read_retries": strconv.Itoa(effectiveRetries(options.ReadRetries)),
	})

	result, buildErr := index.Build(ctx, indexTarget{run: run}, index.Options{
		Root:          root.AbsPath,
		CasePolicy:    casePolicy,
		SHA256:        options.SHA256,
		Deep:          options.Deep,
		OneFileSystem: options.OneFileSystem,
		Workers:       options.Workers,
		ReadRetries:   options.ReadRetries,
		Excludes:      excludes,
		SelfFiles:     scan.SelfArtifacts(dbPath),
		Stats:         run.Stats(),
		Observe:       run.Observe,
	})
	if buildErr != nil {
		cause := AbandonCause{
			Err:          buildErr,
			Coverage:     result.Coverage,
			CoverageJSON: result.CoverageJSON,
		}
		if index.IsCanceled(ctx, buildErr) {
			cause.Interrupted = true
		}
		_ = run.Abandon(ctx, cause)
		if cause.Interrupted {
			// 取消是使用者中斷，不是內部錯誤：退出碼 130 並帶穩定錯誤碼。
			return interruptedError(buildErr)
		}
		return buildErr
	}

	// 摘要失敗的條目在掃描階段已被計為「已處理／成功」。
	// 這裡把它們移到失敗，讓最終統計反映條目終態，而不是假裝完全成功。
	if result.HashFailed > 0 {
		stats := run.Stats()
		stats.AddSucceeded(-result.HashFailed)
		stats.AddFailed(result.HashFailed)
	}

	published, err := run.Publish(ctx, Publication{
		Coverage:     result.Coverage,
		CoverageJSON: result.CoverageJSON,
		MessageKey:   "report.published",
	})
	if err != nil {
		interrupted := index.IsCanceled(ctx, err)
		_ = run.Abandon(ctx, AbandonCause{
			Err:           err,
			PublishFailed: !interrupted,
			Interrupted:   interrupted,
			Coverage:      result.Coverage,
			CoverageJSON:  result.CoverageJSON,
		})
		if interrupted {
			// 發佈交易沒有提交：結果未發佈，收尾記為中斷而不是回滾。
			return interruptedError(err)
		}
		return err
	}
	return published.ExitError()
}

// interruptedError 把取消包成使用者中斷（退出碼 130）。
func interruptedError(cause error) error {
	return model.Wrap(model.CodeInterrupted, "err.interrupted", model.ExitInterrupted, nil, cause)
}

// note 以目前語言輸出一般訊息（受 --quiet 抑制）。
func (s *Service) note(key string, args map[string]string) {
	if s.deps.Console == nil || s.deps.Catalog == nil {
		return
	}
	s.deps.Console.Note("%s", s.deps.Catalog.T(s.deps.Lang, key, args))
}

// planKey 依 SHA256 選項選擇開場訊息：內容是否會被讀取必須在開始前說清楚。
func planKey(sha256Enabled bool) string {
	if sha256Enabled {
		return "report.planSha256"
	}
	return "report.planNoSha256"
}

// resolveDBPath 決定資料庫路徑：未指定時使用掃描根目錄內的 index.db。
func resolveDBPath(callCWD, requested, rootPath string) (string, error) {
	trimmed := strings.TrimSpace(requested)
	if trimmed == "" {
		return filepath.Join(rootPath, defaultDatabaseName), nil
	}
	return scan.ResolvePath(callCWD, trimmed)
}

// defaultDatabaseName 是預設的資料庫檔名。
const defaultDatabaseName = "index.db"

// existingPublishedGeneration 檢查目標是否已是含已發佈代次的索引。
//
// 用唯讀連線詢問，確保檢查本身不會建立資料庫、不會遷移結構，也不會取得寫入鎖。
// 檔案不存在時回報 (0, false, nil)；存在但不是本程式資料庫時直接回報錯誤。
func existingPublishedGeneration(ctx context.Context, dbPath string) (int64, bool, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, model.Wrap(model.CodeDatabaseIO, "err.databaseIO", model.ExitFatal,
			map[string]string{"detail": err.Error()}, err)
	}
	storage, err := store.Open(ctx, dbPath, store.Options{ReadOnly: true})
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = storage.Close() }()
	generation, err := storage.PublishedGeneration(ctx)
	if err != nil {
		return 0, false, err
	}
	if generation > 0 {
		return generation, true, nil
	}
	return 0, false, nil
}

// compileExcludes 把排除模式編譯成共用集合，大小寫策略與掃描一致。
func compileExcludes(patterns []string, policy model.CasePolicy) (*match.Set, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	return match.CompileAll(patterns, match.Options{Insensitive: policy == model.CaseInsensitive})
}

// hashWorkers 回傳本次實際會使用的摘要 worker 數。
//
// 開場訊息印出的數字必須就是摘要層真正會用的數字，
// 因此直接問摘要模組，而不是在這裡另寫一份規則。
func hashWorkers(requested int) int {
	return hash.EffectiveWorkers(requested)
}

// effectiveRetries 把負數的重試次數視為 0。
func effectiveRetries(requested int) int {
	if requested < 0 {
		return 0
	}
	return requested
}
