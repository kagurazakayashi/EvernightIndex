package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔實作暫存與發佈邊界，以及已發佈條目的讀取。
//
// 契約（後續掃描、查詢、去重與臨時副本都必須遵守）：
//  1. 掃描期間的條目變更只寫 entry_stage，不碰 entry；
//  2. 只有 Publish 一個交易會改寫 entry、root_dir.published_generation 與 run 狀態；
//  3. 同一代次只能發佈一次：發佈前比對開始時讀到的已發佈代次，不一致就拒絕；
//  4. 發佈交易失敗時整批回滾，舊代次與舊條目完全不受影響；
//  5. 變化事件與待匯出狀態在同一個發佈交易內提交，CSV 匯出在其後另行完成。

// entryColumn 描述 entry 與 entry_stage 共用的一個資料欄位。
//
// field 同時是寫入參數與掃描目標：
//   - 作為參數時，指標會被 database/sql 解參考（nil 寫成 NULL）；
//   - 作為掃描目標時，可為 NULL 的欄位用指標接住 NULL。
type entryColumn struct {
	name  string
	field func(*model.Entry) any
}

// entryColumns 是條目欄位的唯一定義，順序必須與 schema.go 的 entry 表一致。
var entryColumns = []entryColumn{
	{"path_key", func(e *model.Entry) any { return &e.PathKey }},
	{"rel_path", func(e *model.Entry) any { return &e.RelPath }},
	{"abs_path", func(e *model.Entry) any { return &e.AbsPath }},
	{"name", func(e *model.Entry) any { return &e.Name }},
	{"ext", func(e *model.Entry) any { return &e.Ext }},
	{"parent_rel", func(e *model.Entry) any { return &e.ParentRel }},
	{"entry_type", func(e *model.Entry) any { return &e.Type }},
	{"size_bytes", func(e *model.Entry) any { return &e.Size }},
	{"atime_utc", func(e *model.Entry) any { return &e.Atime }},
	{"mtime_utc", func(e *model.Entry) any { return &e.Mtime }},
	{"btime_utc", func(e *model.Entry) any { return &e.Btime }},
	{"ctime_utc", func(e *model.Entry) any { return &e.Ctime }},
	{"mode", func(e *model.Entry) any { return &e.Mode }},
	{"link_target", func(e *model.Entry) any { return &e.LinkTarget }},
	{"uid", func(e *model.Entry) any { return &e.UID }},
	{"gid", func(e *model.Entry) any { return &e.GID }},
	{"device_id", func(e *model.Entry) any { return &e.DeviceID }},
	{"inode", func(e *model.Entry) any { return &e.Inode }},
	{"nlink", func(e *model.Entry) any { return &e.Nlink }},
	{"win_attrs", func(e *model.Entry) any { return &e.WinAttrs }},
	{"win_volume_id", func(e *model.Entry) any { return &e.WinVolumeID }},
	{"win_file_id", func(e *model.Entry) any { return &e.WinFileID }},
	{"win_reparse_tag", func(e *model.Entry) any { return &e.WinReparseTag }},
	{"bsd_flags", func(e *model.Entry) any { return &e.BSDFlags }},
	{"hash_sha256", func(e *model.Entry) any { return &e.HashSHA256 }},
	{"hash_state", func(e *model.Entry) any { return &e.HashState }},
	{"hash_computed_at_utc", func(e *model.Entry) any { return &e.HashComputedAt }},
	{"hash_size_bytes", func(e *model.Entry) any { return &e.HashSize }},
	{"hash_mtime_utc", func(e *model.Entry) any { return &e.HashMTime }},
	{"status", func(e *model.Entry) any { return &e.Status }},
	{"first_seen_run", func(e *model.Entry) any { return &e.FirstSeenRun }},
	{"last_seen_run", func(e *model.Entry) any { return &e.LastSeenRun }},
	{"last_seen_at_utc", func(e *model.Entry) any { return &e.LastSeenAt }},
	{"error_code", func(e *model.Entry) any { return &e.ErrorCode }},
	{"error_stage", func(e *model.Entry) any { return &e.ErrorStage }},
}

