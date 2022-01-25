package store

import (
	"context"
	"testing"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔驗證增量更新需要的兩個讀取介面：依路徑批次取回已發佈條目，
// 以及用 SQL 反連接串流「本次沒有暫存」的舊條目。

// publishSampleRun 發佈一批合成條目，回傳已發佈的根目錄。
func publishSampleRun(t *testing.T, store *Store, runID string, baseline int64, paths ...string) {
	t.Helper()
	startTestRun(t, store, runID, baseline)
	entries := make([]model.Entry, 0, len(paths))
	for _, path := range paths {
		entries = append(entries, sampleEntry(runID, path, 5))
	}
	if err := store.StageEntries(context.Background(), runID, entries); err != nil {
		t.Fatalf("stage entries: %v", err)
	}
	if _, err := store.Publish(context.Background(), PublishRequest{
		RunID:              runID,
		ExpectedGeneration: baseline,
		Coverage:           model.CoverageComplete,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

// TestLookupPublishedReturnsOnlyRequestedPaths 驗證批次查詢只回傳存在且被要求的路徑。
func TestLookupPublishedReturnsOnlyRequestedPaths(t *testing.T) {
	storage, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, storage, "C:/root")
	publishSampleRun(t, storage, "run-first", 0, "a.txt", "b.txt")

	keys := []string{
		model.PathKey("a.txt", model.CaseInsensitive),
		model.PathKey("b.txt", model.CaseInsensitive),
		model.PathKey("absent.txt", model.CaseInsensitive),
	}
	found, err := storage.LookupPublished(ctx, 1, keys)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("lookup returned %d entries, want 2: %v", len(found), found)
	}
	entry, ok := found[model.PathKey("a.txt", model.CaseInsensitive)]
	if !ok || entry.RelPath != "a.txt" || entry.Size == nil || *entry.Size != 5 {
		t.Errorf("a.txt = %+v", entry)
	}
	if _, ok := found[model.PathKey("absent.txt", model.CaseInsensitive)]; ok {
		t.Error("a missing path must not be reported as found")
	}
	empty, err := storage.LookupPublished(ctx, 1, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("empty lookup = %v, err = %v", empty, err)
	}
}

// TestEachUnseenPublishedStreamsDifference 驗證集合差集只包含本次沒暫存的路徑。
func TestEachUnseenPublishedStreamsDifference(t *testing.T) {
	storage, _ := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, storage, "C:/root")
	publishSampleRun(t, storage, "run-first", 0, "a.txt", "b.txt", "c.txt")

	const secondRun = "run-second"
	startTestRun(t, storage, secondRun, 1)
	if err := storage.StageEntries(ctx, secondRun, []model.Entry{sampleEntry(secondRun, "a.txt", 5)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	var visited []string
	if err := storage.EachUnseenPublished(ctx, 1, secondRun, func(entry model.Entry) error {
		visited = append(visited, entry.RelPath)
		return nil
	}); err != nil {
		t.Fatalf("unseen: %v", err)
	}
	if len(visited) != 2 || visited[0] != "b.txt" || visited[1] != "c.txt" {
		t.Errorf("unseen paths = %v, want [b.txt c.txt] in path order", visited)
	}

	// 換另一個執行 ID 時，前一輪的暫存不算數：所有已發佈路徑都應該出現。
	startTestRun(t, storage, "run-third", 1)
	total := 0
	if err := storage.EachUnseenPublished(ctx, 1, "run-third", func(model.Entry) error {
		total++
		return nil
	}); err != nil {
		t.Fatalf("unseen: %v", err)
	}
	if total != 3 {
		t.Errorf("unseen for an empty staging area = %d, want 3", total)
	}
}

// TestEachUnseenPublishedRequiresRun 驗證缺少執行 ID 時直接拒絕，而不是回傳整份索引。
func TestEachUnseenPublishedRequiresRun(t *testing.T) {
	storage, _ := newStore(t)
	requireCode(t, storage.EachUnseenPublished(context.Background(), 1, "  ", func(model.Entry) error {
		t.Fatal("the visitor must not be called")
		return nil
	}), model.CodeInternal)
}
