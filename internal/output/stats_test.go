package output

import (
	"context"
	"sync"
	"testing"
	"time"
)

// 本檔驗證統計的並行正確性、各計數語義分離，以及每秒節拍的輸出頻率與停止。
// 節拍測試只斷言「至少一次」「停止後不再輸出」與寬鬆上限，不依賴毫秒級調度精度。

// TestStatsConcurrentCounting 驗證多個 goroutine 併發累加不丟計數。
func TestStatsConcurrentCounting(t *testing.T) {
	stats := NewStats()
	// 併發度受環境上限約束：max(1, floor(邏輯 CPU/2))，此處固定為 2。
	const workers = 2
	const perWorker = 500
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := 0; index < perWorker; index++ {
				stats.AddProcessed(1)
				stats.AddSucceeded(1)
				stats.AddBytesRead(3)
				stats.AddErrorEvent(1)
			}
		}()
	}
	group.Wait()

	snapshot := stats.Snapshot()
	if snapshot.Processed != workers*perWorker {
		t.Errorf("processed = %d, want %d", snapshot.Processed, workers*perWorker)
	}
	if snapshot.Succeeded != workers*perWorker {
		t.Errorf("succeeded = %d, want %d", snapshot.Succeeded, workers*perWorker)
	}
	if snapshot.BytesRead != workers*perWorker*3 {
		t.Errorf("bytes = %d, want %d", snapshot.BytesRead, workers*perWorker*3)
	}
	if snapshot.Errors != workers*perWorker {
		t.Errorf("errors = %d, want %d", snapshot.Errors, workers*perWorker)
	}
}

// TestStatsKeepsOutcomesSeparate 驗證已發現、已處理、失敗與已發佈分開表達。
func TestStatsKeepsOutcomesSeparate(t *testing.T) {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stats := NewStatsWithClock(started, func() time.Time { return started.Add(1500 * time.Millisecond) })
	stats.AddDiscovered(3)
	stats.AddProcessed(2)
	stats.AddSucceeded(1)
	stats.AddFailed(1)
	stats.AddErrorEvent(2)
	stats.AddHashComputed(1)

	snapshot := stats.Snapshot()
	if snapshot.Discovered != 3 || snapshot.Processed != 2 {
		t.Errorf("discovered/processed = %d/%d, want 3/2", snapshot.Discovered, snapshot.Processed)
	}
	if snapshot.Succeeded != 1 || snapshot.Failed != 1 {
		t.Errorf("succeeded/failed = %d/%d, want 1/1", snapshot.Succeeded, snapshot.Failed)
	}
	if snapshot.Errors != 2 {
		t.Errorf("error events = %d, want 2 (independent of failed entries)", snapshot.Errors)
	}
	if snapshot.Published != 0 {
		t.Errorf("published = %d, want 0 before the commit point", snapshot.Published)
	}
	if snapshot.Elapsed != 1500*time.Millisecond {
		t.Errorf("elapsed = %s, want 1.5s", snapshot.Elapsed)
	}

	stats.SetPublished(2)
	if got := stats.Snapshot().Published; got != 2 {
		t.Errorf("published = %d, want 2", got)
	}
}

// TestPeriodicEmitsAndStops 驗證節拍會輸出、停止後不再輸出。
func TestPeriodicEmitsAndStops(t *testing.T) {
	stats := NewStats()
	var guard sync.Mutex
	emissions := 0
	periodic := NewPeriodic(stats, 20*time.Millisecond, func(Snapshot) {
		guard.Lock()
		emissions++
		guard.Unlock()
	})
	stop := periodic.Start(context.Background())
	time.Sleep(150 * time.Millisecond)
	stop()

	guard.Lock()
	afterStop := emissions
	guard.Unlock()
	if afterStop < 2 {
		t.Fatalf("emissions = %d, want at least 2 within 150ms at a 20ms interval", afterStop)
	}
	if afterStop > 60 {
		t.Fatalf("emissions = %d, unexpectedly many for a 20ms interval", afterStop)
	}

	time.Sleep(80 * time.Millisecond)
	guard.Lock()
	later := emissions
	guard.Unlock()
	if later != afterStop {
		t.Fatalf("periodic output continued after stop: %d -> %d", afterStop, later)
	}
}

// TestPeriodicStopsWithContext 驗證 context 取消同樣會停止節拍。
func TestPeriodicStopsWithContext(t *testing.T) {
	stats := NewStats()
	var guard sync.Mutex
	emissions := 0
	periodic := NewPeriodic(stats, 20*time.Millisecond, func(Snapshot) {
		guard.Lock()
		emissions++
		guard.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	stop := periodic.Start(ctx)
	time.Sleep(60 * time.Millisecond)
	cancel()
	time.Sleep(60 * time.Millisecond)
	guard.Lock()
	frozen := emissions
	guard.Unlock()
	stop()
	time.Sleep(60 * time.Millisecond)
	guard.Lock()
	defer guard.Unlock()
	if emissions != frozen {
		t.Fatalf("periodic output continued after context cancellation: %d -> %d", frozen, emissions)
	}
}
