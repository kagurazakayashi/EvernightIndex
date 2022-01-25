// Package scan 走訪一個根目錄樹，產生條目流與覆蓋事件。
//
// 職責邊界：
//   - 本套件只「看」檔案系統：採集中繼資料、決定走進哪些目錄、表達覆蓋與失敗，
//     不讀取任何普通檔案內容（SHA256 由 internal/hash 負責），也不寫資料庫。
//   - 條目以批次交給 Sink，由單一 goroutine 呼叫，因此呼叫端不必自己同步；
//     事件（目錄列舉成功/局部失敗/失敗、項目被排除、條目採集失敗、根目錄失效）同樣單一執行緒送出。
//   - 覆蓋缺口（Coverage）是後續保守刪除的唯一依據：只有成功列舉的目錄才能確認子項消失，
//     被排除、跨檔案系統或列舉失敗的區域一律記成缺口，不得當成刪除。
//   - 任何失敗都不會讓可繼續的掃描停下；只有根目錄失效與 Sink 錯誤會中止整次走訪。
//
// 併發模型：固定數量的 worker 從有界任務佇列取目錄，各自列舉並採集子項，
// 批次與事件經有界通道送到單一 collector。取消透過 context，任何一步都能中止；
// 已送出的批次不會被回收，因此呼叫端必須把取消視為「不完整」。
package scan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/fsmeta"
	"github.com/kagurazakayashi/EvernightIndex/internal/match"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
)

// defaultBatchSize 是每次交給 Sink 的條目上限。
const defaultBatchSize = 256

// maxBatchSize 是批次大小上限，避免單一批次佔用過多記憶體。
const maxBatchSize = 4096

// rootRelPath 是根目錄自身在索引中的相對路徑。
//
// 用 "." 而不是空字串：資料庫把路徑鍵與相對路徑當成必要欄位，
// 空字串會讓「根目錄」與「缺值」無法區分，也讓前綴查詢失去錨點。
const rootRelPath = "."

// pathSeparators 是清理路徑尾端時要一起處理的分隔符集合。
var pathSeparators = "/" + string(filepath.Separator)

// ExcludeReason 是條目被移出索引的原因。
type ExcludeReason string

const (
	// ExcludeReasonPattern 表示命中顯式排除模式。
	ExcludeReasonPattern ExcludeReason = "pattern"
	// ExcludeReasonSelfProduced 表示是本程式自產的檔案或目錄。
	ExcludeReasonSelfProduced ExcludeReason = "self_produced"
	// ExcludeReasonCrossDevice 表示跨越根目錄所在檔案系統（--one-file-system）。
	ExcludeReasonCrossDevice ExcludeReason = "cross_device"
)

// MessageKey 回傳原因的 i18n 訊息鍵。
func (r ExcludeReason) MessageKey() string {
	switch r {
	case ExcludeReasonPattern:
		return "scan.reason.pattern"
	case ExcludeReasonSelfProduced:
		return "scan.reason.selfProduced"
	case ExcludeReasonCrossDevice:
		return "scan.reason.crossDevice"
	default:
		return "err.internal"
	}
}

// Valid 表示原因是否屬於已知列舉。
func (r ExcludeReason) Valid() bool {
	switch r {
	case ExcludeReasonPattern, ExcludeReasonSelfProduced, ExcludeReasonCrossDevice:
		return true
	}
	return false
}

// EventKind 是覆蓋與錯誤事件的種類。
type EventKind string

const (
	// EventDirEnumerated 表示一個目錄被完整列舉。
	EventDirEnumerated EventKind = "dir_enumerated"
	// EventDirPartial 表示目錄被列舉，但有條目採集失敗。
	EventDirPartial EventKind = "dir_partial"
	// EventDirFailed 表示子目錄完全無法列舉（權限、離線、消失）。
	EventDirFailed EventKind = "dir_failed"
	// EventEntryExcluded 表示一個條目因排除規則被移出索引。
	EventEntryExcluded EventKind = "entry_excluded"
	// EventEntryFailed 表示單一條目的中繼資料採集失敗。
	EventEntryFailed EventKind = "entry_failed"
	// EventRootInvalid 表示根目錄本身無法列舉，整次掃描因此中止。
	EventRootInvalid EventKind = "root_invalid"
)