// stageDeleteSQL 記錄一筆待刪除路徑；重複標記同一路徑時保持 delete。
const stageDeleteSQL = `INSERT INTO entry_stage(run_id, path_key, op) VALUES (?, ?, 'delete')
	ON CONFLICT(run_id, path_key) DO UPDATE SET op = 'delete'`

// eventInsertSQL 寫入一筆變化事件。
const eventInsertSQL = `INSERT INTO entry_event(run_id, root_id, path_key, rel_path, event_type, changed_fields,
	before_exists, after_exists, before_json, after_json, created_at_utc)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// entryValueNames 回傳不含 path_key 的欄位名（暫存寫入用）。
func entryValueNames() []string {
	names := make([]string, 0, len(entryColumns)-1)
	for _, column := range entryColumns[1:] {
		names = append(names, column.name)
	}
	return names
}

// stageUpsertSQL 建立暫存 upsert 語句；同一路徑重複暫存時以最後一次為準。
func stageUpsertSQL() string {
	names := entryValueNames()
	holders := make([]string, len(names))
	assignments := make([]string, len(names))
	for i, name := range names {
		holders[i] = "?"
		assignments[i] = name + " = excluded." + name
	}
	return "INSERT INTO entry_stage(run_id, path_key, op, " + strings.Join(names, ", ") + ") " +
		"VALUES (?, ?, 'upsert', " + strings.Join(holders, ", ") + ") " +
		"ON CONFLICT(run_id, path_key) DO UPDATE SET op = 'upsert', " + strings.Join(assignments, ", ")
}

// publishUpsertSQL 在發佈交易內把暫存列搬進已發佈表。
func publishUpsertSQL() string {
	all := make([]string, len(entryColumns))
	for i, column := range entryColumns {
		all[i] = column.name
	}
	assignments := make([]string, 0, len(entryColumns)-1)
	for _, column := range entryColumns[1:] {
		assignments = append(assignments, column.name+" = excluded."+column.name)
	}
	return "INSERT INTO entry(root_id, " + strings.Join(all, ", ") + ") " +
		"SELECT ?, " + strings.Join(all, ", ") + " FROM entry_stage WHERE run_id = ? AND op = 'upsert' " +
		"ON CONFLICT(root_id, path_key) DO UPDATE SET " + strings.Join(assignments, ", ")
}

// StageEntries 暫存一批條目 upsert；此時查詢仍看不到任何變更。
func (s *Store) StageEntries(ctx context.Context, runID string, entries []model.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	if err := s.requireRunningRun(ctx, runID); err != nil {
		return err
	}
	statement := stageUpsertSQL()
	return s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		prepared, err := tx.PrepareContext(ctx, statement)
		if err != nil {
			return databaseError(err)
		}
		defer func() { _ = prepared.Close() }()
		for index := range entries {
			entry := &entries[index]
			if err := validateEntry(entry); err != nil {
				return err
			}
			args := make([]any, 0, len(entryColumns)+1)
			args = append(args, runID, entry.PathKey)
			for _, column := range entryColumns[1:] {
				args = append(args, column.field(entry))
			}
			if _, err := prepared.ExecContext(ctx, args...); err != nil {
				return databaseError(err)
			}
		}
		return nil
	})
}

// StageDeletions 暫存一批待刪除路徑；實際刪除只發生在發佈交易內。
func (s *Store) StageDeletions(ctx context.Context, runID string, pathKeys []string) error {
	if len(pathKeys) == 0 {
		return nil
	}
	if err := s.requireRunningRun(ctx, runID); err != nil {
		return err
	}
	return s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		prepared, err := tx.PrepareContext(ctx, stageDeleteSQL)
		if err != nil {
			return databaseError(err)
		}
		defer func() { _ = prepared.Close() }()
		for _, pathKey := range pathKeys {
			if strings.TrimSpace(pathKey) == "" {
				return internalError("deletion path key is empty")
			}
			if _, err := prepared.ExecContext(ctx, runID, pathKey); err != nil {
				return databaseError(err)
			}
		}
		return nil
	})
}

// PublishRequest 是一次發佈的內容。
type PublishRequest struct {
	// RunID 是本次執行；必須仍在 running 狀態。
	RunID string
	// ExpectedGeneration 是執行開始時讀到的已發佈代次（0 表示尚無）。
	ExpectedGeneration int64
	// Coverage 說明覆蓋程度；partial 代表有明確缺口但仍發佈成功區域。
	Coverage model.Coverage
	// CoverageJSON 是缺口明細（可為空）。
	CoverageJSON string
	// Counts 是本次執行的統計計數。
	Counts model.RunCounts
	// Events 是同一個交易內提交的變化事件。
	Events []model.Event
	// RecordExport 為真時登記待匯出狀態（即使沒有事件，例如只做基線更新）。
	RecordExport bool
	// MessageKey 與 ErrorCode 記錄結束原因；正常完成通常為空。
	MessageKey string
	ErrorCode  string
}

// PublishResult 是一次發佈的結果。
type PublishResult struct {
	// Generation 是本次產生的已發佈代次。
	Generation int64
	// Entries 是發佈後的可見條目總數。
	Entries int64
	// Events 是本次寫入的事件數。
	Events int64
}

// Publish 在單一交易內把暫存結果變成已發佈代次。
//
// 交易的每一步失敗（含事件寫入、代次比對、執行狀態檢查）都會整批回滾，
// 因此呼叫端只會看到「已發佈新代次」或「舊代次完全不變」兩種結果。
func (s *Store) Publish(ctx context.Context, request PublishRequest) (PublishResult, error) {
	if strings.TrimSpace(request.RunID) == "" {
		return PublishResult{}, internalError("publish requires a run id")
	}
	if !request.Coverage.Valid() {
		return PublishResult{}, internalError("unknown coverage state " + string(request.Coverage))
	}
	now := model.NowTimestamp()
	generation := request.ExpectedGeneration + 1
	var result PublishResult
	err := s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		root, err := readRootForPublish(ctx, tx)
		if err != nil {
			return err
		}
		current := int64(0)
		if root.publishedGeneration != nil {
			current = *root.publishedGeneration
		}
		if current != request.ExpectedGeneration {
			return publishConflict(request.ExpectedGeneration, current)
		}
		// 代次比對與寫入合併成一次條件更新：即使有非合作行程，
		// 也不可能在檢查與寫入之間插入另一個發佈者。
		updated, err := tx.ExecContext(ctx, `
			UPDATE root_dir
			SET published_generation = ?, published_run_id = ?, published_at_utc = ?
			WHERE id = 1 AND published_generation IS ?`,
			generation, request.RunID, now.String(), nullableGeneration(request.ExpectedGeneration))
		if err != nil {
			return databaseError(err)
		}
		if affected, err := updated.RowsAffected(); err != nil {
			return databaseError(err)
		} else if affected != 1 {
			return publishConflict(request.ExpectedGeneration, current)
		}

		if err := applyStagedEntries(ctx, tx, root.id, request.RunID); err != nil {
			return err
		}
		entries, err := countPublishedEntries(ctx, tx, root.id)
		if err != nil {
			return err
		}
		events, err := insertEvents(ctx, tx, root.id, request.RunID, request.Events, now)
		if err != nil {
			return err
		}
		if err := finishPublishedRun(ctx, tx, request, generation, entries, now); err != nil {
			return err
		}
		if request.RecordExport || len(request.Events) > 0 {
			if err := recordPendingExport(ctx, tx, request.RunID, int64(len(request.Events)), now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM entry_stage WHERE run_id = ?", request.RunID); err != nil {
			return databaseError(err)
		}
		result = PublishResult{Generation: generation, Entries: entries, Events: events}
		return nil
	})
	if err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

// publishedRoot 是發佈交易中需要的根目錄資訊。
type publishedRoot struct {
	id                  int64
	publishedGeneration *int64
}

// readRootForPublish 讀取發佈所需的根目錄狀態。
func readRootForPublish(ctx context.Context, tx *sql.Tx) (publishedRoot, error) {
	var root publishedRoot
	var generation sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT id, published_generation FROM root_dir WHERE id = 1").Scan(&root.id, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return publishedRoot{}, internalError("publish requires a bound root directory")
	}
	if err != nil {
		return publishedRoot{}, databaseError(err)
	}
	if generation.Valid {
		value := generation.Int64
		root.publishedGeneration = &value
	}
	return root, nil
}

// applyStagedEntries 先套用刪除再套用 upsert，讓同一路徑的替換在同一次發佈內完成。
func applyStagedEntries(ctx context.Context, tx *sql.Tx, rootID int64, runID string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM entry
		WHERE root_id = ? AND path_key IN (
			SELECT path_key FROM entry_stage WHERE run_id = ? AND op = 'delete'
		)`, rootID, runID); err != nil {
		return databaseError(err)
	}
	if _, err := tx.ExecContext(ctx, publishUpsertSQL(), rootID, runID); err != nil {
		return databaseError(err)
	}
	return nil
}

