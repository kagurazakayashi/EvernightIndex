package output

import (
	"sync/atomic"
	"time"
)

// 本檔提供執行期間的執行緒安全統計。
//
// 統計刻意把幾個容易混淆的量分開：
//   - Discovered 是已發現；
//   - Processed 是處理完成（不代表成功，也不代表已保存）；
//   - Succeeded / Failed 是條目終態；
//   - Errors 是錯誤事件數，與失敗條目數不同（一個條目可能產生多個錯誤事件）；
//   - Published 只在發佈交易提交成功後才有值，取消或回滾後必須保持 0，
//     因此不能拿 Processed 當成「已保存」對外報告。

// Stats 是一組可同時被多個 goroutine 累加的計數器。
//
// 全部欄位使用原子操作，讀取 Snapshot 時不需鎖，也不會阻擋寫入方。
type Stats struct {
	started time.Time
	now     func() time.Time

	discovered   atomic.Int64
	processed    atomic.Int64
	succeeded    atomic.Int64
	failed       atomic.Int64
	skipped      atomic.Int64
	files        atomic.Int64
	dirs         atomic.Int64
	symlinks     atomic.Int64
	others       atomic.Int64
	added        atomic.Int64
	updated      atomic.Int64
	removed      atomic.Int64
	unchanged    atomic.Int64
	hashComputed atomic.Int64
	hashReused   atomic.Int64
	hashFailed   atomic.Int64
	bytesRead    atomic.Int64
	errors       atomic.Int64
	published    atomic.Int64
}

// NewStats 建立從現在開始計時的統計器。
func NewStats() *Stats { return NewStatsWithClock(time.Now(), time.Now) }

// NewStatsWithClock 建立使用指定時鐘的統計器；供測試固定耗時。
func NewStatsWithClock(started time.Time, now func() time.Time) *Stats {
	if now == nil {
		now = time.Now
	}
	return &Stats{started: started, now: now}
}

// Snapshot 是某一瞬間的統計值。
type Snapshot struct {
	Started      time.Time
	Elapsed      time.Duration
	Discovered   int64
	Processed    int64
	Succeeded    int64
	Failed       int64
	Skipped      int64
	Files        int64
	Dirs         int64
	Symlinks     int64
	Others       int64
	Added        int64
	Updated      int64
	Removed      int64
	Unchanged    int64
	HashComputed int64
	HashReused   int64
	HashFailed   int64
	BytesRead    int64
	Errors       int64
	Published    int64
}

// Snapshot 讀取目前統計值。
func (s *Stats) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	now := s.now()
	return Snapshot{
		Started:      s.started,
		Elapsed:      now.Sub(s.started),
		Discovered:   s.discovered.Load(),
		Processed:    s.processed.Load(),
		Succeeded:    s.succeeded.Load(),
		Failed:       s.failed.Load(),
		Skipped:      s.skipped.Load(),
		Files:        s.files.Load(),
		Dirs:         s.dirs.Load(),
		Symlinks:     s.symlinks.Load(),
		Others:       s.others.Load(),
		Added:        s.added.Load(),
		Updated:      s.updated.Load(),
		Removed:      s.removed.Load(),
		Unchanged:    s.unchanged.Load(),
		HashComputed: s.hashComputed.Load(),
		HashReused:   s.hashReused.Load(),
		HashFailed:   s.hashFailed.Load(),
		BytesRead:    s.bytesRead.Load(),
		Errors:       s.errors.Load(),
		Published:    s.published.Load(),
	}
}

// AddDiscovered 累加已發現數量。
func (s *Stats) AddDiscovered(n int64) { s.add(&s.discovered, n) }

// AddProcessed 累加處理完成數量。
func (s *Stats) AddProcessed(n int64) { s.add(&s.processed, n) }

// AddSucceeded 累加成功條目數。
func (s *Stats) AddSucceeded(n int64) { s.add(&s.succeeded, n) }

// AddFailed 累加失敗條目數。
func (s *Stats) AddFailed(n int64) { s.add(&s.failed, n) }

// AddSkipped 累加跳過條目數。
func (s *Stats) AddSkipped(n int64) { s.add(&s.skipped, n) }

// AddFiles 累加普通檔案數。
func (s *Stats) AddFiles(n int64) { s.add(&s.files, n) }

// AddDirs 累加目錄數。
func (s *Stats) AddDirs(n int64) { s.add(&s.dirs, n) }

// AddSymlinks 累加連結數。
func (s *Stats) AddSymlinks(n int64) { s.add(&s.symlinks, n) }

// AddOthers 累加特殊檔案數。
func (s *Stats) AddOthers(n int64) { s.add(&s.others, n) }

// AddAdded 累加新增數。
func (s *Stats) AddAdded(n int64) { s.add(&s.added, n) }

// AddUpdated 累加更新數。
func (s *Stats) AddUpdated(n int64) { s.add(&s.updated, n) }

// AddRemoved 累加移除數。
func (s *Stats) AddRemoved(n int64) { s.add(&s.removed, n) }

// AddUnchanged 累加未變化數。
func (s *Stats) AddUnchanged(n int64) { s.add(&s.unchanged, n) }

// AddHashComputed 累加實際計算的摘要數。
func (s *Stats) AddHashComputed(n int64) { s.add(&s.hashComputed, n) }

// AddHashReused 累加複用的摘要數。
func (s *Stats) AddHashReused(n int64) { s.add(&s.hashReused, n) }

// AddHashFailed 累加失敗的摘要數。
func (s *Stats) AddHashFailed(n int64) { s.add(&s.hashFailed, n) }

// AddBytesRead 累加讀取位元組數。
func (s *Stats) AddBytesRead(n int64) { s.add(&s.bytesRead, n) }

// AddErrorEvent 累加錯誤事件數。
func (s *Stats) AddErrorEvent(n int64) { s.add(&s.errors, n) }

// SetPublished 記錄已發佈條目數；只有發佈成功才可呼叫。
func (s *Stats) SetPublished(n int64) {
	if s == nil {
		return
	}
	s.published.Store(n)
}

// Counts 把統計轉成執行記錄用的計數。
func (s Snapshot) Counts() (discovered, processed, succeeded, failed, errorEvents int64) {
	return s.Discovered, s.Processed, s.Succeeded, s.Failed, s.Errors
}

// add 是共用的累加實作；nil 指標時靜默忽略，讓呼叫端不必到處判空。
func (s *Stats) add(counter *atomic.Int64, n int64) {
	if s == nil || n == 0 {
		return
	}
	counter.Add(n)
}