// MessageKey 回傳事件種類的 i18n 訊息鍵。
func (k EventKind) MessageKey() string {
	switch k {
	case EventDirEnumerated:
		return "scan.dir.enumerated"
	case EventDirPartial:
		return "scan.dir.partial"
	case EventDirFailed:
		return "scan.dir.failed"
	case EventEntryExcluded:
		return "scan.entry.excluded"
	case EventEntryFailed:
		return "scan.entry.failed"
	case EventRootInvalid:
		return "scan.root.invalid"
	default:
		return "err.internal"
	}
}

// Event 是一筆覆蓋或錯誤事件。
//
// 事件只描述「掃描過程中發生了什麼」，不承載渲染後文字：Kind、Code、Reason 都是
// 穩定的機器值，人類可讀文字由呈現層用 MessageKey 與 MessageArgs 產生。
type Event struct {
	Kind    EventKind
	RelPath string
	AbsPath string
	// Count 是該目錄看見的項目數（僅列舉類事件有意義）。
	Count int64
	// Reason 是排除原因（僅排除事件有值）。
	Reason ExcludeReason
	// Code 是穩定錯誤碼（僅失敗類事件有值）。
	Code model.ErrorCode
	// Detail 是原始錯誤描述，供除錯；不保證跨版本穩定。
	Detail string
}

// MessageArgs 產生訊息的佔位符參數。
//
// reasonText 是呈現層依當前語言提供的可讀原因；傳空字串時使用機器值
// （排除原因用 Reason 列舉，其他事件用 Detail），方便除錯輸出。
func (e Event) MessageArgs(reasonText string) map[string]string {
	if reasonText == "" {
		switch {
		case e.Reason != "":
			reasonText = string(e.Reason)
		case e.Detail != "":
			reasonText = e.Detail
		default:
			reasonText = string(e.Code)
		}
	}
	displayPath := model.EscapeDisplay(e.RelPath)
	if displayPath == "" {
		displayPath = model.EscapeDisplay(e.AbsPath)
	}
	return map[string]string{
		"path":   displayPath,
		"count":  strconvItoa(e.Count),
		"reason": reasonText,
	}
}

// Gap 是一段未被成功覆蓋的區域（相對路徑前綴）。
type Gap struct {
	RelPath string
	Code    model.ErrorCode
	Detail  string
}

// Excluded 是一段被排除、因此不能被當成刪除依據的區域。
type Excluded struct {
	RelPath string
	Reason  ExcludeReason
}

// Coverage 是一次掃描的覆蓋證據。
//
// 缺口只記錄目錄級資訊：條目級失敗由 PartialDirs 表達；
// 被排除的檔案不進缺口（可以事後用同一組模式重新判定），
// 只有被排除的目錄與跨檔案系統的掛載點會成為缺口，因為那些子樹根本沒有被看過。
type Coverage struct {
	// PartialDirs 是成功列舉、但有條目失敗的目錄。
	PartialDirs []Gap
	// FailedDirs 是根本無法列舉的目錄。
	FailedDirs []Gap
	// ExcludedDirs 是被排除且未進入的目錄。
	ExcludedDirs []Excluded
	// FailedEntries 是單一條目失敗的紀錄。
	//
	// 掃描本身只產生目錄級缺口；索引層在摘要失敗或條目採集失敗時
	// 把具體路徑補進這裡，讓「部分完成」能說明是哪些檔案沒有成功，
	// 而不必重新掃描或猜測。
	FailedEntries []Gap
}

// Complete 表示沒有任何覆蓋缺口。
func (c Coverage) Complete() bool {
	return len(c.PartialDirs) == 0 && len(c.FailedDirs) == 0 &&
		len(c.ExcludedDirs) == 0 && len(c.FailedEntries) == 0
}

// coverageJSON 是 Coverage 的穩定 JSON 表示。
type coverageJSON struct {
	PartialDirs   []gapJSON      `json:"partial_dirs"`
	FailedDirs    []gapJSON      `json:"failed_dirs"`
	ExcludedDirs  []excludedJSON `json:"excluded_dirs"`
	FailedEntries []gapJSON      `json:"failed_entries"`
}

// gapJSON 是一段缺口的序列化形式。
type gapJSON struct {
	RelPath string `json:"rel_path"`
	Code    string `json:"code"`
	Detail  string `json:"detail,omitempty"`
}

