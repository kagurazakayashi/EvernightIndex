// Package index 把掃描結果轉成可發佈的索引內容。
//
// 本套件同時支援首次建立與增量更新，兩者共用同一條管線：
//
//	掃描批次 → 取回同路徑的已發佈條目 → 逐條決策（摘要計算/複用/清除）
//	        → 變化分類 → 暫存 → 掃描結束後保守判定消失條目 → 由呼叫端發佈
//
// 契約：
//  1. 掃描結果以批次流式處理：一批進、一批暫存，不在記憶體裡累積整棵樹；
//  2. 已發佈表在發佈交易提交前完全不變：本套件只寫 entry_stage；
//  3. 關閉 SHA256 時完全不開啟檔案內容；開啟時只讀「需要重算」的普通檔案；
//  4. 摘要失敗不留部分結果：摘要清空、狀態 failed、錯誤碼與階段寫入條目，
//     並記入覆蓋缺口，讓結果以「部分完成」呈現；
//  5. 刪除只在覆蓋證據充分時成立：列舉失敗、局部失敗、被排除與跨檔案系統的區域
//     一律保留舊記錄（標為 unverified 或 excluded），絕不當成檔案已刪除；
//  6. 排除規則改變是 excluded 語意，不是刪除；第一版不推斷移動或重命名，
//     新路徑記新增、舊路徑記移除。
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

// defaultRetireBatch 是消失條目判定的暫存批次大小。
const defaultRetireBatch = 256

// Target 是索引內容的寫入目標，由應用層包住一次執行（run）。
//
// 它同時提供讀取已發佈條目的能力，讓索引層不必認識 store：
//   - Lookup 以本次掃描到的路徑批次取回舊條目，用於逐條比對；
//   - Unseen 串流列出「本次沒有再出現」的舊條目，是保守刪除的候選集合；
//   - Stage 與 StageDeletions 只寫暫存區，正式結果仍只能經由發佈交易產生。
type Target interface {
	// RunID 回傳本次執行 ID，會寫入條目的 first_seen_run/last_seen_run。
	RunID() string
	// RootID 回傳綁定的根目錄 ID。
	RootID() int64
	// Stage 暫存一批條目。
	Stage(ctx context.Context, entries []model.Entry) error
	// StageDeletions 暫存一批待刪除路徑；實際刪除只發生在發佈交易內。
	StageDeletions(ctx context.Context, pathKeys []string) error
	// Lookup 依 path_key 取回已發佈條目；不存在的路徑不出現在結果裡。
	Lookup(ctx context.Context, pathKeys []string) (map[string]model.Entry, error)
	// Unseen 依路徑順序串流「本次沒有暫存」的已發佈條目。
	Unseen(ctx context.Context, visit func(model.Entry) error) error
}

// Options 是一次建索引的設定。
type Options struct {
	// Root 是掃描根目錄的絕對路徑。
	Root string
	// CasePolicy 是根目錄的實際大小寫策略。
	CasePolicy model.CasePolicy
	// SHA256 表示是否讀取內容並保存摘要。
	SHA256 bool
	// Deep 表示細緻模式：所有可讀取的普通檔案都重新計算摘要。
	Deep bool
	// ChangeLog 表示要保留完整的變化歷史（before/after 快照）。
	//
	// 關閉時只保留變化分類計數：需求要求變化日誌關閉時不長期保存完整歷史，
	// 但執行摘要仍必須說清楚這次發生了什麼。
	ChangeLog bool
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
	// HashRequests 是送出的摘要請求數，Hashed 與 HashFailed 是終態數，
	// HashReused 是沿用舊摘要（未讀取內容）的條目數。
	HashRequests int64
	Hashed       int64
	HashReused   int64
	HashFailed   int64
	// HashBaseline 是「原本沒有摘要、這次補上基線」的條目數，
	// HashInvalidated 是「快速判據變化、舊摘要被清除」的條目數。
	HashBaseline    int64
	HashInvalidated int64
	// 條目的變化分類計數；同一條目只會落在一個分類裡。
	Added         int64
	Updated       int64
	Removed       int64
	Excluded      int64
	Unverified    int64
	Unchanged     int64
	ContentChange int64
	MetaChange    int64
	TypeChanged   int64
	Restored      int64
	// Events 是變化事件（before/after 快照）；ChangeLog 關閉時為空。
	Events []model.Event
	// ContentOpens 與 BytesRead 是可觀察的內容讀取證據：
	// 關閉 SHA256 時兩者都必須是 0。
	ContentOpens int64
	BytesRead    int64
}

// Partial 表示這次結果有覆蓋缺口，應以部分完成發佈。
func (r Result) Partial() bool { return r.Coverage == model.CoveragePartial }

// Changes 回傳這次是否真的有條目變化（新增、更新或移除）。
func (r Result) Changes() bool { return r.Added > 0 || r.Updated > 0 || r.Removed > 0 }

