package app

import (
	"context"
	"strconv"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/i18n"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
)

// reporter 是統計與操作事件的統一輸出層。
//
// 所有進度、最終統計與 -v 逐條記錄都從這裡寫入標準錯誤；
// --quiet 由 Console 抑制一般進度與最終統計，但錯誤訊息不受影響，
// --progress=false 只關閉每秒節拍，仍保留最終統計。
type reporter struct {
	console  *output.Console
	catalog  *i18n.Catalog
	lang     i18n.Lang
	stats    *output.Stats
	periodic *output.Periodic
	stop     func()
}

// newReporter 建立輸出層；缺少主控台或統計時退化為靜默。
func newReporter(deps Deps, stats *output.Stats) *reporter {
	rep := &reporter{console: deps.Console, catalog: deps.Catalog, lang: deps.Lang, stats: stats}
	if rep.console == nil || stats == nil || deps.Catalog == nil {
		return rep
	}
	if rep.console.ProgressEnabled() {
		rep.periodic = output.NewPeriodic(stats, output.DefaultInterval, rep.progress)
	}
	return rep
}

// start 啟動每秒節拍輸出。
func (r *reporter) start(ctx context.Context) {
	if r == nil || r.periodic == nil {
		return
	}
	r.stop = r.periodic.Start(ctx)
}

// halt 停止節拍輸出，並等待背景 goroutine 結束。
//
// 最終統計必須在 halt 之後輸出，否則最後一次節拍可能與最終彙總交錯。
func (r *reporter) halt() {
	if r == nil || r.stop == nil {
		return
	}
	r.stop()
	r.stop = nil
}

// progress 輸出一行進行中統計。
func (r *reporter) progress(snapshot output.Snapshot) {
	r.note("stats.progress", statsArgs(snapshot))
}

// summary 輸出最終統計；key 決定已發佈、已取消、失敗或回滾的措辭。
func (r *reporter) summary(snapshot output.Snapshot, key string) {
	r.note(key, statsArgs(snapshot))
}

// operation 輸出 -v 的逐條操作記錄。
func (r *reporter) operation(kind model.OpKind, path string) {
	if r == nil || r.console == nil || !r.console.VerboseEnabled() {
		return
	}
	r.console.Verbosef("%s", r.text(kind.MessageKey(), map[string]string{"path": path}))
}

// note 以目前語言輸出一般訊息（受 --quiet 抑制）。
func (r *reporter) note(key string, args map[string]string) {
	if r == nil || r.console == nil {
		return
	}
	r.console.Note("%s", r.text(key, args))
}

// text 依目前語言渲染訊息；沒有資源時回退訊息鍵本身。
func (r *reporter) text(key string, args map[string]string) string {
	if r == nil || r.catalog == nil {
		return key
	}
	return r.catalog.T(r.lang, key, args)
}

// statsArgs 把統計快照轉成訊息佔位符。
//
// 數值一律用十進位整數字串，不隨語言改變，避免腳本解析失敗。
func statsArgs(snapshot output.Snapshot) map[string]string {
	return map[string]string{
		"discovered":  strconv.FormatInt(snapshot.Discovered, 10),
		"processed":   strconv.FormatInt(snapshot.Processed, 10),
		"succeeded":   strconv.FormatInt(snapshot.Succeeded, 10),
		"failed":      strconv.FormatInt(snapshot.Failed, 10),
		"files":       strconv.FormatInt(snapshot.Files, 10),
		"dirs":        strconv.FormatInt(snapshot.Dirs, 10),
		"hashes":      strconv.FormatInt(snapshot.HashComputed, 10),
		"hash_failed": strconv.FormatInt(snapshot.HashFailed, 10),
		"errors":      strconv.FormatInt(snapshot.Errors, 10),
		"published":   strconv.FormatInt(snapshot.Published, 10),
		"bytes":       strconv.FormatInt(snapshot.BytesRead, 10),
		"elapsed":     formatElapsed(snapshot.Elapsed),
	}
}

// formatElapsed 以毫秒精度輸出耗時；不足一秒的執行也能得到有意義的數值。
func formatElapsed(elapsed time.Duration) string {
	if elapsed <= 0 {
		return "0s"
	}
	return elapsed.Round(time.Millisecond).String()
}