// excludedJSON 是一段被排除目錄的序列化形式。
type excludedJSON struct {
	RelPath string `json:"rel_path"`
	Reason  string `json:"reason"`
}

// JSON 把覆蓋結果序列化成穩定鍵序的 JSON；沒有缺口時仍輸出空陣列。
func (c Coverage) JSON() (string, error) {
	encoded := coverageJSON{
		PartialDirs:   make([]gapJSON, 0, len(c.PartialDirs)),
		FailedDirs:    make([]gapJSON, 0, len(c.FailedDirs)),
		ExcludedDirs:  make([]excludedJSON, 0, len(c.ExcludedDirs)),
		FailedEntries: make([]gapJSON, 0, len(c.FailedEntries)),
	}
	for _, gap := range c.PartialDirs {
		encoded.PartialDirs = append(encoded.PartialDirs, gapJSON{RelPath: gap.RelPath, Code: string(gap.Code), Detail: gap.Detail})
	}
	for _, gap := range c.FailedDirs {
		encoded.FailedDirs = append(encoded.FailedDirs, gapJSON{RelPath: gap.RelPath, Code: string(gap.Code), Detail: gap.Detail})
	}
	for _, excluded := range c.ExcludedDirs {
		encoded.ExcludedDirs = append(encoded.ExcludedDirs, excludedJSON{RelPath: excluded.RelPath, Reason: string(excluded.Reason)})
	}
	for _, gap := range c.FailedEntries {
		encoded.FailedEntries = append(encoded.FailedEntries, gapJSON{RelPath: gap.RelPath, Code: string(gap.Code), Detail: gap.Detail})
	}
	raw, err := json.Marshal(encoded)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Result 是一次掃描的結果摘要。
type Result struct {
	// Root 是解析後的根目錄資訊。
	Root Root
	// Entries 是送出的條目數。
	Entries int64
	// DirsEnumerated、DirsPartial、DirsFailed 是目錄列舉的三種結果數。
	DirsEnumerated int64
	DirsPartial    int64
	DirsFailed     int64
	// Excluded 是被排除的條目數（檔案與目錄各計一次）。
	Excluded int64
	// EntryErrors 是條目級採集失敗數，與目錄失敗分開。
	EntryErrors int64
	// Coverage 是覆蓋證據；缺口非空代表結果不能當成完整一次掃描。
	Coverage Coverage
	// Interrupted 表示因為 context 取消而提前結束。
	Interrupted bool
}

// Options 描述一次掃描。
type Options struct {
	// Root 是掃描根目錄的絕對路徑（呼叫端已按呼叫時工作目錄解析）。
	Root string
	// CasePolicy 是根目錄環境的路徑比較策略，用於 path_key 與自產檔案比對。
	CasePolicy model.CasePolicy
	// RunID 寫入條目的 first_seen_run 與 last_seen_run。
	RunID string
	// Excludes 是顯式排除模式（已編譯）；nil 表示沒有模式排除。
	Excludes *match.Set
	// SelfFiles 是本程式自產檔案的絕對路徑；只有這些路徑本身被排除。
	SelfFiles []string
	// SelfDirs 是本程式自產目錄的絕對路徑；該目錄與其後代都被排除。
	// 只放本次執行專屬的工作子目錄與明確指定的日誌目錄，不要放整個臨時父目錄。
	SelfDirs []string
	// OneFileSystem 表示不跨越根目錄所在的檔案系統。
	OneFileSystem bool
	// Workers 是並行列舉目錄的 goroutine 數；<=0 視為 1，上限為 max(1, CPU/2)。
	Workers int
	// BatchSize 是每次交給 Sink 的條目上限；<=0 使用 256，上限 4096。
	BatchSize int
	// QueueDepth 是有界佇列深度；<=0 使用 max(4, Workers*2)。
	QueueDepth int
	// Sink 接收條目批次，由單一 goroutine 呼叫；回傳錯誤即中止掃描。
	Sink func(ctx context.Context, entries []model.Entry) error
	// OnEvent 接收事件，由單一 goroutine 呼叫；可為 nil。
	OnEvent func(Event)
	// Stats 是共用統計；可為 nil。
	Stats *output.Stats
	// Now 供測試注入時鐘；nil 用 time.Now。
	Now func() time.Time
}

// task 是一個待列舉的目錄。
type task struct {
	relPath string
	absPath string
}

// message 是 worker 送到 collector 的一筆輸出：批次或事件。
type message struct {
	entries []model.Entry
	event   *Event
}

// walker 保存一次掃描的共用狀態。
type walker struct {
	opts       Options
	root       Root
	rootVolume string
	excluder   *Excluder
	batchSize  int
	messages   chan message
	pending    atomic.Int64
	// 任務佇列刻意不用 channel：channel 滿了會把「生產者」自己擋住，
	// 而生產者正是唯一能消耗任務的 worker，單一 worker 時會直接自鎖。
	// 這裡改用互斥鎖 + 條件變數，佇列滿時原地處理，既不阻塞生產者又有界。
	queueMu    sync.Mutex
	queueCond  *sync.Cond
	queue      []task
	queueLimit int
	queueDone  bool
	entryCount atomic.Int64
	dirOK      atomic.Int64
	dirPartial atomic.Int64
	dirFailed  atomic.Int64
	excluded   atomic.Int64
	entryErrs  atomic.Int64
	gapsMu     sync.Mutex
	coverage   Coverage
	errOnce    sync.Once
	sinkErr    error
	rootOnce   sync.Once
	rootErr    error
	cancel     context.CancelFunc
}

// Walk 走訪 opts.Root 之下的所有可存取後代與根目錄自身。
//
// 回傳的錯誤只有三種：根目錄不可用（E_ROOT_UNAVAILABLE，含掃描中途失效）、
// Sink 回報的錯誤，以及 context 取消。個別目錄或條目的失敗會變成事件與覆蓋缺口，
// 不會中斷整個可繼續的掃描。
func Walk(ctx context.Context, opts Options) (Result, error) {
	normalized, err := opts.normalize()
	if err != nil {
		return Result{}, err
	}
	root, err := ResolveRoot(normalized.Root)
	if err != nil {
		return Result{}, err
	}
	walkCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workers := effectiveWorkers(normalized.Workers)
	depth := effectiveQueueDepth(normalized.QueueDepth, workers)
	w := &walker{
		opts:       normalized,
		root:       root,
		batchSize:  effectiveBatchSize(normalized.BatchSize),
		messages:   make(chan message, depth),
		queueLimit: depth,
		cancel:     cancel,
	}
	w.queueCond = sync.NewCond(&w.queueMu)
	w.excluder = NewExcluder(normalized.Excludes, normalized.CasePolicy, normalized.SelfFiles, normalized.SelfDirs)
	if volume, ok := fsmeta.VolumeOf(root.Info); ok {
		w.rootVolume = volume
	}

	var workersWG sync.WaitGroup
	for i := 0; i < workers; i++ {
		workersWG.Add(1)
		go func() {
			defer workersWG.Done()
			w.work(walkCtx)
		}()
	}

	// 取消時喚醒所有在佇列上等待的 worker：條件變數本身不回應 context。
	go func() {
		<-walkCtx.Done()
		w.queueMu.Lock()
		w.queueCond.Broadcast()
		w.queueMu.Unlock()
	}()
	w.push(walkCtx, task{relPath: rootRelPath, absPath: root.AbsPath})
	go func() {
		workersWG.Wait()
		close(w.messages)
	}()

	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		w.collect(walkCtx)
	}()
	<-collectorDone
	cancel()

	result := Result{
		Root:           root,
		Entries:        w.entryCount.Load(),
		DirsEnumerated: w.dirOK.Load(),
		DirsPartial:    w.dirPartial.Load(),
		DirsFailed:     w.dirFailed.Load(),
		Excluded:       w.excluded.Load(),
		EntryErrors:    w.entryErrs.Load(),
		Coverage:       w.snapshotCoverage(),
	}
	if w.rootErr != nil {
		return result, w.rootErr
	}
	if w.sinkErr != nil && !errors.Is(w.sinkErr, context.Canceled) {
		return result, w.sinkErr
	}
	if err := ctx.Err(); err != nil {
		result.Interrupted = true
		return result, err
	}
	return result, nil
}

