package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔驗證暫存與發佈邊界：提交可見性、回滾保留舊代次、代次衝突、
// 首次建立未完成、部分完成狀態、事件與待匯出狀態，以及根目錄綁定規則。

// bindTestRoot 綁定一個測試根目錄。
func bindTestRoot(t *testing.T, store *Store, path string) model.Root {
	t.Helper()
	root, err := store.BindRoot(context.Background(), model.RootSpec{
		AbsPath:        path,
		SourcePlatform: "windows",
		CasePolicy:     model.CaseInsensitive,
	})
	if err != nil {
		t.Fatalf("bind root %q: %v", path, err)
	}
	return root
}

// startTestRun 建立一次執行記錄。
func startTestRun(t *testing.T, store *Store, runID string, baseline int64) {
	t.Helper()
	if _, err := store.StartRun(context.Background(), RunSpec{
		RunID:              runID,
		RootID:             1,
		Mode:               model.RunModeCreate,
		ParamsJSON:         "{\"sha256\":true}",
		BaselineGeneration: baseline,
	}); err != nil {
		t.Fatalf("start run %s: %v", runID, err)
	}
}

// sampleEntry 建立一筆合成的普通檔案條目。
func sampleEntry(runID, relPath string, size int64) model.Entry {
	ext := filepath.Ext(relPath)
	stamp := model.NowTimestamp()
	return model.Entry{
		PathKey:      model.PathKey(relPath, model.CaseInsensitive),
		RelPath:      relPath,
		AbsPath:      "C:/root/" + relPath,
		Name:         filepath.Base(relPath),
		Ext:          &ext,
		ParentRel:    "",
		Type:         model.EntryTypeFile,
		Size:         &size,
		Mtime:        &stamp,
		HashState:    model.HashStateNotComputed,
		Status:       model.StatusPresent,
		FirstSeenRun: runID,
		LastSeenRun:  runID,
		LastSeenAt:   stamp,
	}
}

// countRows 統計指定條件的列數。
func countRows(t *testing.T, store *Store, query string, args ...any) int64 {
	t.Helper()
	var total int64
	if err := store.db.QueryRowContext(context.Background(), query, args...).Scan(&total); err != nil {
		t.Fatalf("count rows (%s): %v", query, err)
	}
	return total
}