// countPublishedEntries 統計已發佈條目數（不含暫存列）。
func countPublishedEntries(ctx context.Context, tx *sql.Tx, rootID int64) (int64, error) {
	var total int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM entry WHERE root_id = ?", rootID).Scan(&total); err != nil {
		return 0, databaseError(err)
	}
	return total, nil
}

// insertEvents 在同一交易內寫入變化事件。
func insertEvents(ctx context.Context, tx *sql.Tx, rootID int64, runID string, events []model.Event, now model.Timestamp) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}
	prepared, err := tx.PrepareContext(ctx, eventInsertSQL)
	if err != nil {
		return 0, databaseError(err)
	}
	defer func() { _ = prepared.Close() }()
	for index := range events {
		event := &events[index]
		if !event.Type.Valid() {
			return 0, internalError("unknown event type " + string(event.Type))
		}
		if strings.TrimSpace(event.PathKey) == "" {
			return 0, internalError("event path key is empty")
		}
		owner := event.RunID
		if strings.TrimSpace(owner) == "" {
			owner = runID
		}
		if owner != runID {
			return 0, internalError("event run id does not match the published run")
		}
		changed, err := marshalChangedFields(event.ChangedFields)
		if err != nil {
			return 0, err
		}
		root := event.RootID
		if root == 0 {
			root = rootID
		}
		created := event.CreatedAt
		if created == "" {
			created = now
		}
		if _, err := prepared.ExecContext(ctx, owner, root, event.PathKey, event.RelPath, string(event.Type),
			changed, boolToInt(event.BeforeExists), boolToInt(event.AfterExists),
			nullableString(event.BeforeJSON), nullableString(event.AfterJSON), created.String()); err != nil {
			return 0, databaseError(err)
		}
	}
	return int64(len(events)), nil
}

