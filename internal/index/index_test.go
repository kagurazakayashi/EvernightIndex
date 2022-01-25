package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/match"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
	"github.com/kagurazakayashi/EvernightIndex/internal/scan"
)

// 本檔以 t.TempDir() 內的合成目錄驗證索引層的兩條路徑：
// 首次建立（掃描結果 → 暫存區、摘要決策、覆蓋缺口、自產檔案排除、取消與批次有界）
// 與增量更新（快速判據、摘要複用/重算/清除、變化分類、保守刪除、型別替換、排除語意）。

// emptyDigest 是空檔案的標準 SHA256。
const emptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// helloDigest 是內容 "hello" 的標準 SHA256。
const helloDigest = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

// fakeTarget 是記憶體版的目標：暫存批次、刪除清單與「已發佈」狀態。
//
// Unseen 以目前的暫存批次反推集合差集，形狀與 store 的 SQL 反連接一致，
// 讓刪除判定的測試不需要真的開資料庫。
type fakeTarget struct {
	runID     string
	published map[string]model.Entry
	batches   [][]model.Entry
	deletions []string
	stageHook func(ctx context.Context, batch []model.Entry) error
}

// newFakeTarget 建立以 run-test 為執行 ID 的目標。
func newFakeTarget() *fakeTarget {
	return &fakeTarget{runID: "run-test", published: map[string]model.Entry{}}
}

func (f *fakeTarget) RunID() string { return f.runID }
func (f *fakeTarget) RootID() int64 { return 1 }

func (f *fakeTarget) Stage(ctx context.Context, entries []model.Entry) error {
	copied := append([]model.Entry(nil), entries...)
	f.batches = append(f.batches, copied)
	if f.stageHook != nil {
		return f.stageHook(ctx, copied)
	}
	return nil
}

func (f *fakeTarget) StageDeletions(ctx context.Context, pathKeys []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.deletions = append(f.deletions, pathKeys...)
	return nil
}

func (f *fakeTarget) Lookup(ctx context.Context, pathKeys []string) (map[string]model.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found := map[string]model.Entry{}
	for _, key := range pathKeys {
		if entry, ok := f.published[key]; ok {
			found[key] = entry
		}
	}
	return found, nil
}

func (f *fakeTarget) Unseen(ctx context.Context, visit func(model.Entry) error) error {
	seen := map[string]struct{}{}
	for _, batch := range f.batches {
		for _, entry := range batch {
			seen[entry.PathKey] = struct{}{}
		}
	}
	keys := make([]string, 0, len(f.published))
	for key := range f.published {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(f.published[key]); err != nil {
			return err
		}
	}
	return nil
}

// publish 把目前暫存的結果併入「已發佈」狀態，模擬一次成功的發佈。
func (f *fakeTarget) publish() {
	for _, batch := range f.batches {
		for _, entry := range batch {
			f.published[entry.PathKey] = entry
		}
	}
	for _, key := range f.deletions {
		delete(f.published, key)
	}
	f.batches = nil
	f.deletions = nil
}

// staged 回傳本次暫存的所有條目（依相對路徑索引）。
func (f *fakeTarget) staged() map[string]model.Entry {
	collected := map[string]model.Entry{}
	for _, batch := range f.batches {
		for _, entry := range batch {
			collected[entry.RelPath] = entry
		}
	}
	return collected
}

