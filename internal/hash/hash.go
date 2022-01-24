// Package hash 以串流方式計算檔案主資料內容的 SHA256。
//
// 契約：
//  1. 只讀普通檔案的主資料內容；目錄、連結與特殊檔案由呼叫端跳過。
//  2. 讀取前後都檢查同一個已開啟控制代碼的身分、大小與修改時間，並與掃描時
//     觀測到的值比對；不一致時有限重試，重試後仍不一致就記為失敗，
//     不留下部分計算結果，也不保留失敗前算出的摘要。
//  3. 取消會中止讀取；已完成的結果仍然有效，未完成的不假裝成功。
//  4. worker 數有上限，緩衝區大小固定且由 worker 各自持有，記憶體不隨檔案大小成長。
//
// 這是盡力而為的穩定性檢查：單一控制代碼讀取加上掃描值比對，可以發現
// 「掃描後路徑被替換」與「讀取期間內容被改寫」這類常見情形，
// 但排除不了所有並行修改（例如繞過檔案系統介面的寫入、變更後又還原時間戳）。
package hash

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
)

const (
	// defaultBufferSize 是一次讀取的緩衝區大小：夠大以攤平系統呼叫成本，
	// 又不至於在 worker 數乘上緩衝區時佔用過多記憶體。
	defaultBufferSize = 128 << 10
	// maxBufferSize 是允許指定的緩衝區上限，避免呼叫端誤用超大緩衝區。
	maxBufferSize = 1 << 20
	// errorStageHash 是摘要階段寫入條目 error_stage 的固定值。
	errorStageHash = "hash"
)

// Counter 記錄實際開檔次數與讀取位元組數。
//
// 它存在的理由是可驗證性：關閉 SHA256 時開檔次數必須是 0，
// 這比「程式碼裡沒有呼叫讀取」這種說法更難作假。
type Counter struct {
	opens atomic.Int64
	bytes atomic.Int64
}

// Opens 回傳成功開啟檔案內容的次數。
func (c *Counter) Opens() int64 {
	if c == nil {
		return 0
	}
	return c.opens.Load()
}

// Bytes 回傳實際讀入的位元組數。
func (c *Counter) Bytes() int64 {
	if c == nil {
		return 0
	}
	return c.bytes.Load()
}

// Request 是一次摘要請求，帶上掃描時觀測到的身分與時間。
type Request struct {
	// AbsPath 是掃描時觀測到的絕對路徑。
	AbsPath string
	// Identity 是掃描時的身分（卷 + 檔案標識）；空字串表示平台未提供。
	Identity string
	// Size 是掃描時的邏輯大小；nil 表示不可得。
	Size *int64
	// Mtime 是掃描時的修改時間；nil 表示不可得。
	Mtime *model.Timestamp
}

// Result 是一次摘要的結果。
type Result struct {
	// Path 是請求的絕對路徑。
	Path string
	// Digest 是小寫十六進位摘要；失敗時為空字串，絕不保留部分結果。
	Digest string
	// State 是 computed 或 failed。
	State model.HashState
	// ReadBytes 是最後一次嘗試讀入的位元組數。
	ReadBytes int64
	// Attempts 是實際嘗試次數。
	Attempts int
	// Size 與 Mtime 是讀取時由控制代碼觀測到的值。
	Size  int64
	Mtime model.Timestamp
	// Identity 是讀取時由控制代碼取得的身分；空字串表示平台未提供。
	Identity string
	// Code 是失敗時的穩定錯誤碼。
	Code model.ErrorCode
	// Detail 是失敗的技術細節（不翻譯）。
	Detail string
}

// Computed 表示這次摘要可用。
func (r Result) Computed() bool { return r.State == model.HashStateComputed }

// Options 是摘要模組的設定。
type Options struct {
	// Workers 是同時讀取的檔案數；<1 視為 1，上限為 max(1, floor(邏輯 CPU 數/2))。
	Workers int
	// ReadRetries 是偵測到不穩定後的重試次數，允許 0。
	ReadRetries int
	// BufferSize 是單次讀取緩衝區；0 表示預設值。
	BufferSize int
	// Stats 可為 nil；不為 nil 時累加讀取位元組、摘要成功與失敗數。
	Stats *output.Stats
	// Counter 可為 nil；不為 nil 時累加開檔次數與讀取位元組。
	Counter *Counter
	// AfterRead 僅供測試注入：在讀取完成、穩定性檢查之前呼叫，
	// 用來製造確定性的「讀取期間變更」情境；正式路徑為 nil。
	AfterRead func()
}

