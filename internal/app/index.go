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
// 選項、預檢既有資料庫的格式/根目錄身份/上次發佈狀態，然後依
// 「開啟執行 → 建索引（首次建立或增量更新）→ 發佈或放棄」的固定順序完成工作。
//
// 邊界：
//   - 所有拒絕都發生在任何掃描、檔案內容讀取與資料庫變更之前；
//   - 同一個命令兩種模式：資料庫沒有已發佈代次時建立，有代次時更新；
//     兩者共用同一條暫存/發佈管線，提交成功前已發佈視圖完全不變；
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

// StageDeletions 暫存一批待刪除路徑。
func (t indexTarget) StageDeletions(ctx context.Context, pathKeys []string) error {
	return t.run.StageDeletions(ctx, pathKeys)
}

// Lookup 取回與本次掃描路徑相同的已發佈條目。
func (t indexTarget) Lookup(ctx context.Context, pathKeys []string) (map[string]model.Entry, error) {
	return t.run.Storage().LookupPublished(ctx, t.run.Root().ID, pathKeys)
}

// Unseen 串流列出本次沒有再出現的已發佈條目。
func (t indexTarget) Unseen(ctx context.Context, visit func(model.Entry) error) error {
	return t.run.Storage().EachUnseenPublished(ctx, t.run.Root().ID, t.run.ID(), visit)
}