// TestPublishMakesStagedEntriesVisible 驗證發佈前不可見、發佈後可見且暫存清空。
func TestPublishMakesStagedEntriesVisible(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")

	const runID = "run-publish-1"
	startTestRun(t, store, runID, 0)
	entries := []model.Entry{sampleEntry(runID, "a.txt", 11), sampleEntry(runID, "dir/b.txt", 22)}
	if err := store.StageEntries(ctx, runID, entries); err != nil {
		t.Fatalf("stage entries: %v", err)
	}

	if visible, err := store.CountPublished(ctx, 1); err != nil || visible != 0 {
		t.Fatalf("published entries before commit = %d, err = %v; want 0", visible, err)
	}
	if _, found, err := store.PublishedEntry(ctx, 1, model.PathKey("a.txt", model.CaseInsensitive)); err != nil || found {
		t.Fatalf("staged entry must stay invisible before commit (found=%v, err=%v)", found, err)
	}

	result, err := store.Publish(ctx, PublishRequest{
		RunID:              runID,
		ExpectedGeneration: 0,
		Coverage:           model.CoverageComplete,
		Counts:             model.RunCounts{Discovered: 2, Processed: 2, Succeeded: 2},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if result.Generation != 1 || result.Entries != 2 {
		t.Fatalf("publish result = %+v, want generation 1 with 2 entries", result)
	}
	if generation, err := store.PublishedGeneration(ctx); err != nil || generation != 1 {
		t.Fatalf("published generation = %d, err = %v", generation, err)
	}
	entry, found, err := store.PublishedEntry(ctx, 1, model.PathKey("dir/b.txt", model.CaseInsensitive))
	if err != nil || !found {
		t.Fatalf("published entry not found (found=%v, err=%v)", found, err)
	}
	if entry.Name != "b.txt" || entry.Type != model.EntryTypeFile || entry.Size == nil || *entry.Size != 22 {
		t.Errorf("published entry = %+v", entry)
	}
	if staged := countRows(t, store, "SELECT count(*) FROM entry_stage WHERE run_id = ?", runID); staged != 0 {
		t.Errorf("staged rows = %d, want 0 after publish", staged)
	}
	run, found, err := store.Run(ctx, runID)
	if err != nil || !found {
		t.Fatalf("read run: found=%v err=%v", found, err)
	}
	if run.Status != model.RunStatusPublished || run.Generation == nil || *run.Generation != 1 {
		t.Errorf("run = %+v, want published generation 1", run)
	}
	if run.Coverage != model.CoverageComplete {
		t.Errorf("coverage = %s, want complete", run.Coverage)
	}
	if run.PublishedEntries == nil || *run.PublishedEntries != 2 {
		t.Errorf("published_entries = %v, want 2", run.PublishedEntries)
	}
}

// TestPublishRollbackKeepsPreviousGeneration 驗證發佈交易失敗時舊代次完全不變。
func TestPublishRollbackKeepsPreviousGeneration(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")

	const firstRun = "run-first"
	startTestRun(t, store, firstRun, 0)
	if err := store.StageEntries(ctx, firstRun, []model.Entry{sampleEntry(firstRun, "keep.txt", 5)}); err != nil {
		t.Fatalf("stage first entries: %v", err)
	}
	if _, err := store.Publish(ctx, PublishRequest{RunID: firstRun, ExpectedGeneration: 0, Coverage: model.CoverageComplete}); err != nil {
		t.Fatalf("publish first generation: %v", err)
	}

	const secondRun = "run-second"
	startTestRun(t, store, secondRun, 1)
	if err := store.StageEntries(ctx, secondRun, []model.Entry{sampleEntry(secondRun, "new.txt", 7)}); err != nil {
		t.Fatalf("stage second entries: %v", err)
	}
	if err := store.StageDeletions(ctx, secondRun, []string{model.PathKey("keep.txt", model.CaseInsensitive)}); err != nil {
		t.Fatalf("stage deletion: %v", err)
	}
	// 事件型別不合法會讓發佈交易在寫入事件時失敗。
	_, err := store.Publish(ctx, PublishRequest{
		RunID:              secondRun,
		ExpectedGeneration: 1,
		Coverage:           model.CoverageComplete,
		Events:             []model.Event{{RunID: secondRun, PathKey: "new.txt", RelPath: "new.txt", Type: model.EventType("nonsense")}},
	})
	requireCode(t, err, model.CodeInternal)

	if generation, err := store.PublishedGeneration(ctx); err != nil || generation != 1 {
		t.Fatalf("published generation after rollback = %d, err = %v; want 1", generation, err)
	}
	if total, err := store.CountPublished(ctx, 1); err != nil || total != 1 {
		t.Fatalf("published entries after rollback = %d, err = %v; want 1", total, err)
	}
	if _, found, err := store.PublishedEntry(ctx, 1, model.PathKey("keep.txt", model.CaseInsensitive)); err != nil || !found {
		t.Fatalf("previous entry must stay readable (found=%v, err=%v)", found, err)
	}
	if _, found, _ := store.PublishedEntry(ctx, 1, model.PathKey("new.txt", model.CaseInsensitive)); found {
		t.Error("rolled-back entry must not be visible")
	}
	if retained := countRows(t, store, "SELECT count(*) FROM entry_stage WHERE run_id = ?", secondRun); retained != 2 {
		t.Errorf("staged rows = %d, want 2 kept for the failed run", retained)
	}
	run, _, err := store.Run(ctx, secondRun)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != model.RunStatusRunning {
		t.Errorf("failed publish must leave the run running until the caller records the outcome, got %s", run.Status)
	}
}

// TestPublishRejectsStaleGeneration 驗證代次不一致時拒絕發佈。
func TestPublishRejectsStaleGeneration(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")

	const runID = "run-stale"
	startTestRun(t, store, runID, 0)
	_, err := store.Publish(ctx, PublishRequest{RunID: runID, ExpectedGeneration: 5, Coverage: model.CoverageComplete})
	requireCode(t, err, model.CodePublishConflict)
	if generation, err := store.PublishedGeneration(ctx); err != nil || generation != 0 {
		t.Fatalf("generation = %d, err = %v; a rejected publish must not publish", generation, err)
	}
}

// TestFirstCreationStaysIncomplete 驗證首次建立未發佈時不能當成完整索引。
func TestFirstCreationStaysIncomplete(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")

	const runID = "run-interrupted"
	startTestRun(t, store, runID, 0)
	if err := store.StageEntries(ctx, runID, []model.Entry{sampleEntry(runID, "half.txt", 3)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := store.FinishRun(ctx, FinishSpec{
		RunID:      runID,
		Status:     model.RunStatusInterrupted,
		Coverage:   model.CoverageUnknown,
		MessageKey: "report.interrupted",
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if generation, err := store.PublishedGeneration(ctx); err != nil || generation != 0 {
		t.Fatalf("generation = %d, err = %v; an interrupted first run must not publish", generation, err)
	}
	if total, err := store.CountPublished(ctx, 1); err != nil || total != 0 {
		t.Fatalf("published entries = %d, err = %v; want 0", total, err)
	}
	run, _, err := store.Run(ctx, runID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Status != model.RunStatusInterrupted || run.Generation != nil {
		t.Errorf("run = %+v, want interrupted without a generation", run)
	}
}

// TestPartialCoverageIsPublished 驗證部分完成會發佈並記錄覆蓋缺口。
func TestPartialCoverageIsPublished(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")

	const runID = "run-partial"
	startTestRun(t, store, runID, 0)
	if err := store.StageEntries(ctx, runID, []model.Entry{sampleEntry(runID, "ok.txt", 1)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := store.Publish(ctx, PublishRequest{
		RunID:              runID,
		ExpectedGeneration: 0,
		Coverage:           model.CoveragePartial,
		CoverageJSON:       "{\"gaps\":[\"C:/root/locked\"]}",
		Counts:             model.RunCounts{Discovered: 2, Processed: 1, Succeeded: 1, Failed: 1, ErrorEvents: 1},
	}); err != nil {
		t.Fatalf("publish partial: %v", err)
	}
	run, _, err := store.Run(ctx, runID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.Coverage != model.CoveragePartial {
		t.Errorf("coverage = %s, want partial", run.Coverage)
	}
	if run.CoverageJSON == nil || *run.CoverageJSON == "" {
		t.Error("coverage gaps must be recorded")
	}
	if run.Status != model.RunStatusPublished {
		t.Errorf("status = %s, want published", run.Status)
	}
	if run.Counts.Failed != 1 || run.Counts.ErrorEvents != 1 {
		t.Errorf("counts = %+v, want one failure and one error event", run.Counts)
	}
}

// TestEventsAndPendingExportCommitTogether 驗證事件與待匯出狀態同交易提交。
func TestEventsAndPendingExportCommitTogether(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")

	const runID = "run-events"
	startTestRun(t, store, runID, 0)
	if err := store.StageEntries(ctx, runID, []model.Entry{sampleEntry(runID, "e.txt", 2)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	events := []model.Event{
		{RunID: runID, PathKey: "e.txt", RelPath: "e.txt", Type: model.EventAdded, AfterExists: true, ChangedFields: []string{"size_bytes"}},
		{RunID: runID, PathKey: "gone.txt", RelPath: "gone.txt", Type: model.EventRemoved, BeforeExists: true},
	}
	if _, err := store.Publish(ctx, PublishRequest{
		RunID:              runID,
		ExpectedGeneration: 0,
		Coverage:           model.CoverageComplete,
		Events:             events,
		RecordExport:       true,
	}); err != nil {
		t.Fatalf("publish with events: %v", err)
	}
	if total := countRows(t, store, "SELECT count(*) FROM entry_event WHERE run_id = ?", runID); total != 2 {
		t.Errorf("events = %d, want 2", total)
	}
	exports, err := store.PendingExports(ctx)
	if err != nil {
		t.Fatalf("pending exports: %v", err)
	}
	if len(exports) != 1 || exports[0].RunID != runID || exports[0].EventCount != 2 || exports[0].State != model.ExportPending {
		t.Fatalf("pending exports = %+v", exports)
	}
}

// TestStagingAfterPublishIsRejected 驗證已發佈的執行不能再寫入暫存。
func TestStagingAfterPublishIsRejected(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")

	const runID = "run-closed"
	startTestRun(t, store, runID, 0)
	if _, err := store.Publish(ctx, PublishRequest{RunID: runID, ExpectedGeneration: 0, Coverage: model.CoverageComplete}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := store.StageEntries(ctx, runID, []model.Entry{sampleEntry(runID, "late.txt", 1)}); err == nil {
		t.Fatal("staging into a published run must fail")
	}
}

// TestStartRunMarksLeftoverRuns 驗證殘留的 running 執行會被標記為 abandoned。
func TestStartRunMarksLeftoverRuns(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")

	startTestRun(t, store, "run-old", 0)
	abandoned, err := store.StartRun(ctx, RunSpec{RunID: "run-new", RootID: 1, Mode: model.RunModeUpdate, ParamsJSON: "{}"})
	if err != nil {
		t.Fatalf("start second run: %v", err)
	}
	if abandoned != 1 {
		t.Fatalf("abandoned count = %d, want 1", abandoned)
	}
	old, _, err := store.Run(ctx, "run-old")
	if err != nil {
		t.Fatalf("read old run: %v", err)
	}
	if old.Status != model.RunStatusAbandoned || old.FinishedAt == nil {
		t.Errorf("old run = %+v, want abandoned with a finish time", old)
	}
}

// TestBindRootRules 驗證綁定保存、等價路徑接受與不一致拒絕。
func TestBindRootRules(t *testing.T) {
	store, path := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/Data/Root")

	// 同一路徑的大小寫差異在不區分大小寫的策略下等價，且不覆寫原始綁定。
	root, err := store.BindRoot(ctx, model.RootSpec{AbsPath: "c:/data/root", SourcePlatform: "windows", CasePolicy: model.CaseInsensitive})
	if err != nil {
		t.Fatalf("bind equivalent path: %v", err)
	}
	if root.AbsPath != "C:/Data/Root" {
		t.Errorf("bound path = %q, want the original spelling preserved", root.AbsPath)
	}

	_, err = store.BindRoot(ctx, model.RootSpec{AbsPath: "C:/Other", SourcePlatform: "windows", CasePolicy: model.CaseInsensitive})
	requireCode(t, err, model.CodeRootMismatch)

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(ctx, path, Options{Create: true, BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	reopenedRoot, found, err := reopened.Root(ctx)
	if err != nil || !found {
		t.Fatalf("root after reopen: found=%v err=%v", found, err)
	}
	if reopenedRoot.AbsPath != "C:/Data/Root" {
		t.Errorf("bound path after reopen = %q", reopenedRoot.AbsPath)
	}
}

// TestBindRootRejectsReplacedVolume 驗證卷身份改變時拒絕沿用綁定。
func TestBindRootRejectsReplacedVolume(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	volume := "vol-1"
	if _, err := store.BindRoot(ctx, model.RootSpec{
		AbsPath: "C:/root", SourcePlatform: "windows", CasePolicy: model.CaseInsensitive, VolumeIdentity: &volume,
	}); err != nil {
		t.Fatalf("bind root: %v", err)
	}
	other := "vol-2"
	_, err := store.BindRoot(ctx, model.RootSpec{
		AbsPath: "C:/root", SourcePlatform: "windows", CasePolicy: model.CaseInsensitive, VolumeIdentity: &other,
	})
	requireCode(t, err, model.CodeRootMismatch)
}