// builder 保存一次建索引過程的共用狀態。
type builder struct {
	target    Target
	options   Options
	now       func() time.Time
	hasher    *hash.Hasher
	counter   *hash.Counter
	excluder  *scan.Excluder
	result    Result
	batchSize int
	// regions 是「本次沒有成功覆蓋」的相對路徑前綴，保守刪除的唯一依據。
	regions []region
	// entriesFailed 收集條目級失敗，最後併入 coverage_json。
	entriesFailed []scan.Gap
	// pending 是 retired 條目的暫存緩衝。
	pendingUpserts   []model.Entry
	pendingDeletions []string
}

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
	b := &builder{
		target:    target,
		options:   options,
		now:       now,
		excluder:  scan.NewExcluder(options.Excludes, options.CasePolicy, options.SelfFiles, options.SelfDirs),
		batchSize: effectiveBatch(options.BatchSize),
	}
	if options.SHA256 {
		b.counter = &hash.Counter{}
		b.hasher = hash.New(hash.Options{
			Workers:     options.Workers,
			ReadRetries: options.ReadRetries,
			BufferSize:  options.HashBufferSize,
			Stats:       options.Stats,
			Counter:     b.counter,
			AfterRead:   options.HashAfterRead,
		})
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
		Sink:          b.consume,
		OnEvent:       b.onEvent,
		Stats:         options.Stats,
		Now:           now,
	})
	b.result.Scan = walked
	if b.counter != nil {
		b.result.ContentOpens = b.counter.Opens()
		b.result.BytesRead = b.counter.Bytes()
	}
	if walkErr != nil {
		b.result.Coverage = model.CoverageUnknown
		return b.result, walkErr
	}

	// 保守刪除只在整棵樹走完之後才可能成立：中途中止時沒有任何覆蓋證據。
	b.regions = b.buildRegions(walked.Coverage)
	if err := b.retire(ctx); err != nil {
		b.result.Coverage = model.CoverageUnknown
		return b.result, err
	}

	coverage := walked.Coverage
	coverage.FailedEntries = append(coverage.FailedEntries, b.entriesFailed...)
	// 被排除的目錄仍然記進 coverage（保守刪除判定需要它），但那是使用者自己要的
	// 排除，不是「這次沒能覆蓋」，因此不讓它把結果降級成部分完成。
	if incompleteCoverage(coverage, walked.EntryErrors) {
		b.result.Coverage = model.CoveragePartial
	} else {
		b.result.Coverage = model.CoverageComplete
	}
	encoded, err := coverage.JSON()
	if err != nil {
		return b.result, internalError("coverage JSON: " + err.Error())
	}
	b.result.CoverageJSON = encoded
	if b.counter != nil {
		b.result.ContentOpens = b.counter.Opens()
		b.result.BytesRead = b.counter.Bytes()
	}
	return b.result, nil
}

// consume 處理一批掃描到的條目：比對舊值、決定摘要、分類、暫存。
func (b *builder) consume(ctx context.Context, batch []model.Entry) error {
	keys := make([]string, len(batch))
	for position := range batch {
		keys[position] = batch[position].PathKey
	}
	published, err := b.target.Lookup(ctx, keys)
	if err != nil {
		return err
	}
	plans := make([]plan, len(batch))
	requests := make([]hash.Request, 0, len(batch))
	targets := make([]int, 0, len(batch))
	for position := range batch {
		old, exists := published[batch[position].PathKey]
		plans[position] = b.planEntry(batch[position], old, exists)
		if !plans[position].computeHash {
			continue
		}
		request := hash.Request{AbsPath: plans[position].entry.AbsPath, Size: plans[position].entry.Size, Mtime: plans[position].entry.Mtime}
		if identity, ok := hash.IdentityOfEntry(plans[position].entry); ok {
			request.Identity = identity
		}
		requests = append(requests, request)
		targets = append(targets, position)
	}

	if len(requests) > 0 {
		results, err := b.hasher.Compute(ctx, requests)
		if err != nil {
			return err
		}
		b.result.HashRequests += int64(len(requests))
		for position := range results {
			b.applyHash(&plans[targets[position]], results[position])
		}
	}

	prepared := make([]model.Entry, len(plans))
	for position := range plans {
		prepared[position] = plans[position].entry
	}
	if err := b.target.Stage(ctx, prepared); err != nil {
		return err
	}
	b.result.Entries += int64(len(prepared))
	b.result.Batches++
	for position := range plans {
		if err := b.finish(&plans[position]); err != nil {
			return err
		}
	}
	return nil
}

// onEvent 把掃描事件轉成索引層的可觀察輸出與覆蓋缺口。
func (b *builder) onEvent(event scan.Event) {
	switch event.Kind {
	case scan.EventEntryFailed:
		b.entriesFailed = append(b.entriesFailed, scan.Gap{
			RelPath: event.RelPath,
			Code:    event.Code,
			Detail:  event.Detail,
		})
		observe(b.options.Observe, model.OpFailed, event.RelPath)
	case scan.EventDirFailed:
		observe(b.options.Observe, model.OpFailed, event.RelPath)
	case scan.EventEntryExcluded:
		observe(b.options.Observe, model.OpExcluded, event.RelPath)
	}
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

// effectiveBatch 套用批次預設值。
func effectiveBatch(requested int) int {
	if requested > 0 {
		return requested
	}
	return defaultRetireBatch
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