// work 是 worker 主迴圈：取任務、列舉、送出批次，並在最後一個任務結束時收掉佇列。
func (w *walker) work(ctx context.Context) {
	for {
		current, ok := w.take(ctx)
		if !ok {
			return
		}
		w.runTask(ctx, current)
		w.finish()
	}
}

// take 取出下一個任務；沒有任務且掃描結束（或已取消）時回傳 false。
func (w *walker) take(ctx context.Context) (task, bool) {
	w.queueMu.Lock()
	defer w.queueMu.Unlock()
	for {
		if len(w.queue) > 0 {
			next := w.queue[0]
			w.queue = w.queue[1:]
			return next, true
		}
		if w.queueDone || ctx.Err() != nil {
			return task{}, false
		}
		w.queueCond.Wait()
	}
}

// finish 在一個任務結束後遞減待處理計數，並在歸零時結束整個佇列。
//
// 遞減與判斷在同一把鎖內完成：pending 歸零代表沒有任何任務在執行，
// 因此也不可能還有生產者正在入列，不會遺失任務或重複關閉。
func (w *walker) finish() {
	w.queueMu.Lock()
	remaining := w.pending.Add(-1)
	if remaining == 0 {
		w.queueDone = true
		w.queueCond.Broadcast()
	}
	w.queueMu.Unlock()
}