// Hasher 是一組可重複使用的摘要 worker 設定。
type Hasher struct {
	workers    int
	retries    int
	bufferSize int
	stats      *output.Stats
	counter    *Counter
	afterRead  func()
}

// New 建立摘要器；設定值在此一次固化，之後不再讀取外部狀態。
func New(opts Options) *Hasher {
	bufferSize := opts.BufferSize
	if bufferSize <= 0 {
		bufferSize = defaultBufferSize
	}
	if bufferSize > maxBufferSize {
		bufferSize = maxBufferSize
	}
	retries := opts.ReadRetries
	if retries < 0 {
		retries = 0
	}
	return &Hasher{
		workers:    EffectiveWorkers(opts.Workers),
		retries:    retries,
		bufferSize: bufferSize,
		stats:      opts.Stats,
		counter:    opts.Counter,
		afterRead:  opts.AfterRead,
	}
}

// Workers 回傳實際生效的 worker 數。
func (h *Hasher) Workers() int {
	if h == nil {
		return 0
	}
	return h.workers
}

// EffectiveWorkers 回傳受控的 worker 上限：max(1, floor(邏輯 CPU 數/2))。
//
// requested < 1 代表「預設」，預設哈希 worker 為 1；超過上限一律夾到上限，
// 不因為使用者給了大數字就開出無界並行。
func EffectiveWorkers(requested int) int {
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

// Compute 併發計算一批請求，結果順序與請求一致。
//
// 回傳的 error 只表示整體取消或呼叫端錯誤；個別檔案的失敗記在各自 Result 裡。
// 記憶體上限由「請求批次大小 + worker 數 × 緩衝區」決定，不隨檔案大小成長。
func (h *Hasher) Compute(ctx context.Context, requests []Request) ([]Result, error) {
	results := make([]Result, len(requests))
	if len(requests) == 0 {
		return results, nil
	}
	if err := ctx.Err(); err != nil {
		return results, err
	}
	workers := h.workers
	if workers > len(requests) {
		workers = len(requests)
	}
	indexes := make(chan int)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			buffer := make([]byte, h.bufferSize)
			for index := range indexes {
				results[index] = h.hashOne(ctx, requests[index], buffer)
			}
		}()
	}
	var feedErr error
feed:
	for index := range requests {
		select {
		case indexes <- index:
		case <-ctx.Done():
			feedErr = ctx.Err()
			break feed
		}
	}
	close(indexes)
	wait.Wait()
	if feedErr != nil {
		return results, feedErr
	}
	return results, nil
}

// hashOne 在有限次數內嘗試取得穩定摘要。
func (h *Hasher) hashOne(ctx context.Context, request Request, buffer []byte) Result {
	attempts := h.retries + 1
	result := Result{Path: request.AbsPath, State: model.HashStateFailed}
	for attempt := 1; attempt <= attempts; attempt++ {
		outcome, retry := h.readOnce(ctx, request, buffer)
		outcome.Attempts = attempt
		result = outcome
		if outcome.Computed() || !retry || ctx.Err() != nil {
			break
		}
	}
	if result.Computed() {
		if h.stats != nil {
			h.stats.AddHashComputed(1)
		}
		return result
	}
	if h.stats != nil && result.Code != model.CodeInterrupted {
		h.stats.AddHashFailed(1)
	}
	if result.Code == "" {
		result.Code = model.CodeHashRead
	}
	return result
}