// finishPublishedRun 把執行標記為已發佈並寫入統計與覆蓋狀態。
func finishPublishedRun(ctx context.Context, tx *sql.Tx, request PublishRequest, generation, entries int64, now model.Timestamp) error {
	updated, err := tx.ExecContext(ctx, `
		UPDATE run
		SET status = 'published', generation = ?, finished_at_utc = ?,
		    coverage = ?, coverage_json = ?,
		    discovered = ?, processed = ?, succeeded = ?, failed = ?, error_events = ?,
		    published_entries = ?, message_key = ?, error_code = ?
		WHERE run_id = ? AND status = 'running' AND baseline_generation IS ?`,
		generation, now.String(), string(request.Coverage), nullableText(request.CoverageJSON),
		request.Counts.Discovered, request.Counts.Processed, request.Counts.Succeeded,
		request.Counts.Failed, request.Counts.ErrorEvents, entries,
		nullableText(request.MessageKey), nullableText(request.ErrorCode),
		request.RunID, nullableGeneration(request.ExpectedGeneration))
	if err != nil {
		return databaseError(err)
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return databaseError(err)
	}
	if affected != 1 {
		return model.New(model.CodePublishConflict, "err.publishConflict", model.ExitFatal, map[string]string{
			"expected": strconv.FormatInt(request.ExpectedGeneration, 10),
			"found":    "run " + request.RunID + " is not running at the recorded baseline",
		})
	}
	return nil
}