// runTask 列舉一個目錄並送出其中的條目。
func (w *walker) runTask(ctx context.Context, current task) {
	if ctx.Err() != nil {
		return
	}
	isRoot := current.relPath == rootRelPath
	batch := make([]model.Entry, 0, w.batchSize)
	if isRoot {
		batch = append(batch, w.entryFor(current.absPath, current.relPath, w.root.Info))
		w.discover(w.root.Info.Type)
	}

	dirEntries, err := os.ReadDir(current.absPath)
	if err != nil {
		if isRoot {
			// 根目錄失效不是「一個子目錄讀不到」：整次掃描必須終止，
			// 且不得讓呼叫端把結果當成「根目錄變成空目錄」發布。
			w.offer(Event{
				Kind:    EventRootInvalid,
				RelPath: current.relPath,
				AbsPath: current.absPath,
				Code:    model.CodeRootUnavailable,
				Detail:  err.Error(),
			})
			w.rootOnce.Do(func() { w.rootErr = rootUnavailable(current.absPath, err) })
			w.cancel()
			return
		}
		w.recordGap(&w.coverage.FailedDirs, Gap{
			RelPath: current.relPath,
			Code:    model.CodeScanIO,
			Detail:  err.Error(),
		})
		w.dirFailed.Add(1)
		w.countError()
		w.emit(ctx, Event{
			Kind:    EventDirFailed,
			RelPath: current.relPath,
			AbsPath: current.absPath,
			Code:    model.CodeScanIO,
			Detail:  err.Error(),
		})
		w.flush(ctx, batch)
		return
	}

	partial := false
	failureDetail := ""
	for _, dirEntry := range dirEntries {
		if ctx.Err() != nil {
			break
		}
		name := dirEntry.Name()
		childRel := name
		if !isRoot {
			childRel = current.relPath + "/" + name
		}
		childAbs := filepath.Join(current.absPath, name)
		isDirHint := dirEntry.IsDir()

		if reason, excluded := w.exclude(childAbs, childRel, isDirHint); excluded {
			w.excluded.Add(1)
			w.skip(1)
			w.emit(ctx, Event{
				Kind:    EventEntryExcluded,
				RelPath: childRel,
				AbsPath: childAbs,
				Reason:  reason,
			})
			if isDirHint {
				w.recordExcluded(childRel, reason)
			}
			continue
		}

		info, statErr := fsmeta.Collect(childAbs)
		if statErr != nil {
			partial = true
			if failureDetail == "" {
				failureDetail = statErr.Error()
			}
			w.entryErrs.Add(1)
			w.fail(1)
			w.countError()
			w.emit(ctx, Event{
				Kind:    EventEntryFailed,
				RelPath: childRel,
				AbsPath: childAbs,
				Code:    model.CodeScanIO,
				Detail:  statErr.Error(),
			})
			continue
		}

		batch = append(batch, w.entryFor(childAbs, childRel, info))
		w.discover(info.Type)
		if info.Incomplete {
			// 條目有效但擴展欄位缺失（例如無法開啟 Windows 屬性句柄）：
			// 記事件讓上層知道能力受限，但不當成失敗、也不丟掉條目。
			w.emit(ctx, Event{
				Kind:    EventEntryFailed,
				RelPath: childRel,
				AbsPath: childAbs,
				Code:    model.CodeScanIO,
				Detail:  info.IncompleteReason,
			})
		}
		if info.IsDir() && !w.pushDir(ctx, childRel, childAbs, info) {
			continue
		}
		if len(batch) >= w.batchSize {
			w.flush(ctx, batch)
			batch = make([]model.Entry, 0, w.batchSize)
		}
	}
	w.flush(ctx, batch)

	if ctx.Err() != nil {
		return
	}
	if partial {
		w.dirPartial.Add(1)
		w.recordGap(&w.coverage.PartialDirs, Gap{
			RelPath: current.relPath,
			Code:    model.CodeScanIO,
			Detail:  failureDetail,
		})
		w.emit(ctx, Event{
			Kind:    EventDirPartial,
			RelPath: current.relPath,
			AbsPath: current.absPath,
			Count:   int64(len(dirEntries)),
			Code:    model.CodeScanIO,
			Detail:  failureDetail,
		})
		return
	}
	w.dirOK.Add(1)
	w.emit(ctx, Event{
		Kind:    EventDirEnumerated,
		RelPath: current.relPath,
		AbsPath: current.absPath,
		Count:   int64(len(dirEntries)),
	})
}