// readOnce 讀取一次檔案內容並檢查穩定性。
//
// retry 為真表示這是「可能還在變動」的失敗，值得再試一次；
// 權限不足、檔案消失這類失敗重試也不會成功，直接回報。
func (h *Hasher) readOnce(ctx context.Context, request Request, buffer []byte) (Result, bool) {
	result := Result{Path: request.AbsPath, State: model.HashStateFailed}

	// 先確認路徑本身仍是普通檔案：掃描與讀取之間可能被換成目錄或連結。
	linkInfo, err := os.Lstat(request.AbsPath)
	if err != nil {
		result.fail(model.CodeHashRead, err.Error())
		return result, false
	}
	if !linkInfo.Mode().IsRegular() {
		result.fail(model.CodeHashRead, "path is no longer a regular file")
		return result, false
	}
	file, err := os.Open(request.AbsPath)
	if err != nil {
		result.fail(model.CodeHashRead, err.Error())
		return result, false
	}
	defer func() { _ = file.Close() }()
	if h.counter != nil {
		h.counter.opens.Add(1)
	}
	before, err := file.Stat()
	if err != nil {
		result.fail(model.CodeHashRead, err.Error())
		return result, false
	}
	identity, hasIdentity := fileIdentity(file)
	if request.Identity != "" && hasIdentity && identity != request.Identity {
		// 掃描之後路徑指向別的物件：這次讀到的內容與已記錄的元資料不同一個檔案。
		result.fail(model.CodeHashUnstable, "identity changed since scan")
		return result, true
	}
	if request.Size != nil && *request.Size != before.Size() {
		result.fail(model.CodeHashUnstable, "size changed since scan")
		return result, true
	}
	observedMtime := model.NewTimestamp(before.ModTime())
	if request.Mtime != nil && *request.Mtime != observedMtime {
		result.fail(model.CodeHashUnstable, "modification time changed since scan")
		return result, true
	}

	digest := sha256.New()
	read := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			result.fail(model.CodeInterrupted, err.Error())
			return result, false
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			_, _ = digest.Write(buffer[:n])
			read += int64(n)
			if h.stats != nil {
				h.stats.AddBytesRead(int64(n))
			}
			if h.counter != nil {
				h.counter.bytes.Add(int64(n))
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			result.fail(model.CodeHashRead, readErr.Error())
			return result, false
		}
	}
	if h.afterRead != nil {
		h.afterRead()
	}
	after, err := file.Stat()
	if err != nil {
		result.fail(model.CodeHashRead, err.Error())
		return result, false
	}
	if after.Size() != before.Size() {
		result.fail(model.CodeHashUnstable, "size changed while reading")
		return result, true
	}
	if !after.ModTime().Equal(before.ModTime()) {
		result.fail(model.CodeHashUnstable, "modification time changed while reading")
		return result, true
	}
	if identityAfter, ok := fileIdentity(file); ok && hasIdentity && identityAfter != identity {
		result.fail(model.CodeHashUnstable, "identity changed while reading")
		return result, true
	}
	// 路徑是否仍指向同一個物件：同一個控制代碼讀到的內容仍然有效，
	// 但若路徑已被替換，把摘要記在這個路徑上會誤導後續比較。
	if current, ok := pathIdentity(request.AbsPath); ok && hasIdentity && current != identity {
		result.fail(model.CodeHashUnstable, "path was replaced while reading")
		return result, true
	}

	result.Digest = hex.EncodeToString(digest.Sum(nil))
	result.State = model.HashStateComputed
	result.ReadBytes = read
	result.Size = before.Size()
	result.Mtime = observedMtime
	result.Identity = identity
	result.Code = ""
	result.Detail = ""
	return result, false
}

// fail 記錄失敗原因；摘要一律清空，避免留下部分結果。
func (r *Result) fail(code model.ErrorCode, detail string) {
	r.State = model.HashStateFailed
	r.Digest = ""
	r.Code = code
	r.Detail = detail
}

// IdentityOfEntry 由條目的平台欄位組出與 fileIdentity 相同的字串。
//
// 兩邊格式必須一致，否則「掃描時的身分」與「讀取控制代碼的身分」永遠不相等。
// 平台未提供標識時回傳 false，呼叫端不得改成「看起來一樣就算相同」。
func IdentityOfEntry(entry model.Entry) (string, bool) {
	if entry.WinVolumeID != nil && *entry.WinVolumeID != "" &&
		entry.WinFileID != nil && *entry.WinFileID != "" {
		return "win:" + *entry.WinVolumeID + "|" + *entry.WinFileID, true
	}
	if entry.DeviceID != nil && entry.Inode != nil {
		return "dev:" + strconv.FormatInt(*entry.DeviceID, 10) + "|" + strconv.FormatInt(*entry.Inode, 10), true
	}
	return "", false
}