// recordPendingExport 登記待匯出狀態；事件已提交但 CSV 尚未輸出。
func recordPendingExport(ctx context.Context, tx *sql.Tx, runID string, eventCount int64, now model.Timestamp) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO change_export(run_id, state, event_count, attempts, updated_at_utc)
		VALUES (?, 'pending', ?, 0, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			state = 'pending',
			event_count = excluded.event_count,
			updated_at_utc = excluded.updated_at_utc`, runID, eventCount, now.String())
	if err != nil {
		return databaseError(err)
	}
	return nil
}

// StartRun 建立一次執行，並把同一根目錄下殘留的 running 執行補標為 abandoned。
//
// 必須在持有正式目標寫入鎖時呼叫：鎖保證沒有其他行程正在寫入，
// 因此看到的 running 執行必定是已消失行程留下的殘留。
func (s *Store) StartRun(ctx context.Context, spec RunSpec) (int64, error) {
	if strings.TrimSpace(spec.RunID) == "" {
		return 0, internalError("run id is required")
	}
	if !spec.Mode.Valid() {
		return 0, internalError("unknown run mode " + string(spec.Mode))
	}
	if strings.TrimSpace(spec.ParamsJSON) == "" {
		return 0, internalError("scan parameters are required")
	}
	if spec.RootID <= 0 {
		return 0, internalError("run requires a bound root directory")
	}
	now := model.NowTimestamp()
	var abandoned int64
	err := s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE run SET status = 'abandoned', finished_at_utc = ?, message_key = ?
			WHERE status = 'running'`, now.String(), "report.abandoned")
		if err != nil {
			return databaseError(err)
		}
		if abandoned, err = result.RowsAffected(); err != nil {
			return databaseError(err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO run(run_id, root_id, mode, status, started_at_utc, baseline_generation, coverage, params_json)
			VALUES (?, ?, ?, 'running', ?, ?, 'unknown', ?)`,
			spec.RunID, spec.RootID, string(spec.Mode), now.String(),
			nullableGeneration(spec.BaselineGeneration), spec.ParamsJSON)
		return err
	})
	if err != nil {
		return 0, err
	}
	return abandoned, nil
}

// RunSpec 描述一次即將開始的執行。
type RunSpec struct {
	RunID              string
	RootID             int64
	Mode               model.RunMode
	ParamsJSON         string
	BaselineGeneration int64
}

// FinishSpec 描述一次未發佈的收尾。
type FinishSpec struct {
	RunID        string
	Status       model.RunStatus
	Coverage     model.Coverage
	CoverageJSON string
	Counts       model.RunCounts
	MessageKey   string
	ErrorCode    string
}

// FinishRun 把仍在 running 的執行標記為 failed、interrupted 或 aborted。
//
// 已發佈的執行不受影響：RowsAffected 為 0 時回傳 nil，呼叫端必須自行記住
// 是否已提交，不能因為「取消晚到」就宣稱回滾了已提交的結果。
func (s *Store) FinishRun(ctx context.Context, spec FinishSpec) error {
	if strings.TrimSpace(spec.RunID) == "" {
		return internalError("run id is required")
	}
	if !spec.Status.Valid() || !spec.Status.Finished() || spec.Status == model.RunStatusPublished {
		return internalError("finish requires a non-published terminal status, got " + string(spec.Status))
	}
	if !spec.Coverage.Valid() {
		return internalError("unknown coverage state " + string(spec.Coverage))
	}
	now := model.NowTimestamp()
	return s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE run
			SET status = ?, finished_at_utc = ?, coverage = ?, coverage_json = ?,
			    discovered = ?, processed = ?, succeeded = ?, failed = ?, error_events = ?,
			    message_key = ?, error_code = ?
			WHERE run_id = ? AND status = 'running'`,
			string(spec.Status), now.String(), string(spec.Coverage), nullableText(spec.CoverageJSON),
			spec.Counts.Discovered, spec.Counts.Processed, spec.Counts.Succeeded,
			spec.Counts.Failed, spec.Counts.ErrorEvents,
			nullableText(spec.MessageKey), nullableText(spec.ErrorCode), spec.RunID)
		return err
	})
}

// PublishedEntry 讀取一筆已發佈條目；未找到時 found 為 false。
func (s *Store) PublishedEntry(ctx context.Context, rootID int64, pathKey string) (model.Entry, bool, error) {
	var entry model.Entry
	if err := s.scanEntry(ctx, rootID, pathKey, &entry); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Entry{}, false, nil
		}
		return model.Entry{}, false, err
	}
	return entry, true, nil
}