// pushDir 把子目錄排入佇列，必要時因跨檔案系統而排除。
//
// 回傳 false 表示這個子目錄不會被進入：跨檔案系統的掛載點仍是一筆條目，
// 但它的子樹沒有被看過，因此要記成覆蓋缺口，不能當成刪除依據。
func (w *walker) pushDir(ctx context.Context, relPath, absPath string, info fsmeta.Info) bool {
	if w.opts.OneFileSystem && w.rootVolume != "" {
		if volume, ok := fsmeta.VolumeOf(info); ok && volume != w.rootVolume {
			w.excluded.Add(1)
			w.skip(1)
			w.recordExcluded(relPath, ExcludeReasonCrossDevice)
			w.emit(ctx, Event{
				Kind:    EventEntryExcluded,
				RelPath: relPath,
				AbsPath: absPath,
				Reason:  ExcludeReasonCrossDevice,
			})
			return false
		}
	}
	w.push(ctx, task{relPath: relPath, absPath: absPath})
	return true
}

// exclude 判斷一個子項是否被排除，並回報原因。
//
// 規則本身住在 Excluder，索引層用同一個型別判定舊條目是否因排除規則改變而移出，
// 兩邊因此不可能出現不一致。目錄被排除時不會進入，其後代既不會出現，
// 也不會被判定為刪除。
func (w *walker) exclude(absPath, relPath string, isDir bool) (ExcludeReason, bool) {
	return w.excluder.Match(absPath, relPath, isDir)
}

// entryFor 由中繼資料組出一筆條目：路徑欄位由掃描層負責，其餘由 fsmeta 填寫。
func (w *walker) entryFor(absPath, relPath string, info fsmeta.Info) model.Entry {
	name := baseName(relPath, absPath)
	entry := model.Entry{
		PathKey:      model.PathKey(relPath, w.opts.CasePolicy),
		RelPath:      relPath,
		AbsPath:      absPath,
		Name:         name,
		ParentRel:    parentRel(relPath),
		Status:       model.StatusPresent,
		FirstSeenRun: w.opts.RunID,
		LastSeenRun:  w.opts.RunID,
		LastSeenAt:   model.NewTimestamp(w.now()),
	}
	entry.HashState = model.HashStateNotApplicable
	if info.Type == model.EntryTypeFile {
		entry.HashState = model.HashStateNotComputed
		entry.Ext = extensionOf(name)
	}
	info.Fill(&entry)
	return entry
}

// push 把子目錄排入佇列；佇列已滿時由呼叫端就地把這個任務做完。
//
// 關鍵約束：生產者永遠不能被自己擋住。若在這裡阻塞等待空位，
// 唯一能騰出空位的 worker 正在等的就是它自己，單一 worker 時必定死鎖。
// 原地處理讓佇列保持有界（記憶體不隨目錄數成長），也不會遺失任何子目錄；
// 遞迴深度只受目錄層數限制，與檔案數無關。
func (w *walker) push(ctx context.Context, next task) {
	if ctx.Err() != nil {
		return
	}
	w.queueMu.Lock()
	if w.queueDone {
		// 佇列已判定結束（代表沒有任何任務在執行，也就不該再有人入列）。
		w.queueMu.Unlock()
		return
	}
	if len(w.queue) < w.queueLimit {
		w.queue = append(w.queue, next)
		w.pending.Add(1)
		w.queueCond.Signal()
		w.queueMu.Unlock()
		return
	}
	w.queueMu.Unlock()
	w.pending.Add(1)
	w.runTask(ctx, next)
	w.finish()
}

