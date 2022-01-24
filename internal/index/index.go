// Package index 把掃描結果轉成可發佈的索引內容。
//
// 本步驟只做「首次建立」：資料庫還沒有已發佈代次時，把整棵目錄樹寫進暫存區，
// 依 SHA256 選項決定要不要讀取檔案內容，最後由呼叫端在交易內發佈。
// 增量更新（快速判據比對、摘要重用、保守刪除判定）不在本步驟範圍內，
// 相關入口刻意留白，不提供半套實作。
//
// 契約：
//  1. 掃描結果以批次流式處理：一批進、一批暫存，不在記憶體裡累積整棵樹；
//  2. 關閉 SHA256 時完全不開啟檔案內容，條目留下 disabled 狀態，目錄與特殊檔案
//     一律 not_applicable；
//  3. 摘要失敗不留部分結果：摘要清空、狀態 failed、錯誤碼與階段寫入條目，
//     並記入覆蓋缺口，讓結果以「部分完成」呈現；
//  4. 條目終態以這裡寫入的值為準，--deep 對首次建立等同全量計算。
package index

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/hash"
	"github.com/kagurazakayashi/EvernightIndex/internal/match"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
	"github.com/kagurazakayashi/EvernightIndex/internal/scan"
)

// errorStageHash 是摘要階段寫入條目 error_stage 的固定值。
const errorStageHash = "hash"

// Target 是索引內容的寫入目標，由應用層包住一次執行（run）。
//
// 只暴露暫存能力：正式結果只能經由發佈交易產生，索引層不得直接改寫已發佈表。
type Target interface {
	// RunID 回傳本次執行 ID，會寫入條目的 first_seen_run/last_seen_run。
	RunID() string
	// RootID 回傳綁定的根目錄 ID。
	RootID() int64
	// Stage 暫存一批條目。
	Stage(ctx context.Context, entries []model.Entry) error
}

// Options 是一次建索引的設定。
type Options struct {
	// Root 是掃描根目錄的絕對路徑。
	Root string
	// CasePolicy 是根目錄的實際大小寫策略。
	CasePolicy model.CasePolicy
	// SHA256 表示是否讀取內容並保存摘要。
	SHA256 bool
	// Deep 表示細緻模式；首次建立時等同全量計算，但仍保留參數以記錄掃描設定。
	Deep bool
	// OneFileSystem 限制不跨檔案系統。
	OneFileSystem bool
	// Workers 是摘要 worker 數；0 表示預設 1，上限由 hash.EffectiveWorkers 夾住。
	Workers int
	// ReadRetries 是偵測到不穩定後的重試次數。
	ReadRetries int
	// Excludes 是編譯後的排除模式。
	Excludes *match.Set
	// SelfFiles 與 SelfDirs 是本程式自產檔案與本次專屬目錄。
	SelfFiles []string
	SelfDirs  []string
	// Stats 可為 nil。
	Stats *output.Stats
	// BatchSize 是送進暫存的批次大小；0 表示掃描層預設值。
	BatchSize int
	// Observe 可為 nil；不為 nil 時對每個條目終態回報一次，供 -v 輸出。
	Observe func(kind model.OpKind, relPath string)
	// Now 可為 nil，預設 time.Now。
	Now func() time.Time
	// HashBufferSize 與 HashAfterRead 只供測試注入。
	HashBufferSize int
	HashAfterRead  func()
}

// Result 是一次建索引的可觀察結果。
type Result struct {
	// Scan 是掃描層的原始摘要。
	Scan scan.Result
	// Coverage 是含摘要失敗在內的覆蓋狀態。
	Coverage model.Coverage
	// CoverageJSON 是覆蓋狀態的穩定 JSON。
	CoverageJSON string
	// Entries 是已暫存的條目數，Batches 是暫存批次數。
	Entries int64
	Batches int64
	// HashRequests 是送出的摘要請求數，Hashed 與 HashFailed 是終態數。
	HashRequests int64
	Hashed       int64
	HashFailed   int64
	// ContentOpens 與 BytesRead 是可觀察的內容讀取證據：
	// 關閉 SHA256 時兩者都必須是 0。
	ContentOpens int64
	BytesRead    int64
}