// scanEntry 以共用的欄位定義掃描一筆條目。
func (s *Store) scanEntry(ctx context.Context, rootID int64, pathKey string, entry *model.Entry) error {
	query := "SELECT " + strings.Join(entryColumnNames(), ", ") + " FROM entry WHERE root_id = ? AND path_key = ?"
	if err := s.db.QueryRowContext(ctx, query, rootID, pathKey).Scan(entryScanDestinations(entry)...); err != nil {
		return databaseError(err)
	}
	return nil
}

// CountPublished 回傳已發佈條目總數。
func (s *Store) CountPublished(ctx context.Context, rootID int64) (int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM entry WHERE root_id = ?", rootID).Scan(&total); err != nil {
		return 0, databaseError(err)
	}
	return total, nil
}

// EntrySummary 是 info 需要的條目統計。
type EntrySummary struct {
	Total             int64
	Files             int64
	Dirs              int64
	Symlinks          int64
	Special           int64
	HashComputed      int64
	HashReused        int64
	HashNotComputed   int64
	HashNotApplicable int64
	HashFailed        int64
	HashDisabled      int64
}

// EntrySummary 統計已發佈條目的類型與摘要覆蓋率。
func (s *Store) EntrySummary(ctx context.Context, rootID int64) (EntrySummary, error) {
	var summary EntrySummary
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*),
		       coalesce(sum(CASE WHEN entry_type = 'file' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN entry_type = 'dir' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN entry_type = 'symlink' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN entry_type = 'special' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN hash_state = 'computed' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN hash_state = 'reused' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN hash_state = 'not_computed' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN hash_state = 'not_applicable' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN hash_state = 'failed' THEN 1 ELSE 0 END), 0),
		       coalesce(sum(CASE WHEN hash_state = 'disabled' THEN 1 ELSE 0 END), 0)
		FROM entry WHERE root_id = ?`, rootID).Scan(
		&summary.Total, &summary.Files, &summary.Dirs, &summary.Symlinks, &summary.Special,
		&summary.HashComputed, &summary.HashReused, &summary.HashNotComputed,
		&summary.HashNotApplicable, &summary.HashFailed, &summary.HashDisabled,
	)
	if err != nil {
		return EntrySummary{}, databaseError(err)
	}
	return summary, nil
}

// Run 讀取一次執行；不存在時 found 為 false。
func (s *Store) Run(ctx context.Context, runID string) (model.Run, bool, error) {
	return s.scanRun(ctx, "WHERE run_id = ?", runID)
}

// LatestRun 讀取最近開始的一次執行。
func (s *Store) LatestRun(ctx context.Context) (model.Run, bool, error) {
	return s.scanRun(ctx, "ORDER BY started_at_utc DESC, run_id DESC LIMIT 1")
}

// scanRun 以固定欄位順序讀取一筆執行。
func (s *Store) scanRun(ctx context.Context, clause string, args ...any) (model.Run, bool, error) {
	var run model.Run
	var mode, status, coverage string
	var finishedAt, paramsJSON sql.NullString
	var baseline, generation, publishedEntries sql.NullInt64
	var coverageJSON, messageKey, errorCode sql.NullString
	query := `SELECT run_id, root_id, mode, status, started_at_utc, finished_at_utc,
		baseline_generation, generation, coverage, coverage_json, params_json,
		discovered, processed, succeeded, failed, error_events,
		published_entries, message_key, error_code FROM run ` + clause
	err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&run.ID, &run.RootID, &mode, &status, &run.StartedAt, &finishedAt,
		&baseline, &generation, &coverage, &coverageJSON, &paramsJSON,
		&run.Counts.Discovered, &run.Counts.Processed, &run.Counts.Succeeded,
		&run.Counts.Failed, &run.Counts.ErrorEvents,
		&publishedEntries, &messageKey, &errorCode,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Run{}, false, nil
	}
	if err != nil {
		return model.Run{}, false, databaseError(err)
	}
	run.Mode = model.RunMode(mode)
	run.Status = model.RunStatus(status)
	run.Coverage = model.Coverage(coverage)
	if finishedAt.Valid {
		stamp := model.Timestamp(finishedAt.String)
		run.FinishedAt = &stamp
	}
	if baseline.Valid {
		value := baseline.Int64
		run.BaselineGeneration = &value
	}
	if generation.Valid {
		value := generation.Int64
		run.Generation = &value
	}
	if publishedEntries.Valid {
		value := publishedEntries.Int64
		run.PublishedEntries = &value
	}
	run.CoverageJSON = stringPointer(coverageJSON)
	run.ParamsJSON = paramsJSON.String
	run.MessageKey = stringPointer(messageKey)
	run.ErrorCode = stringPointer(errorCode)
	return run, true, nil
}

// PendingExports 回傳仍待匯出的變化日誌狀態。
func (s *Store) PendingExports(ctx context.Context) ([]model.PendingExport, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, state, target_path, checksum, event_count, attempts, last_error, updated_at_utc
		FROM change_export WHERE state = 'pending' ORDER BY run_id`)
	if err != nil {
		return nil, databaseError(err)
	}
	defer func() { _ = rows.Close() }()
	var exports []model.PendingExport
	for rows.Next() {
		var export model.PendingExport
		var state string
		var target, checksum, lastError sql.NullString
		if err := rows.Scan(&export.RunID, &state, &target, &checksum, &export.EventCount,
			&export.Attempts, &lastError, &export.UpdatedAt); err != nil {
			return nil, databaseError(err)
		}
		export.State = model.ExportState(state)
		export.TargetPath = stringPointer(target)
		export.Checksum = stringPointer(checksum)
		export.LastError = stringPointer(lastError)
		exports = append(exports, export)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	return exports, nil
}