// flush 把目前的批次送到 collector；空批次直接丟棄。
func (w *walker) flush(ctx context.Context, batch []model.Entry) {
	if len(batch) == 0 {
		return
	}
	w.entryCount.Add(int64(len(batch)))
	select {
	case w.messages <- message{entries: batch}:
	case <-ctx.Done():
	}
}

// emit 送出一筆事件；取消後不再阻塞。
func (w *walker) emit(ctx context.Context, event Event) {
	payload := event
	select {
	case w.messages <- message{event: &payload}:
	case <-ctx.Done():
	}
}

// offer 是取消後仍要盡力送出一筆事件的版本（例如根目錄失效）。
func (w *walker) offer(event Event) {
	payload := event
	select {
	case w.messages <- message{event: &payload}:
	default:
	}
}

// collect 是唯一的消費端：呼叫 OnEvent 與 Sink，並把 Sink 的錯誤存起來。
func (w *walker) collect(ctx context.Context) {
	for payload := range w.messages {
		if payload.event != nil {
			if w.opts.OnEvent != nil {
				w.opts.OnEvent(*payload.event)
			}
			continue
		}
		if len(payload.entries) == 0 {
			continue
		}
		if err := w.opts.Sink(ctx, payload.entries); err != nil {
			w.errOnce.Do(func() { w.sinkErr = err })
			w.cancel()
			return
		}
		if w.opts.Stats != nil {
			w.opts.Stats.AddProcessed(int64(len(payload.entries)))
			w.opts.Stats.AddSucceeded(int64(len(payload.entries)))
		}
	}
}

// recordGap 記錄一段目錄級缺口。
func (w *walker) recordGap(target *[]Gap, gap Gap) {
	w.gapsMu.Lock()
	defer w.gapsMu.Unlock()
	*target = append(*target, gap)
}

// recordExcluded 記錄一段被排除、因此不可作為刪除依據的目錄。
func (w *walker) recordExcluded(relPath string, reason ExcludeReason) {
	w.gapsMu.Lock()
	defer w.gapsMu.Unlock()
	w.coverage.ExcludedDirs = append(w.coverage.ExcludedDirs, Excluded{RelPath: relPath, Reason: reason})
}

// snapshotCoverage 複製目前的覆蓋結果，避免回傳後被改寫。
func (w *walker) snapshotCoverage() Coverage {
	w.gapsMu.Lock()
	defer w.gapsMu.Unlock()
	return Coverage{
		PartialDirs:  append([]Gap(nil), w.coverage.PartialDirs...),
		FailedDirs:   append([]Gap(nil), w.coverage.FailedDirs...),
		ExcludedDirs: append([]Excluded(nil), w.coverage.ExcludedDirs...),
	}
}

// now 回傳目前時間（可注入）。
func (w *walker) now() time.Time {
	if w.opts.Now != nil {
		return w.opts.Now()
	}
	return time.Now()
}

// discover 累加發現計數與型別計數。
func (w *walker) discover(kind model.EntryType) {
	if w.opts.Stats == nil {
		return
	}
	w.opts.Stats.AddDiscovered(1)
	switch kind {
	case model.EntryTypeFile:
		w.opts.Stats.AddFiles(1)
	case model.EntryTypeDir:
		w.opts.Stats.AddDirs(1)
	case model.EntryTypeSymlink:
		w.opts.Stats.AddSymlinks(1)
	default:
		w.opts.Stats.AddOthers(1)
	}
}

// fail 累加失敗條目數。
func (w *walker) fail(n int64) {
	if w.opts.Stats == nil {
		return
	}
	w.opts.Stats.AddFailed(n)
}

// skip 累加跳過數。
func (w *walker) skip(n int64) {
	if w.opts.Stats == nil {
		return
	}
	w.opts.Stats.AddSkipped(n)
}

