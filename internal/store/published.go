package store

import (
	"context"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔提供增量更新需要的已發佈條目讀取介面。
//
// 兩個介面刻意不同形狀：
//   - LookupPublished 以「本次掃描到的路徑」批次取回舊條目，讓索引層可以就地比對；
//   - EachUnseenPublished 以串流方式列出「這次沒再出現」的舊條目，
//     用 SQL 反連接完成集合差集，因此不必把整份索引載進記憶體。
//
// 兩者都只讀已發佈表，不經 entry_stage，也不會改寫任何狀態。

// lookupChunkSize 是單次 IN 查詢的路徑數上限。
//
// 它同時是 SQLite 參數上限的安全邊界：掃描批次上限為 4096，
// 但這裡仍然分塊，讓直接呼叫本介面的路徑不必先確認批次大小。
const lookupChunkSize = 200

// LookupPublished 依 path_key 批次讀取已發佈條目。
//
// 回傳的 map 只包含確實存在的路徑；不存在的路徑不會出現在結果裡，
// 呼叫端必須把「沒有這筆」當成新增，而不是當成讀取失敗。
func (s *Store) LookupPublished(ctx context.Context, rootID int64, pathKeys []string) (map[string]model.Entry, error) {
	found := make(map[string]model.Entry, len(pathKeys))
	if rootID <= 0 {
		return nil, internalError("root id is required")
	}
	if len(pathKeys) == 0 {
		return found, nil
	}
	names := entryColumnNames()
	prefix := "SELECT " + strings.Join(names, ", ") + " FROM entry WHERE root_id = ? AND path_key IN ("
	for start := 0; start < len(pathKeys); start += lookupChunkSize {
		end := start + lookupChunkSize
		if end > len(pathKeys) {
			end = len(pathKeys)
		}
		chunk := pathKeys[start:end]
		holders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		args = append(args, rootID)
		for position, pathKey := range chunk {
			holders[position] = "?"
			args = append(args, pathKey)
		}
		query := prefix + strings.Join(holders, ", ") + ")"
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, databaseError(err)
		}
		for rows.Next() {
			var entry model.Entry
			if err := rows.Scan(entryScanDestinations(&entry)...); err != nil {
				_ = rows.Close()
				return nil, databaseError(err)
			}
			found[entry.PathKey] = entry
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, databaseError(err)
		}
		if err := rows.Close(); err != nil {
			return nil, databaseError(err)
		}
	}
	return found, nil
}

// EachUnseenPublished 依 path_key 順序串流已發佈表中「本次執行沒有暫存」的條目。
//
// 集合差集在 SQL 內完成：entry_stage 已含本次所有掃描到的路徑，
// 因此 NOT EXISTS 的結果就是候選的消失條目。這只是候選，
// 是否真的刪除由呼叫端依覆蓋證據判定。
func (s *Store) EachUnseenPublished(ctx context.Context, rootID int64, runID string, visit func(model.Entry) error) error {
	if rootID <= 0 {
		return internalError("root id is required")
	}
	if strings.TrimSpace(runID) == "" {
		return internalError("run id is required")
	}
	if visit == nil {
		return internalError("visitor is required")
	}
	names := entryColumnNames()
	query := "SELECT " + strings.Join(names, ", ") + " FROM entry WHERE root_id = ? AND NOT EXISTS (" +
		"SELECT 1 FROM entry_stage WHERE entry_stage.run_id = ? AND entry_stage.path_key = entry.path_key" +
		") ORDER BY entry.path_key"
	rows, err := s.db.QueryContext(ctx, query, rootID, runID)
	if err != nil {
		return databaseError(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var entry model.Entry
		if err := rows.Scan(entryScanDestinations(&entry)...); err != nil {
			return databaseError(err)
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return databaseError(err)
	}
	return nil
}

// entryColumnNames 回傳 entry 與 entry_stage 的完整欄位名（順序與 schema 一致）。
func entryColumnNames() []string {
	names := make([]string, len(entryColumns))
	for index, column := range entryColumns {
		names[index] = column.name
	}
	return names
}

// entryScanDestinations 回傳對應 entryColumnNames 的掃描目標。
func entryScanDestinations(entry *model.Entry) []any {
	destinations := make([]any, len(entryColumns))
	for index, column := range entryColumns {
		destinations[index] = column.field(entry)
	}
	return destinations
}
