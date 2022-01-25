package index

import (
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔把變化分類接上事務內的事件介面。
//
// 事件包含 before/after 快照，是後續 CSV 匯出的唯一輸入；它在同一個發佈交易裡
// 與索引一起提交，因此「索引已更新但事件缺失」不會發生。
// 變化日誌關閉時這裡刻意不組事件：需求要求關閉時不長期保存完整歷史，
// 只保留執行摘要裡的變化計數。

// newEvent 建立一筆事件的共通欄位。
func (b *builder) newEvent(pathKey, relPath string, kind model.EventType, fields []string) model.Event {
	return model.Event{
		RunID:         b.target.RunID(),
		RootID:        b.target.RootID(),
		PathKey:       pathKey,
		RelPath:       relPath,
		Type:          kind,
		ChangedFields: fields,
		CreatedAt:     model.NewTimestamp(b.now()),
	}
}

// recordEvent 記錄一次掃描條目的變化。
func (b *builder) recordEvent(current *plan, kind model.EventType, fields []string) error {
	if !b.options.ChangeLog || kind == "" {
		return nil
	}
	event := b.newEvent(current.entry.PathKey, current.entry.RelPath, kind, fields)
	if current.old != nil {
		before, err := model.SnapshotOf(*current.old).JSON()
		if err != nil {
			return internalError("snapshot before: " + err.Error())
		}
		event.BeforeExists = true
		event.BeforeJSON = &before
	}
	after, err := model.SnapshotOf(current.entry).JSON()
	if err != nil {
		return internalError("snapshot after: " + err.Error())
	}
	event.AfterExists = true
	event.AfterJSON = &after
	b.result.Events = append(b.result.Events, event)
	return nil
}

// recordRemovedEvent 記錄一筆確認消失：沒有 after 快照。
func (b *builder) recordRemovedEvent(old model.Entry) error {
	if !b.options.ChangeLog {
		return nil
	}
	event := b.newEvent(old.PathKey, old.RelPath, model.EventRemoved, nil)
	before, err := model.SnapshotOf(old).JSON()
	if err != nil {
		return internalError("snapshot before: " + err.Error())
	}
	event.BeforeExists = true
	event.BeforeJSON = &before
	b.result.Events = append(b.result.Events, event)
	return nil
}

// recordRetiredEvent 記錄一筆因排除規則移出索引：after 是標成 excluded 的條目。
func (b *builder) recordRetiredEvent(old, updated model.Entry) error {
	if !b.options.ChangeLog {
		return nil
	}
	event := b.newEvent(updated.PathKey, updated.RelPath, model.EventExcluded, []string{"status"})
	before, err := model.SnapshotOf(old).JSON()
	if err != nil {
		return internalError("snapshot before: " + err.Error())
	}
	after, err := model.SnapshotOf(updated).JSON()
	if err != nil {
		return internalError("snapshot after: " + err.Error())
	}
	event.BeforeExists = true
	event.BeforeJSON = &before
	event.AfterExists = true
	event.AfterJSON = &after
	b.result.Events = append(b.result.Events, event)
	return nil
}
