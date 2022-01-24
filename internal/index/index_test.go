package index

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
)

// 本檔以 t.TempDir() 內的合成目錄驗證建索引層：條目轉換、摘要決策、
// 摘要失敗的覆蓋缺口、自產檔案排除、取消與批次有界。

// emptyDigest 是空檔案的標準 SHA256。
const emptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// helloDigest 是內容 "hello" 的標準 SHA256。
const helloDigest = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

// fakeTarget 收集暫存批次，並可注入暫存時的行為。
type fakeTarget struct {
	batches   [][]model.Entry
	stageHook func(ctx context.Context, batch []model.Entry) error
}

func (f *fakeTarget) RunID() string { return "run-test" }
func (f *fakeTarget) RootID() int64 { return 1 }

func (f *fakeTarget) Stage(ctx context.Context, entries []model.Entry) error {
	copied := append([]model.Entry(nil), entries...)
	f.batches = append(f.batches, copied)
	if f.stageHook != nil {
		return f.stageHook(ctx, copied)
	}
	return nil
}

// staged 回傳所有暫存條目（依相對路徑索引）。
func (f *fakeTarget) staged() map[string]model.Entry {
	collected := map[string]model.Entry{}
	for _, batch := range f.batches {
		for _, entry := range batch {
			collected[entry.RelPath] = entry
		}
	}
	return collected
}

// newTree 建立語料：一般檔案、空檔案、空目錄、巢狀目錄與隱藏檔案。
func newTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nested", "deep"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	files := map[string]string{
		"hello.txt":           "hello",
		"zero.txt":            "",
		"nested/deep/data.md": "abcdefghij",
		".hidden":             "h",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// TestBuildStoresMetadataAndHashes 驗證條目欄位、摘要狀態與可觀察讀取計數。