// countError 累加錯誤事件數。
func (w *walker) countError() {
	if w.opts.Stats == nil {
		return
	}
	w.opts.Stats.AddErrorEvent(1)
}

// normalize 檢查選項；缺必要值時回報內部錯誤。
func (o Options) normalize() (Options, error) {
	if strings.TrimSpace(o.Root) == "" {
		return Options{}, model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "scan root is empty",
		})
	}
	if !o.CasePolicy.Valid() {
		return Options{}, model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "unknown case policy " + string(o.CasePolicy),
		})
	}
	if o.Sink == nil {
		return Options{}, model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "scan sink is nil",
		})
	}
	return o, nil
}

// effectiveWorkers 把請求的並行度限制在 max(1, CPU/2) 之內。
func effectiveWorkers(requested int) int {
	limit := runtime.NumCPU() / 2
	if limit < 1 {
		limit = 1
	}
	if requested < 1 {
		return 1
	}
	if requested > limit {
		return limit
	}
	return requested
}

// effectiveBatchSize 套用批次大小預設值與上限。
func effectiveBatchSize(requested int) int {
	if requested < 1 {
		return defaultBatchSize
	}
	if requested > maxBatchSize {
		return maxBatchSize
	}
	return requested
}

// effectiveQueueDepth 套用有界佇列深度。
func effectiveQueueDepth(requested, workers int) int {
	if requested > 0 {
		return requested
	}
	depth := workers * 2
	if depth < 4 {
		depth = 4
	}
	return depth
}

// buildSelfSets 把自產檔案與目錄轉成折疊後的路徑集合，供精確比對與前綴比對。
func buildSelfSets(files, dirs []string, policy model.CasePolicy) (map[string]struct{}, []string) {
	fileSet := make(map[string]struct{}, len(files))
	for _, file := range files {
		if strings.TrimSpace(file) == "" {
			continue
		}
		fileSet[foldPath(file, policy)] = struct{}{}
	}
	dirList := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		folded := strings.TrimRight(foldPath(dir, policy), "/")
		if folded == "" {
			continue
		}
		dirList = append(dirList, folded)
	}
	return fileSet, dirList
}

// foldPath 把路徑轉成比較形式：統一分隔符、依策略折疊大小寫。
func foldPath(value string, policy model.CasePolicy) string {
	return model.PathKey(filepath.Clean(value), policy)
}

// baseName 取得條目的名稱；根目錄用真實路徑的最後一段。
//
// 不為根目錄發明特殊字串：磁碟機根目錄得到 "C:"，POSIX 根目錄得到 "/"，
// 兩者都是可顯示且穩定的值。
func baseName(relPath, absPath string) string {
	if relPath != rootRelPath {
		return path.Base(relPath)
	}
	trimmed := strings.TrimRight(absPath, pathSeparators)
	if trimmed == "" {
		return absPath
	}
	if index := strings.LastIndexAny(trimmed, pathSeparators); index >= 0 {
		if name := trimmed[index+1:]; name != "" {
			return name
		}
	}
	return trimmed
}

// parentRel 取得父目錄的相對路徑。
//
// 根目錄自身沒有父目錄（空字串）；根目錄的直接子項以 "." 表示父目錄，
// 這樣前綴查詢與目錄對應都不需要特例。
func parentRel(relPath string) string {
	if relPath == rootRelPath {
		return ""
	}
	index := strings.LastIndex(relPath, "/")
	if index < 0 {
		return rootRelPath
	}
	return relPath[:index]
}

// extensionOf 取得含點且小寫的副檔名；沒有副檔名時回 nil。
//
// ".bashrc" 這種以點開頭且只有一個點的名稱不算有副檔名，
// 否則隱藏檔會被誤判成有副檔名。
func extensionOf(name string) *string {
	if name == "" {
		return nil
	}
	if strings.HasPrefix(name, ".") && strings.Count(name, ".") == 1 {
		return nil
	}
	ext := path.Ext(name)
	if ext == "" || ext == "." {
		return nil
	}
	lower := strings.ToLower(ext)
	return &lower
}

// strconvItoa 把整數轉成十進位字串，供事件參數使用。
func strconvItoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	current := value
	if negative {
		current = -current
	}
	var digits [20]byte
	index := len(digits)
	for current > 0 {
		index--
		digits[index] = byte('0' + current%10)
		current /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}