// requireRunningRun 確認執行仍可寫入暫存區。
func (s *Store) requireRunningRun(ctx context.Context, runID string) error {
	if strings.TrimSpace(runID) == "" {
		return internalError("run id is required")
	}
	var status string
	err := s.db.QueryRowContext(ctx, "SELECT status FROM run WHERE run_id = ?", runID).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return internalError("run " + runID + " does not exist")
	case err != nil:
		return databaseError(err)
	case status != string(model.RunStatusRunning):
		return internalError("run " + runID + " is " + status + ", not running")
	}
	return nil
}

// validateEntry 檢查暫存列是否具備發佈所需的必要欄位。
//
// entry_stage 允許 NULL（刪除列只帶路徑），所以必要欄位在寫入時就驗證，
// 避免把問題延後到發佈交易。
func validateEntry(entry *model.Entry) error {
	required := map[string]string{
		"path_key":       entry.PathKey,
		"rel_path":       entry.RelPath,
		"abs_path":       entry.AbsPath,
		"name":           entry.Name,
		"first_seen_run": entry.FirstSeenRun,
		"last_seen_run":  entry.LastSeenRun,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return internalError("entry field " + name + " is required")
		}
	}
	if entry.LastSeenAt == "" {
		return internalError("entry field last_seen_at_utc is required")
	}
	if !entry.Type.Valid() {
		return internalError("unknown entry type " + string(entry.Type))
	}
	if !entry.HashState.Valid() {
		return internalError("unknown hash state " + string(entry.HashState))
	}
	if !entry.Status.Valid() {
		return internalError("unknown entry status " + string(entry.Status))
	}
	if entry.HashSHA256 != nil && entry.HashState != model.HashStateComputed && entry.HashState != model.HashStateReused {
		return internalError("digest present but hash state is " + string(entry.HashState))
	}
	return nil
}

// marshalChangedFields 把欄位清單轉成 JSON；空清單寫入 NULL。
func marshalChangedFields(fields []string) (any, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, errDatabaseIO(err)
	}
	return string(encoded), nil
}

// boolToInt 把布林轉成 SQLite 的 0/1。
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// nullableText 把空字串轉成 NULL，避免「有欄位但內容空」與「沒有值」混淆。
func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// publishConflict 建立代次不一致的錯誤。
func publishConflict(expected, found int64) error {
	return model.New(model.CodePublishConflict, "err.publishConflict", model.ExitFatal, map[string]string{
		"expected": strconv.FormatInt(expected, 10),
		"found":    strconv.FormatInt(found, 10),
	})
}

// internalError 建立內部錯誤，避免在程式邊界上吞掉呼叫錯誤。
func internalError(detail string) error {
	return model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{"detail": detail})
}
