// Package app 編排任務：命令列層只負責解析與呈現，掃描、索引與儲存細節在此組合。
//
// 本套件是「執行生命週期」的所在：開始時取得正式目標寫入鎖、開啟資料庫、
// 綁定根目錄並建立執行記錄；結束時只有兩條合法出路——發佈交易成功，
// 或明確記錄未發佈的收尾狀態。底層套件不呼叫 os.Exit，退出碼由 CLI 決定。
package app

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/i18n"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
	"github.com/kagurazakayashi/EvernightIndex/internal/store"
)

// Deps 是應用服務需要的呈現依賴；測試可以只給需要的那幾項。
type Deps struct {
	Console *output.Console
	Catalog *i18n.Catalog
	Lang    i18n.Lang
}

// Options 是命令列選項轉成的應用層參數。
//
// 只放已實作行為會用到的欄位；後續步驟新增選項時在此集中擴充。
type Options struct {
	DB            string
	LogDir        string
	TempDir       string
	SHA256        bool
	Deep          bool
	ChangeLog     bool
	OneFileSystem bool
	Workers       int
	ReadRetries   int
	LockTimeout   time.Duration
	Excludes      []string
}

// Request 是一次命令請求。
type Request struct {
	Command string
	Args    []string
	Options Options
}

// Service 是應用服務入口。
type Service struct {
	deps Deps
}

// New 建立應用服務。
func New(deps Deps) *Service { return &Service{deps: deps} }

// Run 執行請求。
//
// 尚未實作的命令明確回報 E_NOT_IMPLEMENTED，不回傳空結果假裝成功，
// 也不建立資料庫或掃描目錄。
func (s *Service) Run(ctx context.Context, request Request) error {
	if err := ctx.Err(); err != nil {
		return model.Wrap(model.CodeInterrupted, "err.interrupted", model.ExitInterrupted, nil, err)
	}
	switch request.Command {
	case "index":
		return s.runIndex(ctx, request)
	case "info":
		return s.runInfo(ctx, request)
	default:
		return model.New(model.CodeNotImplemented, "err.notImplemented", model.ExitFatal, map[string]string{
			"command": request.Command,
		})
	}
}

// runInfo 輸出資料庫的識別、綁定狀態、代次與統計。
//
// info 是唯讀命令：以唯讀模式開啟，不建立資料庫、不遷移結構、不取得寫入鎖。
// 輸出使用穩定的英語欄位名與列舉值，供腳本解析；人類可讀的進度與錯誤仍走
// 四語言資源。
func (s *Service) runInfo(ctx context.Context, request Request) error {
	dbPath := strings.TrimSpace(request.Options.DB)
	if dbPath == "" {
		return model.New(model.CodeMissingArgument, "err.missingArgument", model.ExitUsage, map[string]string{
			"command":  "info",
			"argument": "--db <path>",
		})
	}
	storage, err := store.Open(ctx, dbPath, store.Options{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = storage.Close() }()

	uuid, err := storage.Meta(ctx, "db_uuid")
	if err != nil {
		return err
	}
	root, bound, err := storage.Root(ctx)
	if err != nil {
		return err
	}
	summary, err := storage.EntrySummary(ctx, root.ID)
	if err != nil {
		return err
	}
	latest, hasRun, err := storage.LatestRun(ctx)
	if err != nil {
		return err
	}
	exports, err := storage.PendingExports(ctx)
	if err != nil {
		return err
	}

	s.result("database: %s", dbPath)
	s.result("schema_version: %d", store.SchemaVersion)
	s.result("db_uuid: %s", uuid)
	if !bound {
		s.result("root: unbound")
		s.result("index_state: incomplete")
	} else {
		s.result("root: %s", root.AbsPath)
		s.result("source_platform: %s", root.SourcePlatform)
		s.result("case_policy: %s", root.CasePolicy)
		generation := "-"
		publishedRun := "-"
		publishedAt := "-"
		if root.PublishedGeneration != nil {
			generation = strconv.FormatInt(*root.PublishedGeneration, 10)
			publishedRun = valueOrDash(root.PublishedRunID)
			if root.PublishedAt != nil {
				publishedAt = root.PublishedAt.String()
			}
		}
		s.result("generation: %s", generation)
		s.result("published_run: %s", publishedRun)
		s.result("published_at: %s", publishedAt)
		if root.PublishedGeneration == nil {
			// 首次建立被取消或發佈失敗時就是這個狀態：不能當成完整索引。
			s.result("index_state: incomplete (no published generation)")
		} else {
			s.result("index_state: complete")
		}
	}
	s.result("entries: %d (files %d, dirs %d, symlinks %d, special %d)",
		summary.Total, summary.Files, summary.Dirs, summary.Symlinks, summary.Special)
	s.result("hashes: computed %d, reused %d, not_computed %d, not_applicable %d, failed %d, disabled %d",
		summary.HashComputed, summary.HashReused, summary.HashNotComputed,
		summary.HashNotApplicable, summary.HashFailed, summary.HashDisabled)
	if !hasRun {
		s.result("last_run: none")
	} else {
		finished := "-"
		if latest.FinishedAt != nil {
			finished = latest.FinishedAt.String()
		}
		generation := "-"
		if latest.Generation != nil {
			generation = strconv.FormatInt(*latest.Generation, 10)
		}
		s.result("last_run: %s status=%s coverage=%s generation=%s started=%s finished=%s",
			latest.ID, latest.Status, latest.Coverage, generation, latest.StartedAt, finished)
	}
	s.result("pending_exports: %d", len(exports))
	for _, export := range exports {
		s.result("pending_export: run=%s state=%s events=%d attempts=%d target=%s",
			export.RunID, export.State, export.EventCount, export.Attempts, valueOrDash(export.TargetPath))
	}
	return nil
}

// result 把結果行寫到標準輸出；沒有主控台時（單元測試）靜默略過。
func (s *Service) result(format string, args ...any) {
	if s.deps.Console == nil {
		return
	}
	s.deps.Console.Result(format, args...)
}

// valueOrDash 把可為 nil 的字串轉成輸出值。
func valueOrDash(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}