// publishedEntry 取出已發佈狀態中的一條路徑。
func (f *fakeTarget) publishedEntry(t *testing.T, relPath string) model.Entry {
	t.Helper()
	entry, ok := f.published[model.PathKey(relPath, model.CaseSensitive)]
	if !ok {
		t.Fatalf("path %q is not published", relPath)
	}
	return entry
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

// runBuild 執行一次建索引，錯誤時直接結束測試。
func runBuild(t *testing.T, target Target, options Options) Result {
	t.Helper()
	if options.CasePolicy == "" {
		options.CasePolicy = model.CaseSensitive
	}
	result, err := Build(context.Background(), target, options)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return result
}

// digestOf 計算內容的標準 SHA256。
func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// TestBuildStoresMetadataAndHashes 驗證條目欄位、摘要狀態與可觀察讀取計數。
func TestBuildStoresMetadataAndHashes(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	stats := output.NewStats()
	result := runBuild(t, target, Options{
		Root:        root,
		SHA256:      true,
		Stats:       stats,
		ReadRetries: 1,
	})
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
		"nested/deep/data.md": digestOf("abcdefghij"),
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
	if result.Added != 8 || result.Removed != 0 || result.Updated != 0 {
		t.Errorf("classification = added %d, updated %d, removed %d", result.Added, result.Updated, result.Removed)
	}
	if got := stats.Snapshot(); got.HashComputed != 4 || got.BytesRead != 16 || got.Added != 8 {
		t.Errorf("stats = hashed %d, bytes %d, added %d", got.HashComputed, got.BytesRead, got.Added)
	}
}

// TestBuildSkipsContentWhenSHA256Disabled 驗證關閉 SHA256 時完全不讀內容。
func TestBuildSkipsContentWhenSHA256Disabled(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	stats := output.NewStats()
	result := runBuild(t, target, Options{
		Root:   root,
		SHA256: false,
		Stats:  stats,
	})
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
	target := newFakeTarget()
	_, err := Build(context.Background(), target, Options{
		Root:   t.TempDir(),
		SHA256: false,
		Deep:   true,
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
	target := newFakeTarget()
	stats := output.NewStats()
	result := runBuild(t, target, Options{
		Root:          root,
		SHA256:        true,
		Stats:         stats,
		ReadRetries:   0,
		HashAfterRead: appendTo(filepath.Join(root, "hello.txt")),
	})
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
	target := newFakeTarget()
	runBuild(t, target, Options{
		Root:      root,
		SHA256:    false,
		SelfFiles: selfFiles,
	})
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
		name := filepath.Join(root, "file"+string(rune('a'+index))+".txt")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := newFakeTarget()
	target.stageHook = func(stageCtx context.Context, _ []model.Entry) error {
		cancel()
		return stageCtx.Err()
	}
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
	if len(target.deletions) != 0 {
		t.Errorf("a canceled run must not stage deletions")
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
	target := newFakeTarget()
	runBuild(t, target, Options{
		Root:      root,
		SHA256:    false,
		BatchSize: batchSize,
	})
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
	runBuild(t, newFakeTarget(), Options{
		Root:   root,
		SHA256: true,
		Observe: func(kind model.OpKind, _ string) {
			seen[kind]++
		},
	})
	if seen[model.OpAdded] == 0 || seen[model.OpHashed] == 0 {
		t.Errorf("observed operations = %v, want at least added and hashed", seen)
	}
}

// TestUpdateReusesDigestWhenCriteriaUnchanged 驗證快速模式在判據未變時不讀內容。
func TestUpdateReusesDigestWhenCriteriaUnchanged(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	result := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	if result.ContentOpens != 0 || result.BytesRead != 0 {
		t.Errorf("quick mode read content without cause: opens %d, bytes %d", result.ContentOpens, result.BytesRead)
	}
	if result.HashReused != 4 {
		t.Errorf("hash reused = %d, want 4", result.HashReused)
	}
	if result.Added != 0 || result.Updated != 0 || result.Removed != 0 {
		t.Errorf("classification = added %d, updated %d, removed %d", result.Added, result.Updated, result.Removed)
	}
	if result.Unchanged != 8 {
		t.Errorf("unchanged = %d, want 8", result.Unchanged)
	}
	for relPath, entry := range target.staged() {
		if entry.Type != model.EntryTypeFile {
			continue
		}
		if entry.HashState != model.HashStateReused {
			t.Errorf("%s: hash state = %s, want reused", relPath, entry.HashState)
		}
		if entry.HashSHA256 == nil {
			t.Errorf("%s: reused digest must be kept", relPath)
		}
		if entry.FirstSeenRun != "run-test" {
			t.Errorf("%s: first seen run = %s", relPath, entry.FirstSeenRun)
		}
	}
}

// TestUpdateDetectsSameSizeContentChange 驗證同大小但 mtime 改變的內容修改會被重算。
func TestUpdateDetectsSameSizeContentChange(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	// 內容換成同樣 5 位元組，長度不變，只有 mtime 變。
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("HELLO"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	result := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	if result.ContentOpens != 1 {
		t.Errorf("content opens = %d, want 1 (only the changed file)", result.ContentOpens)
	}
	if result.ContentChange != 1 {
		t.Errorf("content changes = %d, want 1", result.ContentChange)
	}
	entry := target.staged()["hello.txt"]
	if entry.HashSHA256 == nil || *entry.HashSHA256 != digestOf("HELLO") {
		t.Errorf("digest = %v, want the digest of the new content", entry.HashSHA256)
	}
	if entry.HashState != model.HashStateComputed {
		t.Errorf("hash state = %s, want computed", entry.HashState)
	}
}

// TestUpdateDetectsContentChangeAfterRestoredMtime 驗證「改內容再把 mtime 還原」。
//
// 這種修改讓大小與 mtime 都看起來沒變；快速判據靠檔案身分與 change time 保守觸發，
// 因此仍然會重算摘要並確認內容變化。平台上取不到 change time 時無法區分，跳過。
func TestUpdateDetectsContentChangeAfterRestoredMtime(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	original := target.publishedEntry(t, "hello.txt")
	if original.Mtime == nil || original.Ctime == nil {
		t.Skip("this platform does not report both mtime and change time")
	}
	stamp, ok := original.Mtime.Time()
	if !ok {
		t.Fatalf("cannot parse recorded mtime %q", *original.Mtime)
	}
	path := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(path, []byte("HELLO"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("restore mtime: %v", err)
	}
	result := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	if result.ContentOpens != 1 {
		t.Errorf("content opens = %d, want 1: a restored mtime must not hide the change", result.ContentOpens)
	}
	if result.ContentChange != 1 {
		t.Errorf("content changes = %d, want 1", result.ContentChange)
	}
	entry := target.staged()["hello.txt"]
	if entry.HashSHA256 == nil || *entry.HashSHA256 != digestOf("HELLO") {
		t.Errorf("digest = %v, want the digest of the new content", entry.HashSHA256)
	}
}

// TestUpdateMetadataOnlyChangeIsNotContentChange 驗證「mtime 變但摘要相同」不算內容變化。
func TestUpdateMetadataOnlyChangeIsNotContentChange(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	path := filepath.Join(root, "hello.txt")
	later := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("touch: %v", err)
	}
	result := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	if result.ContentOpens != 1 {
		t.Errorf("content opens = %d, want 1", result.ContentOpens)
	}
	if result.ContentChange != 0 {
		t.Errorf("content changes = %d, want 0: an identical digest is not a content change", result.ContentChange)
	}
	if result.MetaChange != 1 {
		t.Errorf("metadata changes = %d, want 1", result.MetaChange)
	}
	entry := target.staged()["hello.txt"]
	if entry.HashSHA256 == nil || *entry.HashSHA256 != helloDigest {
		t.Errorf("digest = %v, want the unchanged digest", entry.HashSHA256)
	}
}

// TestUpdateClearsDigestWithoutSHA256 驗證關閉保存時：不讀內容、變化清除舊摘要、未變保留。
func TestUpdateClearsDigestWithoutSHA256(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("HELLO"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	result := runBuild(t, target, Options{Root: root, SHA256: false})
	if result.ContentOpens != 0 || result.BytesRead != 0 {
		t.Errorf("closed hashing must not read content: opens %d, bytes %d", result.ContentOpens, result.BytesRead)
	}
	if result.HashInvalidated != 1 {
		t.Errorf("invalidated digests = %d, want 1", result.HashInvalidated)
	}
	staged := target.staged()
	changed := staged["hello.txt"]
	if changed.HashSHA256 != nil || changed.HashState != model.HashStateDisabled {
		t.Errorf("changed file kept a digest: state %s digest %v", changed.HashState, changed.HashSHA256)
	}
	if changed.HashComputedAt != nil || changed.HashSize != nil || changed.HashMTime != nil {
		t.Errorf("a cleared digest must drop its size/mtime baseline")
	}
	kept := staged["zero.txt"]
	if kept.HashSHA256 == nil || *kept.HashSHA256 != emptyDigest {
		t.Errorf("unchanged file lost its digest: %v", kept.HashSHA256)
	}
	if kept.HashState != model.HashStateComputed {
		t.Errorf("unchanged file hash state = %s, want the recorded state", kept.HashState)
	}
	// 沿用舊摘要不是本次驗證，因此不能計成「已複用」或「已計算」。
	if result.HashReused != 0 || result.Hashed != 0 {
		t.Errorf("closed hashing counted hash work: reused %d, hashed %d", result.HashReused, result.Hashed)
	}
}

// TestUpdateDeepRecomputesEveryFile 驗證細緻模式重算所有可讀取普通檔案。
func TestUpdateDeepRecomputesEveryFile(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	result := runBuild(t, target, Options{Root: root, SHA256: true, Deep: true, ReadRetries: 1})
	if result.ContentOpens != 4 {
		t.Errorf("content opens = %d, want 4 (every regular file)", result.ContentOpens)
	}
	if result.HashReused != 0 {
		t.Errorf("deep mode reused %d digests, want 0", result.HashReused)
	}
	if result.ContentChange != 0 || result.MetaChange != 0 || result.Updated != 0 {
		t.Errorf("deep verification of unchanged content reported changes: content %d, metadata %d, updated %d",
			result.ContentChange, result.MetaChange, result.Updated)
	}
	for relPath, entry := range target.staged() {
		if entry.Type != model.EntryTypeFile {
			continue
		}
		if entry.HashState != model.HashStateComputed {
			t.Errorf("%s: hash state = %s, want computed", relPath, entry.HashState)
		}
		if entry.HashComputedAt == nil {
			t.Errorf("%s: a recomputed digest must record when it was computed", relPath)
		}
	}
}

// TestUpdateEstablishesBaselineWhenDigestMissing 驗證只有基線、沒有歷史摘要時不宣告內容變化。
func TestUpdateEstablishesBaselineWhenDigestMissing(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: false})
	target.publish()

	result := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	if result.HashBaseline != 4 {
		t.Errorf("baseline digests = %d, want 4", result.HashBaseline)
	}
	if result.ContentChange != 0 {
		t.Errorf("content changes = %d, want 0: a new baseline cannot prove a change", result.ContentChange)
	}
	if result.Updated != 4 {
		t.Errorf("updated = %d, want 4 (the digest baseline changed)", result.Updated)
	}
	for relPath, entry := range target.staged() {
		if entry.Type != model.EntryTypeFile {
			continue
		}
		if entry.HashState != model.HashStateComputed || entry.HashSHA256 == nil {
			t.Errorf("%s: hash state = %s digest = %v", relPath, entry.HashState, entry.HashSHA256)
		}
	}
}

// TestUpdateRemovesMissingEntries 驗證消失的路徑會被暫存刪除。
func TestUpdateRemovesMissingEntries(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	if err := os.Remove(filepath.Join(root, "zero.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	result := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	if result.Removed != 1 {
		t.Errorf("removed = %d, want 1", result.Removed)
	}
	if len(target.deletions) != 1 || target.deletions[0] != model.PathKey("zero.txt", model.CaseSensitive) {
		t.Errorf("staged deletions = %v", target.deletions)
	}
	if _, ok := target.staged()["zero.txt"]; ok {
		t.Error("a removed path must not be staged as an upsert")
	}
}

// TestUpdateMarksExcludedInsteadOfRemoving 驗證排除規則改變是 excluded 而不是刪除。
func TestUpdateMarksExcludedInsteadOfRemoving(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	excludes, err := match.CompileAll([]string{"nested/"}, match.Options{})
	if err != nil {
		t.Fatalf("compile excludes: %v", err)
	}
	result := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1, Excludes: excludes})
	if len(target.deletions) != 0 {
		t.Errorf("exclusions must never delete: %v", target.deletions)
	}
	if result.Excluded != 3 {
		t.Errorf("excluded = %d, want 3 (the directory and its subtree)", result.Excluded)
	}
	staged := target.staged()
	for _, relPath := range []string{"nested", "nested/deep", "nested/deep/data.md"} {
		entry, ok := staged[relPath]
		if !ok {
			t.Fatalf("%s was not staged as excluded", relPath)
		}
		if entry.Status != model.StatusExcluded {
			t.Errorf("%s: status = %s, want excluded", relPath, entry.Status)
		}
	}
	if result.Coverage != model.CoverageComplete {
		t.Errorf("coverage = %s: a user exclusion is not a coverage gap", result.Coverage)
	}
}

// TestUpdateKeepsUncoveredRegionUnverified 驗證未覆蓋區域不清空舊記錄。
//
// 這裡直接餵入合成的覆蓋證據：目錄列舉失敗、局部失敗或跨檔案系統都會進入
// 未覆蓋區域，其中的舊條目只能標成 unverified，不能當成已刪除。
func TestUpdateKeepsUncoveredRegionUnverified(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	b := &builder{
		target:    target,
		options:   Options{CasePolicy: model.CaseSensitive, SHA256: true},
		now:       time.Now,
		excluder:  scan.NewExcluder(nil, model.CaseSensitive, nil, nil),
		batchSize: 2,
	}
	b.regions = b.buildRegions(scan.Coverage{
		FailedDirs:  []scan.Gap{{RelPath: "nested", Code: model.CodeScanIO}},
		PartialDirs: []scan.Gap{{RelPath: "empty", Code: model.CodeScanIO}},
	})
	// 模擬「這次沒有看到」：清空暫存批次，讓所有已發佈條目都成為候選。
	if err := b.retire(context.Background()); err != nil {
		t.Fatalf("retire: %v", err)
	}
	deleted := map[string]bool{}
	for _, key := range target.deletions {
		if strings.HasPrefix(key, "nested") || key == "empty" || key == "." {
			t.Errorf("path %q is inside an uncovered region and must not be deleted", key)
		}
		deleted[key] = true
	}
	for _, relPath := range []string{".hidden", "hello.txt", "zero.txt"} {
		if !deleted[model.PathKey(relPath, model.CaseSensitive)] {
			t.Errorf("path %q is fully covered and missing, so it must be deleted: %v", relPath, target.deletions)
		}
	}
	if len(target.deletions) != 3 {
		t.Errorf("deletions = %v, want the three covered paths", target.deletions)
	}
	staged := target.staged()
	for _, relPath := range []string{".", "nested", "nested/deep", "nested/deep/data.md", "empty"} {
		entry, ok := staged[relPath]
		if !ok {
			t.Fatalf("%s was not kept as unverified", relPath)
		}
		if entry.Status != model.StatusUnverified {
			t.Errorf("%s: status = %s, want unverified", relPath, entry.Status)
		}
		if entry.LastSeenRun != "run-test" {
			t.Errorf("%s: an unverified entry must not claim this run saw it", relPath)
		}
	}
	if b.result.Unverified != 5 {
		t.Errorf("unverified = %d, want 5 (including the root entry)", b.result.Unverified)
	}
	if b.result.Removed != 3 {
		t.Errorf("removed = %d, want 3", b.result.Removed)
	}
}

// TestUpdateHandlesDirectoryReplacedByFile 驗證目錄變成檔案時舊子樹的一致性。
func TestUpdateHandlesDirectoryReplacedByFile(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	target.publish()

	if err := os.RemoveAll(filepath.Join(root, "nested")); err != nil {
		t.Fatalf("remove tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested"), []byte("now a file"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	result := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	if result.TypeChanged != 1 {
		t.Errorf("type changes = %d, want 1", result.TypeChanged)
	}
	if result.Removed != 2 {
		t.Errorf("removed = %d, want 2 (the old subtree)", result.Removed)
	}
	staged := target.staged()["nested"]
	if staged.Type != model.EntryTypeFile {
		t.Errorf("type = %s, want file", staged.Type)
	}
	if staged.HashSHA256 == nil || *staged.HashSHA256 != digestOf("now a file") {
		t.Errorf("digest = %v, want the digest of the new content", staged.HashSHA256)
	}
	if staged.FirstSeenRun != "run-test" {
		t.Errorf("first seen run = %s, want the original run", staged.FirstSeenRun)
	}
}

// TestUpdateHashFailureClearsPreviousDigest 驗證重算失敗不把舊摘要留成當前摘要。
func TestUpdateHashFailureClearsPreviousDigest(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 0})
	target.publish()

	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("HELLO"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	result := runBuild(t, target, Options{
		Root:          root,
		SHA256:        true,
		ReadRetries:   0,
		HashAfterRead: appendTo(filepath.Join(root, "hello.txt")),
	})
	if result.HashFailed != 1 {
		t.Fatalf("hash failures = %d, want 1", result.HashFailed)
	}
	entry := target.staged()["hello.txt"]
	if entry.HashSHA256 != nil {
		t.Errorf("digest = %v, want no digest after a failed recomputation", entry.HashSHA256)
	}
	if entry.HashState != model.HashStateFailed {
		t.Errorf("hash state = %s, want failed", entry.HashState)
	}
	if result.Coverage != model.CoveragePartial {
		t.Errorf("coverage = %s, want partial", result.Coverage)
	}
}

// TestUpdateRecordsChangeEventsWithSnapshots 驗證變化事件帶有 before/after 快照。
func TestUpdateRecordsChangeEventsWithSnapshots(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	first := runBuild(t, target, Options{Root: root, SHA256: true, ChangeLog: true, ReadRetries: 1})
	if len(first.Events) != 8 {
		t.Fatalf("first run events = %d, want 8", len(first.Events))
	}
	for _, event := range first.Events {
		if event.Type != model.EventAdded || event.BeforeExists || !event.AfterExists {
			t.Errorf("unexpected first-run event: %+v", event)
		}
		if event.AfterJSON == nil || !strings.Contains(*event.AfterJSON, `"entry_type"`) {
			t.Errorf("added event has no after snapshot: %+v", event)
		}
	}
	target.publish()

	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("HELLO"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	second := runBuild(t, target, Options{Root: root, SHA256: true, ChangeLog: true, ReadRetries: 1})
	if len(second.Events) != 1 {
		t.Fatalf("second run events = %d, want 1: %+v", len(second.Events), second.Events)
	}
	event := second.Events[0]
	if event.Type != model.EventContentChanged || event.RelPath != "hello.txt" {
		t.Errorf("event = %s %s, want content_changed for hello.txt", event.Type, event.RelPath)
	}
	if !event.BeforeExists || !event.AfterExists || event.BeforeJSON == nil || event.AfterJSON == nil {
		t.Fatalf("event must carry both snapshots: %+v", event)
	}
	if !strings.Contains(*event.BeforeJSON, helloDigest) || !strings.Contains(*event.AfterJSON, digestOf("HELLO")) {
		t.Errorf("snapshots do not carry the digests: before %s after %s", *event.BeforeJSON, *event.AfterJSON)
	}
	if !contains(event.ChangedFields, "hash_sha256") {
		t.Errorf("changed fields = %v, want hash_sha256", event.ChangedFields)
	}
}

// TestUpdateKeepsCountsWithoutChangeLog 驗證關閉變化日誌時不保留完整歷史但仍有計數。
func TestUpdateKeepsCountsWithoutChangeLog(t *testing.T) {
	root := newTree(t)
	target := newFakeTarget()
	first := runBuild(t, target, Options{Root: root, SHA256: true, ReadRetries: 1})
	if len(first.Events) != 0 {
		t.Errorf("events = %d, want none while the change log is off", len(first.Events))
	}
	if first.Added != 8 {
		t.Errorf("added = %d, want 8", first.Added)
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
	sort.Strings(keys)
	return keys
}

// contains 判斷字串清單是否包含指定值。
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