// Partial 表示這次結果有覆蓋缺口，應以部分完成發佈。
func (r Result) Partial() bool { return r.Coverage == model.CoveragePartial }

// Build 掃描根目錄並把結果寫入目標的暫存區。
//
// 呼叫端負責發佈或放棄：本函式只寫暫存，不呼叫 Publish，
// 也不改動已發佈的索引與代次。
func Build(ctx context.Context, target Target, options Options) (Result, error) {
	if target == nil {
		return Result{}, internalError("index target is required")
	}
	if strings.TrimSpace(options.Root) == "" {
		return Result{}, internalError("scan root is required")
	}
	// 細緻更新必然重算摘要，與關閉 SHA256 互斥；命令列層已先拒絕，
	// 這裡再拒絕一次，讓直接呼叫本套件的路徑不會繞過該約定。
	if options.Deep && !options.SHA256 {
		return Result{}, model.New(model.CodeFlagConflict, "err.conflictDeepNoSha256", model.ExitUsage, nil)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	result := Result{}
	var entriesFailed []scan.Gap
	counter := &hash.Counter{}
	var hasher *hash.Hasher
	if options.SHA256 {
		hasher = hash.New(hash.Options{
			Workers:     options.Workers,
			ReadRetries: options.ReadRetries,
			BufferSize:  options.HashBufferSize,
			Stats:       options.Stats,
			Counter:     counter,
			AfterRead:   options.HashAfterRead,
		})
	}

	sink := func(sinkCtx context.Context, batch []model.Entry) error {
		prepared := make([]model.Entry, len(batch))
		requests := make([]hash.Request, 0, len(batch))
		targets := make([]int, 0, len(batch))
		for position := range batch {
			entry := batch[position]
			if entry.Type != model.EntryTypeFile {
				// 目錄、連結與特殊檔案沒有「主資料內容」，不參與摘要。
				entry.HashSHA256 = nil
				entry.HashState = model.HashStateNotApplicable
				prepared[position] = entry
				continue
			}
			if !options.SHA256 {
				// 明確的「未啟用」狀態：沒有讀過內容，也沒有任何摘要可比較。
				entry.HashSHA256 = nil
				entry.HashState = model.HashStateDisabled
				prepared[position] = entry
				continue
			}
			entry.HashState = model.HashStateNotComputed
			identity, hasIdentity := hash.IdentityOfEntry(entry)
			request := hash.Request{AbsPath: entry.AbsPath, Size: entry.Size, Mtime: entry.Mtime}
			if hasIdentity {
				request.Identity = identity
			}
			requests = append(requests, request)
			targets = append(targets, position)
			prepared[position] = entry
		}

		if len(requests) > 0 {
			results, err := hasher.Compute(sinkCtx, requests)
			if err != nil {
				return err
			}
			result.HashRequests += int64(len(requests))
			for position := range results {
				outcome := results[position]
				entry := &prepared[targets[position]]
				if outcome.Computed() {
					digest := outcome.Digest
					entry.HashSHA256 = &digest
					entry.HashState = model.HashStateComputed
					computedAt := model.NewTimestamp(now())
					entry.HashComputedAt = &computedAt
					size := outcome.Size
					entry.HashSize = &size
					if outcome.Mtime != "" {
						mtime := outcome.Mtime
						entry.HashMTime = &mtime
					}
					entry.ErrorCode = nil
					entry.ErrorStage = nil
					result.Hashed++
					observe(options.Observe, model.OpHashed, entry.RelPath)
					continue
				}
				code := outcome.Code
				if code == "" {
					code = model.CodeHashRead
				}
				codeText := string(code)
				stage := errorStageHash
				entry.HashSHA256 = nil
				entry.HashState = model.HashStateFailed
				entry.HashComputedAt = nil
				entry.HashSize = nil
				entry.HashMTime = nil
				entry.ErrorCode = &codeText
				entry.ErrorStage = &stage
				entriesFailed = append(entriesFailed, scan.Gap{
					RelPath: entry.RelPath,
					Code:    code,
					Detail:  outcome.Detail,
				})
				result.HashFailed++
				if options.Stats != nil {
					options.Stats.AddErrorEvent(1)
				}
				observe(options.Observe, model.OpFailed, entry.RelPath)
			}
		}

		if err := target.Stage(sinkCtx, prepared); err != nil {
			return err
		}
		result.Entries += int64(len(prepared))
		result.Batches++
		for position := range prepared {
			observe(options.Observe, model.OpAdded, prepared[position].RelPath)
		}
		return nil
	}

	onEvent := func(event scan.Event) {
		switch event.Kind {
		case scan.EventEntryFailed:
			entriesFailed = append(entriesFailed, scan.Gap{
				RelPath: event.RelPath,
				Code:    event.Code,
				Detail:  event.Detail,
			})
			observe(options.Observe, model.OpFailed, event.RelPath)
		case scan.EventDirFailed:
			observe(options.Observe, model.OpFailed, event.RelPath)
		case scan.EventEntryExcluded:
			observe(options.Observe, model.OpExcluded, event.RelPath)
		}
	}

	walked, walkErr := scan.Walk(ctx, scan.Options{
		Root:          options.Root,
		CasePolicy:    options.CasePolicy,
		RunID:         target.RunID(),
		Excludes:      options.Excludes,
		SelfFiles:     options.SelfFiles,
		SelfDirs:      options.SelfDirs,
		OneFileSystem: options.OneFileSystem,
		BatchSize:     options.BatchSize,
		Sink:          sink,
		OnEvent:       onEvent,
		Stats:         options.Stats,
		Now:           now,
	})
	result.Scan = walked
	result.ContentOpens = counter.Opens()
	result.BytesRead = counter.Bytes()
	if walkErr != nil {
		result.Coverage = model.CoverageUnknown
		return result, walkErr
	}

	coverage := walked.Coverage
	coverage.FailedEntries = append(coverage.FailedEntries, entriesFailed...)
	// 被排除的目錄仍然記進 coverage（保守刪除判定需要它），但那是使用者自己要的
	// 排除，不是「這次沒能覆蓋」，因此不讓它把結果降級成部分完成。
	if incompleteCoverage(coverage, walked.EntryErrors) {
		result.Coverage = model.CoveragePartial
	} else {
		result.Coverage = model.CoverageComplete
	}
	encoded, err := coverage.JSON()
	if err != nil {
		return result, internalError("coverage JSON: " + err.Error())
	}
	result.CoverageJSON = encoded
	return result, nil
}

// incompleteCoverage 判斷是否有「非使用者選擇」的覆蓋缺口。
//
// 只有列舉失敗、條目處理失敗與摘要失敗算缺口；使用者指定的排除不算失敗。
func incompleteCoverage(coverage scan.Coverage, entryErrors int64) bool {
	return len(coverage.PartialDirs) > 0 ||
		len(coverage.FailedDirs) > 0 ||
		len(coverage.FailedEntries) > 0 ||
		entryErrors > 0
}

// observe 在設定回呼時回報一次條目終態。
func observe(callback func(kind model.OpKind, relPath string), kind model.OpKind, relPath string) {
	if callback == nil {
		return
	}
	callback(kind, relPath)
}

// IsCanceled 判斷錯誤是否來自使用者取消。
//
// 取消與一般失敗的收尾狀態不同：前者記 interrupted，後者記 failed。
func IsCanceled(ctx context.Context, err error) bool {
	if err == nil {
		return ctx.Err() != nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return ctx.Err() != nil
}

// internalError 建立內部錯誤。
func internalError(detail string) error {
	return model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{"detail": detail})
}