func TestBuildStoresMetadataAndHashes(t *testing.T) {
	root := newTree(t)
	target := &fakeTarget{}
	stats := output.NewStats()
	result, err := Build(context.Background(), target, Options{
		Root:        root,
		CasePolicy:  model.CaseSensitive,
		SHA256:      true,
		Stats:       stats,
		ReadRetries: 1,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if result.Coverage != model.CoverageComplete {
		t.Errorf("coverage = %s, want complete (%s)", result.Coverage, result.CoverageJSON)
	}
	staged := target.staged()
	// 四個目錄（根目錄、空目錄、兩層巢狀目錄）加上四個檔案。
	if len(staged) != 8 {
		t.Fatalf("staged %d entries, want 8: %v", len(staged), keysOf(staged))
	}
	for relPath, want := range map[string]string{
		"hello.txt":           helloDigest,
		"zero.txt":            emptyDigest,
		"nested/deep/data.md": "72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0",
	} {
		entry := staged[relPath]
		if entry.HashState != model.HashStateComputed {
			t.Errorf("%s: hash state = %s, want computed", relPath, entry.HashState)
			continue
		}
		if entry.HashSHA256 == nil || *entry.HashSHA256 != want {
			t.Errorf("%s: digest = %v, want %s", relPath, entry.HashSHA256, want)
		}
		if entry.HashComputedAt == nil || entry.HashSize == nil || entry.HashMTime == nil {
			t.Errorf("%s: hash metadata is incomplete", relPath)
		}
	}
	for _, dirPath := range []string{".", "empty", "nested", "nested/deep"} {
		entry, ok := staged[dirPath]
		if !ok {
			t.Fatalf("missing directory entry %q", dirPath)
		}
		if entry.Type != model.EntryTypeDir || entry.HashState != model.HashStateNotApplicable {
			t.Errorf("%s: type = %s hash state = %s", dirPath, entry.Type, entry.HashState)
		}
		if entry.Size != nil {
			t.Errorf("%s: a directory must not carry a size", dirPath)
		}
	}
	for relPath, entry := range staged {
		if entry.Status != model.StatusPresent || entry.FirstSeenRun != "run-test" ||
			entry.LastSeenRun != "run-test" || entry.LastSeenAt == "" {
			t.Errorf("%s: run metadata is incomplete: %+v", relPath, entry)
		}
	}
	if result.ContentOpens != 4 {
		t.Errorf("content opens = %d, want 4", result.ContentOpens)
	}
	if result.BytesRead != 16 {
		t.Errorf("bytes read = %d, want 16", result.BytesRead)
	}
	if got := stats.Snapshot(); got.HashComputed != 4 || got.BytesRead != 16 {
		t.Errorf("stats = hashed %d, bytes %d", got.HashComputed, got.BytesRead)
	}
}

// TestBuildSkipsContentWhenSHA256Disabled 驗證關閉 SHA256 時完全不讀內容。
func TestBuildSkipsContentWhenSHA256Disabled(t *testing.T) {
	root := newTree(t)
	target := &fakeTarget{}
	stats := output.NewStats()
	result, err := Build(context.Background(), target, Options{
		Root:       root,
		CasePolicy: model.CaseSensitive,
		SHA256:     false,
		Stats:      stats,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if result.ContentOpens != 0 || result.BytesRead != 0 || result.HashRequests != 0 {
		t.Errorf("content access happened without SHA256: opens %d, bytes %d, requests %d",
			result.ContentOpens, result.BytesRead, result.HashRequests)
	}
	if got := stats.Snapshot(); got.BytesRead != 0 || got.HashComputed != 0 || got.HashFailed != 0 {
		t.Errorf("stats recorded content work: bytes %d, hashed %d, failed %d",
			got.BytesRead, got.HashComputed, got.HashFailed)
	}
	for relPath, entry := range target.staged() {
		switch entry.Type {
		case model.EntryTypeFile:
			if entry.HashState != model.HashStateDisabled {
				t.Errorf("%s: hash state = %s, want disabled", relPath, entry.HashState)
			}
			if entry.HashSHA256 != nil || entry.HashComputedAt != nil {
				t.Errorf("%s: disabled hashing must not leave a digest", relPath)
			}
		default:
			if entry.HashState != model.HashStateNotApplicable {
				t.Errorf("%s: hash state = %s, want not_applicable", relPath, entry.HashState)
			}
		}
	}
}

// TestBuildRejectsDeepWithoutSHA256 驗證非法組合在掃描前被拒絕。
func TestBuildRejectsDeepWithoutSHA256(t *testing.T) {
	target := &fakeTarget{}
	_, err := Build(context.Background(), target, Options{
		Root:       t.TempDir(),
		CasePolicy: model.CaseSensitive,
		Deep:       true,
	})
	if err == nil {
		t.Fatal("deep without sha256 must fail")
	}
	appErr, ok := err.(*model.Error)
	if !ok || appErr.Code != model.CodeFlagConflict || appErr.ExitCode() != model.ExitUsage {
		t.Fatalf("error = %v, want %s with exit code 2", err, model.CodeFlagConflict)
	}
	if len(target.batches) != 0 {
		t.Errorf("nothing may be staged before the conflict is reported")
	}
}

// TestBuildRecordsHashFailureAsGap 驗證摘要失敗不留摘要，並形成覆蓋缺口。
func TestBuildRecordsHashFailureAsGap(t *testing.T) {
	root := newTree(t)
	target := &fakeTarget{}
	stats := output.NewStats()
	result, err := Build(context.Background(), target, Options{
		Root:          root,
		CasePolicy:    model.CaseSensitive,
		SHA256:        true,
		Stats:         stats,
		ReadRetries:   0,
		HashAfterRead: appendTo(filepath.Join(root, "hello.txt")),
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if result.HashFailed != 1 {
		t.Fatalf("hash failures = %d, want 1", result.HashFailed)
	}
	if result.Coverage != model.CoveragePartial {
		t.Errorf("coverage = %s, want partial", result.Coverage)
	}
	entry := target.staged()["hello.txt"]
	if entry.HashState != model.HashStateFailed || entry.HashSHA256 != nil {
		t.Errorf("hash state = %s digest = %v, want failed without a digest", entry.HashState, entry.HashSHA256)
	}
	if entry.ErrorCode == nil || *entry.ErrorCode != string(model.CodeHashUnstable) {
		t.Errorf("error code = %v, want %s", entry.ErrorCode, model.CodeHashUnstable)
	}
	if entry.ErrorStage == nil || *entry.ErrorStage != "hash" {
		t.Errorf("error stage = %v, want hash", entry.ErrorStage)
	}
	if entry.HashComputedAt != nil || entry.HashSize != nil {
		t.Errorf("a failed hash must not keep computed metadata")
	}
	if !strings.Contains(result.CoverageJSON, "hello.txt") ||
		!strings.Contains(result.CoverageJSON, string(model.CodeHashUnstable)) {
		t.Errorf("coverage JSON does not explain the failing path: %s", result.CoverageJSON)
	}
	var decoded struct {
		FailedEntries []struct {
			RelPath string `json:"rel_path"`
			Code    string `json:"code"`
		} `json:"failed_entries"`
	}
	if err := json.Unmarshal([]byte(result.CoverageJSON), &decoded); err != nil {
		t.Fatalf("coverage JSON: %v", err)
	}
	if len(decoded.FailedEntries) != 1 || decoded.FailedEntries[0].RelPath != "hello.txt" {
		t.Errorf("failed_entries = %+v", decoded.FailedEntries)
	}
	if got := stats.Snapshot(); got.Errors != 1 || got.HashFailed != 1 {
		t.Errorf("stats = errors %d, hash failed %d", got.Errors, got.HashFailed)
	}
}

// TestBuildExcludesSelfProducedFiles 驗證資料庫與鎖檔即使存在也不會進索引。
func TestBuildExcludesSelfProducedFiles(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	dbPath := filepath.Join(root, "index.db")
	selfFiles := []string{
		dbPath,
		dbPath + "-wal",
		dbPath + "-shm",
		dbPath + "-journal",
		dbPath + ".evernightindex.lock",
	}
	for _, path := range selfFiles {
		if err := os.WriteFile(path, []byte("self"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	target := &fakeTarget{}
	_, err := Build(context.Background(), target, Options{
		Root:       root,
		CasePolicy: model.CaseSensitive,
		SHA256:     false,
		SelfFiles:  selfFiles,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	staged := target.staged()
	if len(staged) != 2 {
		t.Fatalf("staged %d entries, want 2: %v", len(staged), keysOf(staged))
	}
	if _, ok := staged["keep.txt"]; !ok {
		t.Error("unrelated file was not indexed")
	}
	if _, ok := staged["index.db"]; ok {
		t.Error("the database itself must never be indexed")
	}
}

// TestBuildCancellationStopsBeforeFurtherStaging 驗證取消會中止後續暫存。
func TestBuildCancellationStopsBeforeFurtherStaging(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 20; index++ {
		name := filepath.Join(root, "file"+strings.Repeat("0", 1)+string(rune('a'+index))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := &fakeTarget{stageHook: func(stageCtx context.Context, _ []model.Entry) error {
		cancel()
		return stageCtx.Err()
	}}
	_, err := Build(ctx, target, Options{
		Root:       root,
		CasePolicy: model.CaseSensitive,
		SHA256:     false,
		BatchSize:  2,
	})
	if err == nil {
		t.Fatal("cancellation must abort the build")
	}
	if !IsCanceled(ctx, err) {
		t.Errorf("error = %v, want a cancellation", err)
	}
	if len(target.batches) != 1 {
		t.Errorf("staged %d batches after cancellation, want 1", len(target.batches))
	}
}

// TestBuildStreamsBoundedBatches 驗證批次有界：不會把整棵樹堆在記憶體裡。
func TestBuildStreamsBoundedBatches(t *testing.T) {
	root := t.TempDir()
	const fileCount = 120
	const batchSize = 16
	for index := 0; index < fileCount; index++ {
		name := filepath.Join(root, "f"+string(rune('a'+index%26))+string(rune('a'+index/26%26))+".txt")
		if err := os.WriteFile(name, []byte("payload"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	target := &fakeTarget{}
	_, err := Build(context.Background(), target, Options{
		Root:       root,
		CasePolicy: model.CaseSensitive,
		SHA256:     false,
		BatchSize:  batchSize,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(target.batches) < 2 {
		t.Fatalf("expected several batches, got %d", len(target.batches))
	}
	total := 0
	for _, batch := range target.batches {
		if len(batch) > batchSize {
			t.Errorf("batch carried %d entries, limit is %d", len(batch), batchSize)
		}
		total += len(batch)
	}
	if total < fileCount {
		t.Errorf("staged %d entries, want at least %d", total, fileCount)
	}
}

// TestBuildObservesEntryOutcomes 驗證逐條回呼涵蓋新增與摘要。
func TestBuildObservesEntryOutcomes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	seen := map[model.OpKind]int{}
	_, err := Build(context.Background(), &fakeTarget{}, Options{
		Root:       root,
		CasePolicy: model.CaseSensitive,
		SHA256:     true,
		Observe: func(kind model.OpKind, _ string) {
			seen[kind]++
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if seen[model.OpAdded] == 0 || seen[model.OpHashed] == 0 {
		t.Errorf("observed operations = %v, want at least added and hashed", seen)
	}
}

// appendTo 回傳一個「在讀取後追加內容」的鉤子。
func appendTo(path string) func() {
	return func() {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = file.WriteString("MORE")
		_ = file.Close()
	}
}

// keysOf 取出暫存條目的相對路徑，方便失敗訊息閱讀。
func keysOf(entries map[string]model.Entry) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	return keys
}
