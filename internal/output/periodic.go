package output

import (
	"context"
	"sync"
	"time"
)

// DefaultInterval 是統計輸出的預設節拍：每秒一次。
const DefaultInterval = time.Second

// Periodic 依固定節拍輸出統計快照。
//
// 契約：
//   - 節拍只用於「進行中」的輸出，不負責最終彙總；任務不足一秒也必須由
//     呼叫端輸出最終統計（見 app 的收尾流程）；
//   - Stop 之後不會再有輸出，因此最終統計不會與最後一次節拍交錯；
//   - 測試不依賴毫秒級精確度：只斷言「至少輸出一次」與「停止後不再輸出」。
type Periodic struct {
	stats    *Stats
	interval time.Duration
	emit     func(Snapshot)
}

// NewPeriodic 建立節拍輸出器；interval <= 0 時使用 DefaultInterval。
func NewPeriodic(stats *Stats, interval time.Duration, emit func(Snapshot)) *Periodic {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Periodic{stats: stats, interval: interval, emit: emit}
}

// Start 啟動背景節拍，回傳冪等的停止函式。
//
// 停止函式會等待背景 goroutine 結束，確保呼叫端在輸出最終彙總前
// 不會再收到節拍輸出。
func (p *Periodic) Start(ctx context.Context) (stop func()) {
	if p == nil || p.emit == nil || p.stats == nil {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				p.emit(p.stats.Snapshot())
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}