// runIndex 執行 index 命令，並把「取消造成的錯誤」統一成使用者中斷。
//
// 回傳的錯誤決定退出碼：參數問題 2、資料庫或根目錄問題 1、部分完成 3、中斷 130。
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
		// 身份採集失敗同樣在任何寫入之前結束：不明身分的根目錄不能拿來更新既有索引。
		return err
	}
	dbPath, err := resolveDBPath(callCWD, options.DB, root.AbsPath)
	if err != nil {
		return err
	}
	// 預檢：格式/版本、根目錄身份與上次發佈狀態。唯讀開啟，不建立、不遷移、不上鎖。
	inspection, err := inspectDatabase(ctx, dbPath, root.AbsPath, volume)
	if err != nil {
		return err
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

	if inspection.Exists && inspection.Generation == 0 {
		// 既有資料庫沒有已發佈代次（上次建立被取消或發佈失敗）：
		// 重跑等同重新建立，不覆蓋任何已發佈結果，但要說清楚為什麼。
		s.note("report.reusingIncomplete", map[string]string{"path": inspection.Path})
	}
	s.note(planMessageKey(run.Mode(), options), map[string]string{
		"root":         root.AbsPath,
		"database":     dbPath,
		"generation":   strconv.FormatInt(run.Baseline(), 10),
		"next":         strconv.FormatInt(run.Baseline()+1, 10),
		"workers":      strconv.Itoa(hashWorkers(options.Workers)),
		"read_retries": strconv.Itoa(effectiveRetries(options.ReadRetries)),
	})

	result, buildErr := index.Build(ctx, indexTarget{run: run}, index.Options{
		Root:          root.AbsPath,
		CasePolicy:    casePolicy,
		SHA256:        options.SHA256,
		Deep:          options.Deep,
		ChangeLog:     options.ChangeLog,
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

	publication := Publication{
		Coverage:     result.Coverage,
		CoverageJSON: result.CoverageJSON,
		MessageKey:   "report.published",
	}
	// 變化事件與索引同交易提交；變化日誌關閉時不保留完整歷史，只留執行摘要計數。
	if options.ChangeLog {
		publication.Events = result.Events
		publication.RecordExport = true
	}
	published, err := run.Publish(ctx, publication)
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
	s.reportChanges(run.Mode(), options, result)
	return published.ExitError()
}

// reportChanges 輸出這次更新的變化摘要與必要的限制說明。
//
// 統計行說的是「處理了多少」，這裡說的是「變了什麼」，兩者刻意分開：
// 快速模式沒有讀取每個檔案的內容，因此不能讓使用者以為所有變化都被發現了。
func (s *Service) reportChanges(mode model.RunMode, options Options, result index.Result) {
	if mode == model.RunModeUpdate {
		s.note("report.changeSummary", map[string]string{
			"added":      strconv.FormatInt(result.Added, 10),
			"updated":    strconv.FormatInt(result.Updated, 10),
			"removed":    strconv.FormatInt(result.Removed, 10),
			"excluded":   strconv.FormatInt(result.Excluded, 10),
			"unverified": strconv.FormatInt(result.Unverified, 10),
			"unchanged":  strconv.FormatInt(result.Unchanged, 10),
		})
	}
	if result.HashReused > 0 {
		s.note("report.hashReused", map[string]string{
			"count": strconv.FormatInt(result.HashReused, 10),
		})
	}
	if result.Unverified > 0 {
		s.note("report.unverifiedKept", map[string]string{
			"count": strconv.FormatInt(result.Unverified, 10),
		})
	}
	if options.SHA256 && !options.Deep && mode == model.RunModeUpdate {
		s.note("report.quickModeCaveat", nil)
	}
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

// planMessageKey 依模式與選項選擇開場訊息。
//
// 內容是否會被讀取必須在開始前說清楚：快速模式只讀取必要檔案，
// 細緻模式一定重算全部摘要，關閉保存時則完全不開啟檔案內容。
func planMessageKey(mode model.RunMode, options Options) string {
	if mode == model.RunModeUpdate {
		switch {
		case options.Deep:
			return "report.planUpdateDeep"
		case options.SHA256:
			return "report.planUpdateSha256"
		default:
			return "report.planUpdateNoSha256"
		}
	}
	if options.SHA256 {
		return "report.planCreateSha256"
	}
	return "report.planCreateNoSha256"
}

// inspection 是既有資料庫的預檢結果。
//
// Generation 為 0 表示資料庫存在但尚未發佈任何代次（上一次建立被取消或發佈失敗），
// 這種資料庫沒有任何已發佈結果，重跑等同重新建立。
type inspection struct {
	Exists     bool
	Path       string
	Generation int64
}

// inspectDatabase 以唯讀方式檢查目標資料庫的格式、根目錄身份與上次發佈狀態。
//
// 用唯讀連線的理由與 info 相同：檢查本身不得建立資料庫、不得遷移結構、
// 也不得取得寫入鎖。路徑或卷身份不一致時直接拒絕，絕不自動改綁。
func inspectDatabase(ctx context.Context, dbPath, rootPath string, volume *string) (inspection, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return inspection{Path: dbPath}, nil
		}
		return inspection{}, model.Wrap(model.CodeDatabaseIO, "err.databaseIO", model.ExitFatal,
			map[string]string{"detail": err.Error()}, err)
	}
	storage, err := store.Open(ctx, dbPath, store.Options{ReadOnly: true})
	if err != nil {
		return inspection{}, err
	}
	defer func() { _ = storage.Close() }()
	result := inspection{Exists: true, Path: dbPath}
	bound, hasRoot, err := storage.Root(ctx)
	if err != nil {
		return inspection{}, err
	}
	if !hasRoot {
		// 綁定尚未完成：沒有任何已發佈結果，等同首次建立。
		return result, nil
	}
	// 大小寫策略沿用資料庫記錄的來源環境策略，不用目前系統的假設覆蓋它。
	if model.PathKey(rootPath, bound.CasePolicy) != bound.PathKey {
		return inspection{}, rootMismatch(bound.AbsPath, rootPath)
	}
	if bound.VolumeIdentity != nil && volume != nil && *bound.VolumeIdentity != *volume {
		// 同一個路徑換了卷（例如磁碟代號被重新掛載到別的媒體）：
		// 這不是「檔案被刪光」，而是根目錄被換掉，必須拒絕而不是清空舊記錄。
		return inspection{}, rootMismatch(
			bound.AbsPath+" (volume "+*bound.VolumeIdentity+")",
			rootPath+" (volume "+*volume+")",
		)
	}
	generation, err := storage.PublishedGeneration(ctx)
	if err != nil {
		return inspection{}, err
	}
	result.Generation = generation
	return result, nil
}

// rootMismatch 建立根目錄不一致的錯誤。
func rootMismatch(expected, found string) error {
	return model.New(model.CodeRootMismatch, "err.rootMismatch", model.ExitFatal, map[string]string{
		"expected": expected,
		"found":    found,
	})
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
