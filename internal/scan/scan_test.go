package scan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/fsmeta"
	"github.com/kagurazakayashi/EvernightIndex/internal/i18n"
	"github.com/kagurazakayashi/EvernightIndex/internal/match"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
)

// localCasePolicy 回傳本機環境的比較策略，僅供測試組裝選項使用。
func localCasePolicy() model.CasePolicy {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return model.CaseInsensitive
	}
	return model.CaseSensitive
}

// mustWrite 建立檔案（含必要的中間目錄）。
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// mustMkdir 建立目錄。
func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// walkResult 是一次測試掃描的收集結果。
type walkResult struct {
	result  Result
	entries map[string]model.Entry
	events  []Event
	err     error
}

// runWalk 執行一次掃描並收集條目與事件。
func runWalk(t *testing.T, opts Options, ctx context.Context) walkResult {
	t.Helper()
	collected := walkResult{entries: map[string]model.Entry{}}
	opts.Sink = func(_ context.Context, batch []model.Entry) error {
		for _, entry := range batch {
			collected.entries[entry.RelPath] = entry
		}
		return nil
	}
	opts.OnEvent = func(event Event) { collected.events = append(collected.events, event) }
	collected.result, collected.err = Walk(ctx, opts)
	return collected
}

// TestWalkSyntheticTree 用真實的合成目錄驗證遍歷、型別與中繼資料。
func TestWalkSyntheticTree(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "file.txt"), "hello")
	mustWrite(t, filepath.Join(root, ".hidden"), "hidden")
	mustWrite(t, filepath.Join(root, ".hidden-dir", "inner.txt"), "inner")
	mustMkdir(t, filepath.Join(root, "empty"))
	mustWrite(t, filepath.Join(root, "sub", "nested", "deep.txt"), "deep")
	mustWrite(t, filepath.Join(root, "sub", "with space.txt"), "space")
	mustWrite(t, filepath.Join(root, "-leading-dash.txt"), "dash")
	mustWrite(t, filepath.Join(root, "日本語 ファイル.txt"), "unicode")

	wantPaths := []string{
		".",
		"file.txt",
		".hidden",
		".hidden-dir",
		".hidden-dir/inner.txt",
		"empty",
		"sub",
		"sub/nested",
		"sub/nested/deep.txt",
		"sub/with space.txt",
		"-leading-dash.txt",
		"日本語 ファイル.txt",
	}
	// Windows 檔名不允許換行，因此換行名稱只在其他平台以真實樣本驗證；
	// 名稱字串本身在各平台都由 TestEntryFieldRules 的白箱檢查覆蓋。
	if runtime.GOOS == "windows" {
		t.Log("skipping newline filename sample: not representable in Windows names")
	} else {
		mustWrite(t, filepath.Join(root, "line\nbreak.txt"), "newline")
		wantPaths = append(wantPaths, "line\nbreak.txt")
	}

	stats := output.NewStats()
	collected := runWalk(t, Options{
		Root:       root,
		CasePolicy: localCasePolicy(),
		RunID:      "run-synthetic",
		Stats:      stats,
	}, context.Background())
	if collected.err != nil {
		t.Fatalf("Walk: %v", collected.err)
	}

	for _, want := range wantPaths {
		if _, ok := collected.entries[want]; !ok {
			t.Fatalf("missing entry %q; got %v", want, sortedKeys(collected.entries))
		}
	}
	if len(collected.entries) != len(wantPaths) {
		t.Fatalf("entry count = %d, want %d: %v", len(collected.entries), len(wantPaths), sortedKeys(collected.entries))
	}

	rootEntry := collected.entries["."]
	if rootEntry.Type != model.EntryTypeDir {
		t.Fatalf("root type = %q, want dir", rootEntry.Type)
	}
	if rootEntry.Name != filepath.Base(root) {
		t.Fatalf("root name = %q, want %q", rootEntry.Name, filepath.Base(root))
	}
	if rootEntry.ParentRel != "" {
		t.Fatalf("root parent = %q, want empty", rootEntry.ParentRel)
	}
	if rootEntry.PathKey != "." {
		t.Fatalf("root path key = %q, want .", rootEntry.PathKey)
	}
	if rootEntry.AbsPath != collected.result.Root.AbsPath {
		t.Fatalf("root abs path = %q, want %q", rootEntry.AbsPath, collected.result.Root.AbsPath)
	}

	fileEntry := collected.entries["file.txt"]
	if fileEntry.Type != model.EntryTypeFile {
		t.Fatalf("file type = %q", fileEntry.Type)
	}
	if fileEntry.Size == nil || *fileEntry.Size != 5 {
		t.Fatalf("file size = %v, want 5", fileEntry.Size)
	}
	if fileEntry.Ext == nil || *fileEntry.Ext != ".txt" {
		t.Fatalf("file ext = %v, want .txt", fileEntry.Ext)
	}
	if fileEntry.ParentRel != "." {
		t.Fatalf("file parent = %q, want .", fileEntry.ParentRel)
	}
	if fileEntry.HashState != model.HashStateNotComputed {
		t.Fatalf("file hash state = %q, want not_computed", fileEntry.HashState)
	}
	if fileEntry.Mtime == nil || fileEntry.Atime == nil {
		t.Fatal("file should have atime and mtime")
	}
	if fileEntry.LastSeenRun != "run-synthetic" || fileEntry.FirstSeenRun != "run-synthetic" {
		t.Fatalf("run ids = %q/%q", fileEntry.FirstSeenRun, fileEntry.LastSeenRun)
	}

	emptyEntry := collected.entries["empty"]
	if emptyEntry.Type != model.EntryTypeDir {
		t.Fatalf("empty dir type = %q", emptyEntry.Type)
	}
	if emptyEntry.Size != nil {
		t.Fatalf("empty dir must not have a size, got %d", *emptyEntry.Size)
	}

	nested := collected.entries["sub/nested/deep.txt"]
	if nested.ParentRel != "sub/nested" {
		t.Fatalf("nested parent = %q", nested.ParentRel)
	}

	if collected.result.Entries != int64(len(wantPaths)) {
		t.Fatalf("result entries = %d, want %d", collected.result.Entries, len(wantPaths))
	}
	if !collected.result.Coverage.Complete() {
		t.Fatalf("coverage should be complete without exclusions: %+v", collected.result.Coverage)
	}
	if collected.result.DirsFailed != 0 || collected.result.DirsPartial != 0 {
		t.Fatalf("unexpected directory failures: %+v", collected.result)
	}
	if collected.result.DirsEnumerated != 5 {
		t.Fatalf("enumerated dirs = %d, want 5", collected.result.DirsEnumerated)
	}

	snapshot := stats.Snapshot()
	if snapshot.Discovered != int64(len(wantPaths)) {
		t.Fatalf("discovered = %d, want %d", snapshot.Discovered, len(wantPaths))
	}
	if snapshot.Processed != int64(len(wantPaths)) {
		t.Fatalf("processed = %d, want %d", snapshot.Processed, len(wantPaths))
	}
	if snapshot.Files+snapshot.Dirs != int64(len(wantPaths)) {
		t.Fatalf("files=%d dirs=%d should add up to %d", snapshot.Files, snapshot.Dirs, len(wantPaths))
	}
	for _, event := range collected.events {
		if event.Kind == EventDirFailed || event.Kind == EventEntryFailed || event.Kind == EventRootInvalid {
			t.Fatalf("unexpected failure event: %+v", event)
		}
	}
}

// TestWalkSymlinksAreNotFollowed 驗證連結本身被記錄、目標不被遞迴。
func TestWalkSymlinksAreNotFollowed(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "target.txt"), "target")
	mustMkdir(t, filepath.Join(root, "dir"))
	mustWrite(t, filepath.Join(root, "dir", "inner.txt"), "inner")

	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Skipf("symlinks are not permitted here: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks are not permitted here: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, "broken.txt")); err != nil {
		t.Skipf("symlinks are not permitted here: %v", err)
	}

	collected := runWalk(t, Options{Root: root, CasePolicy: localCasePolicy(), RunID: "run-links"}, context.Background())
	if collected.err != nil {
		t.Fatalf("Walk: %v", collected.err)
	}
	loop, ok := collected.entries["loop"]
	if !ok {
		t.Fatal("symlink itself should be indexed")
	}
	if loop.Type != model.EntryTypeSymlink {
		t.Fatalf("loop type = %q, want symlink", loop.Type)
	}
	if loop.Size != nil {
		t.Fatal("symlink must not carry the target size")
	}
	for relPath := range collected.entries {
		if strings.HasPrefix(relPath, "loop/") {
			t.Fatalf("scan descended into a symlinked directory: %q", relPath)
		}
	}
	link := collected.entries["link.txt"]
	if link.LinkTarget == nil || *link.LinkTarget != filepath.Join(root, "target.txt") {
		t.Fatalf("link target = %v", link.LinkTarget)
	}
	broken := collected.entries["broken.txt"]
	if broken.Type != model.EntryTypeSymlink {
		t.Fatalf("broken link type = %q", broken.Type)
	}
	if broken.LinkTarget == nil {
		t.Fatal("broken link should keep its target text")
	}
}

// TestWalkExclusionsAndSelfArtifacts 驗證模式排除與程式自產檔案排除。
func TestWalkExclusionsAndSelfArtifacts(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "keep.txt"), "keep")
	mustWrite(t, filepath.Join(root, "cache", "a.txt"), "cached")
	mustWrite(t, filepath.Join(root, "secret.log"), "log")
	dbPath := filepath.Join(root, "index.db")
	mustWrite(t, dbPath, "db")
	mustWrite(t, dbPath+"-wal", "wal")
	mustWrite(t, dbPath+"-shm", "shm")

	excludes, err := match.CompileAll([]string{"cache/", "*.log"}, match.Options{})
	if err != nil {
		t.Fatalf("CompileAll: %v", err)
	}
	collected := runWalk(t, Options{
		Root:       root,
		CasePolicy: localCasePolicy(),
		RunID:      "run-exclude",
		Excludes:   excludes,
		SelfFiles:  SelfArtifacts(dbPath),
	}, context.Background())
	if collected.err != nil {
		t.Fatalf("Walk: %v", collected.err)
	}

	if _, ok := collected.entries["keep.txt"]; !ok {
		t.Fatal("keep.txt should be indexed")
	}
	for _, excluded := range []string{"cache", "cache/a.txt", "secret.log", "index.db", "index.db-wal", "index.db-shm"} {
		if _, ok := collected.entries[excluded]; ok {
			t.Fatalf("%q should be excluded", excluded)
		}
	}
	if collected.result.Excluded != 5 {
		t.Fatalf("excluded count = %d, want 5", collected.result.Excluded)
	}

	reasons := map[string]ExcludeReason{}
	for _, event := range collected.events {
		if event.Kind == EventEntryExcluded {
			reasons[event.RelPath] = event.Reason
		}
	}
	if reasons["cache"] != ExcludeReasonPattern {
		t.Fatalf("cache reason = %q", reasons["cache"])
	}
	if reasons["secret.log"] != ExcludeReasonPattern {
		t.Fatalf("secret.log reason = %q", reasons["secret.log"])
	}
	for _, self := range []string{"index.db", "index.db-wal", "index.db-shm"} {
		if reasons[self] != ExcludeReasonSelfProduced {
			t.Fatalf("%s reason = %q, want self_produced", self, reasons[self])
		}
	}

	excludedDirs := collected.result.Coverage.ExcludedDirs
	if len(excludedDirs) != 1 || excludedDirs[0].RelPath != "cache" || excludedDirs[0].Reason != ExcludeReasonPattern {
		t.Fatalf("excluded dirs should record the skipped subtree: %+v", excludedDirs)
	}
	if collected.result.Coverage.Complete() {
		t.Fatal("coverage with an excluded directory must not be complete")
	}
}

// TestWalkCancellation 驗證取消會讓掃描停止而不是卡住。
func TestWalkCancellation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		mustWrite(t, filepath.Join(root, "dir", "file"+strconvItoa(int64(i))+".txt"), "x")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var result Result
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err = Walk(ctx, Options{
			Root:       root,
			CasePolicy: localCasePolicy(),
			RunID:      "run-cancel",
			Sink: func(_ context.Context, _ []model.Entry) error {
				cancel()
				return nil
			},
		})
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("scan did not stop after cancellation")
	}
	if !result.Interrupted {
		t.Fatalf("result should be marked interrupted: %+v", result)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestWalkSinkError 驗證 Sink 的錯誤會中止掃描並如實回報。
func TestWalkSinkError(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "file.txt"), "x")
	boom := errors.New("sink failed")

	done := make(chan error, 1)
	go func() {
		_, err := Walk(context.Background(), Options{
			Root:       root,
			CasePolicy: localCasePolicy(),
			RunID:      "run-sink",
			Sink:       func(context.Context, []model.Entry) error { return boom },
		})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the sink error", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("scan did not stop after a sink error")
	}
}

// TestWalkRootUnavailable 驗證根目錄不存在時回報穩定錯誤碼。
func TestWalkRootUnavailable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	_, err := Walk(context.Background(), Options{
		Root:       missing,
		CasePolicy: localCasePolicy(),
		Sink:       func(context.Context, []model.Entry) error { return nil },
	})
	if err == nil {
		t.Fatal("Walk should fail for a missing root")
	}
	var modelErr *model.Error
	if !errors.As(err, &modelErr) || modelErr.Code != model.CodeRootUnavailable {
		t.Fatalf("err = %v, want %s", err, model.CodeRootUnavailable)
	}
}

// newTestWalker 建立一個只用手動任務驅動的 walker，供白箱測試使用。
func newTestWalker(t *testing.T) *walker {
	t.Helper()
	w := &walker{
		opts:       Options{Root: t.TempDir(), CasePolicy: model.CaseSensitive, RunID: "run-white"},
		batchSize:  4,
		messages:   make(chan message, 32),
		queueLimit: 8,
		cancel:     func() {},
	}
	w.queueCond = sync.NewCond(&w.queueMu)
	return w
}

// queuedTasks 取出目前排隊中的任務（白箱檢查用）。
func queuedTasks(w *walker) []task {
	w.queueMu.Lock()
	defer w.queueMu.Unlock()
	return append([]task(nil), w.queue...)
}

// drainMessages 取出目前所有待處理訊息。
func drainMessages(w *walker) (events []Event) {
	for {
		select {
		case payload := <-w.messages:
			if payload.event != nil {
				events = append(events, *payload.event)
			}
		default:
			return events
		}
	}
}

// TestRunTaskFailedDirectory 驗證列舉失敗的目錄變成缺口與事件，而不是刪除或崩潰。
func TestRunTaskFailedDirectory(t *testing.T) {
	w := newTestWalker(t)
	missing := filepath.Join(w.opts.Root, "gone")
	w.runTask(context.Background(), task{relPath: "sub", absPath: missing})

	if w.dirFailed.Load() != 1 {
		t.Fatalf("dirFailed = %d, want 1", w.dirFailed.Load())
	}
	if len(w.coverage.FailedDirs) != 1 {
		t.Fatalf("failed gaps = %+v", w.coverage.FailedDirs)
	}
	if w.coverage.FailedDirs[0].RelPath != "sub" {
		t.Fatalf("gap rel path = %q", w.coverage.FailedDirs[0].RelPath)
	}
	if w.coverage.FailedDirs[0].Code != model.CodeScanIO {
		t.Fatalf("gap code = %q", w.coverage.FailedDirs[0].Code)
	}
	events := drainMessages(w)
	if len(events) != 1 || events[0].Kind != EventDirFailed {
		t.Fatalf("events = %+v, want one dir_failed", events)
	}
}

// TestRootTaskInvalidAborts 驗證根目錄失效會中止掃描並標記根錯誤。
func TestRootTaskInvalidAborts(t *testing.T) {
	w := newTestWalker(t)
	w.runTask(context.Background(), task{relPath: rootRelPath, absPath: filepath.Join(w.opts.Root, "gone")})

	if w.rootErr == nil {
		t.Fatal("root failure should be recorded")
	}
	var modelErr *model.Error
	if !errors.As(w.rootErr, &modelErr) || modelErr.Code != model.CodeRootUnavailable {
		t.Fatalf("root error = %v, want %s", w.rootErr, model.CodeRootUnavailable)
	}
	events := drainMessages(w)
	if len(events) != 1 || events[0].Kind != EventRootInvalid {
		t.Fatalf("events = %+v, want one root_invalid", events)
	}
}

// TestPushDirOneFileSystem 驗證跨檔案系統的掛載點被排除且不進入。
func TestPushDirOneFileSystem(t *testing.T) {
	w := newTestWalker(t)
	w.opts.OneFileSystem = true
	w.rootVolume = "dev:1"
	target := filepath.Join(w.opts.Root, "mnt")
	mustMkdir(t, target)

	otherDevice := int64(2)
	otherInfo := fsmeta.Info{Type: model.EntryTypeDir, DeviceID: &otherDevice}
	if w.pushDir(context.Background(), "mnt", target, otherInfo) {
		t.Fatal("a directory on another device must not be descended into")
	}
	if w.excluded.Load() != 1 {
		t.Fatalf("excluded = %d, want 1", w.excluded.Load())
	}
	if len(w.coverage.ExcludedDirs) != 1 || w.coverage.ExcludedDirs[0].Reason != ExcludeReasonCrossDevice {
		t.Fatalf("excluded dirs = %+v", w.coverage.ExcludedDirs)
	}
	if queued := queuedTasks(w); len(queued) != 0 {
		t.Fatalf("cross-device directory should not be queued: %+v", queued)
	}

	sameDevice := int64(1)
	sameInfo := fsmeta.Info{Type: model.EntryTypeDir, DeviceID: &sameDevice}
	if !w.pushDir(context.Background(), "local", filepath.Join(w.opts.Root, "local"), sameInfo) {
		t.Fatal("a directory on the same device should be queued")
	}
	queued := queuedTasks(w)
	if len(queued) != 1 || queued[0].relPath != "local" {
		t.Fatalf("queued tasks = %+v, want one task for local", queued)
	}
}

// TestCoverageJSON 驗證覆蓋結果的穩定 JSON 表示。
func TestCoverageJSON(t *testing.T) {
	coverage := Coverage{
		PartialDirs:  []Gap{{RelPath: "a", Code: model.CodeScanIO, Detail: "boom"}},
		FailedDirs:   []Gap{{RelPath: "b", Code: model.CodeScanIO}},
		ExcludedDirs: []Excluded{{RelPath: "c", Reason: ExcludeReasonPattern}},
	}
	encoded, err := coverage.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !json.Valid([]byte(encoded)) {
		t.Fatalf("invalid json: %s", encoded)
	}
	want := `{"partial_dirs":[{"rel_path":"a","code":"E_SCAN_IO","detail":"boom"}],"failed_dirs":[{"rel_path":"b","code":"E_SCAN_IO"}],"excluded_dirs":[{"rel_path":"c","reason":"pattern"}],"failed_entries":[]}`
	if encoded != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
	empty, err := Coverage{}.JSON()
	if err != nil {
		t.Fatalf("empty JSON: %v", err)
	}
	if empty != `{"partial_dirs":[],"failed_dirs":[],"excluded_dirs":[],"failed_entries":[]}` {
		t.Fatalf("empty json = %s", empty)
	}
}

// TestEventMessageKeysInCatalogs 驗證每個事件種類與排除原因在四語言資源裡都有訊息。
func TestEventMessageKeysInCatalogs(t *testing.T) {
	catalog := i18n.MustDefault()
	kinds := []EventKind{EventDirEnumerated, EventDirPartial, EventDirFailed, EventEntryExcluded, EventEntryFailed, EventRootInvalid}
	for _, kind := range kinds {
		key := kind.MessageKey()
		if key == "err.internal" {
			t.Fatalf("event kind %q has no message key", kind)
		}
		event := Event{Kind: kind, RelPath: "dir/file", Count: 3, Code: model.CodeScanIO, Detail: "detail"}
		for _, lang := range i18n.Supported {
			text := catalog.T(lang, key, event.MessageArgs("reason"))
			if text == "" || text == key || strings.Contains(text, "{") {
				t.Fatalf("%s: %s rendered %q", lang, key, text)
			}
		}
	}
	reasons := []ExcludeReason{ExcludeReasonPattern, ExcludeReasonSelfProduced, ExcludeReasonCrossDevice}
	for _, reason := range reasons {
		if !reason.Valid() {
			t.Fatalf("reason %q should be valid", reason)
		}
		key := reason.MessageKey()
		for _, lang := range i18n.Supported {
			text := catalog.T(lang, key, nil)
			if text == "" || text == key {
				t.Fatalf("%s: %s missing", lang, key)
			}
		}
	}
}

// TestSelfArtifacts 驗證自產檔案清單只包含明確檔案。
func TestSelfArtifacts(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	artifacts := SelfArtifacts(dbPath)
	want := []string{
		dbPath,
		dbPath + "-journal",
		dbPath + "-wal",
		dbPath + "-shm",
		dbPath + model.LockFileSuffix,
	}
	if len(artifacts) != len(want) {
		t.Fatalf("artifacts = %v", artifacts)
	}
	for index, path := range artifacts {
		if path != want[index] {
			t.Fatalf("artifact %d = %q, want %q", index, path, want[index])
		}
	}
	for _, path := range artifacts {
		if filepath.Dir(path) == path {
			t.Fatalf("artifact %q is a directory, not a file", path)
		}
	}
}

// TestEntryFieldRules 驗證路徑欄位的組裝規則（白箱）。
func TestEntryFieldRules(t *testing.T) {
	w := &walker{opts: Options{CasePolicy: model.CaseSensitive, RunID: "run-fields"}}
	rootAbs := filepath.Join("data", "root")
	dirInfo := fsmeta.Info{Type: model.EntryTypeDir}

	root := w.entryFor(rootAbs, ".", dirInfo)
	if root.Name != "root" || root.ParentRel != "" || root.PathKey != "." {
		t.Fatalf("root entry = %+v", root)
	}
	if root.HashState != model.HashStateNotApplicable {
		t.Fatalf("root hash state = %q", root.HashState)
	}

	fileSize := int64(7)
	file := w.entryFor(filepath.Join(rootAbs, "notes.TXT"), "notes.TXT", fsmeta.Info{Type: model.EntryTypeFile, Size: &fileSize})
	if file.Name != "notes.TXT" || file.Ext == nil || *file.Ext != ".txt" {
		t.Fatalf("file entry = %+v", file)
	}
	if file.ParentRel != "." {
		t.Fatalf("file parent = %q", file.ParentRel)
	}
	if file.HashState != model.HashStateNotComputed {
		t.Fatalf("file hash state = %q", file.HashState)
	}

	hidden := w.entryFor(filepath.Join(rootAbs, ".gitignore"), ".gitignore", fsmeta.Info{Type: model.EntryTypeFile})
	if hidden.Ext != nil {
		t.Fatalf("dotfile should have no extension, got %q", *hidden.Ext)
	}

	nested := w.entryFor(filepath.Join(rootAbs, "sub", "deep.txt"), "sub/deep.txt", fsmeta.Info{Type: model.EntryTypeFile})
	if nested.ParentRel != "sub" {
		t.Fatalf("nested parent = %q", nested.ParentRel)
	}
	if nested.PathKey != "sub/deep.txt" {
		t.Fatalf("nested path key = %q", nested.PathKey)
	}

	// 名稱含換行、定位字元或前導連字號時，原始位元組必須原封不動地留在相對路徑與路徑鍵，
	// 只有顯示時才轉義：這是「穩定識別」與「顯示文字」分離的可驗證證據。
	oddNames := []string{"line\nbreak.txt", "-leading-dash.txt", "tab\tname.txt"}
	for _, name := range oddNames {
		entry := w.entryFor(filepath.Join(rootAbs, name), name, fsmeta.Info{Type: model.EntryTypeFile})
		if entry.RelPath != name {
			t.Fatalf("rel path changed: %q -> %q", name, entry.RelPath)
		}
		if entry.Name != name {
			t.Fatalf("name changed: %q -> %q", name, entry.Name)
		}
		if entry.PathKey != name {
			t.Fatalf("path key changed: %q -> %q", name, entry.PathKey)
		}
		if strings.ContainsAny(name, "\n\t") && model.EscapeDisplay(entry.RelPath) == entry.RelPath {
			t.Fatalf("display of %q should be escaped", name)
		}
	}
}

// TestResolvePathUsesCallCWD 驗證顯式相對路徑以呼叫時工作目錄為基準。
func TestResolvePathUsesCallCWD(t *testing.T) {
	callCWD := t.TempDir()
	resolved, err := ResolvePath(callCWD, "sub/../index.db")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if resolved != filepath.Join(callCWD, "index.db") {
		t.Fatalf("resolved = %q, want %q", resolved, filepath.Join(callCWD, "index.db"))
	}
	absolute := filepath.Join(callCWD, "other.db")
	same, err := ResolvePath(callCWD, absolute)
	if err != nil {
		t.Fatalf("ResolvePath absolute: %v", err)
	}
	if same != absolute {
		t.Fatalf("absolute path changed: %q -> %q", absolute, same)
	}
	if _, err := ResolvePath(callCWD, "  "); err == nil {
		t.Fatal("empty path should fail")
	}
	if _, err := ResolvePath("relative-cwd", "index.db"); err == nil {
		t.Fatal("relative call working directory should fail")
	}
}

// sortedKeys 回傳排序後的鍵，讓失敗訊息可讀。
func sortedKeys(entries map[string]model.Entry) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestWalkWideDirectoryDoesNotDeadlock 驗證寬目錄不會讓生產者自鎖。
//
// 迴歸來源：任務佇列原本是無緩衝邏輯的 channel，佇列一滿就把 worker 自己擋住，
// 而 worker 正是唯一能騰出空位的一方。單一 worker 遇到超過佇列深度的同層子目錄
// 就會永久卡住，且不會有任何錯誤訊息。
func TestWalkWideDirectoryDoesNotDeadlock(t *testing.T) {
	root := t.TempDir()
	const dirCount = 24
	for index := 0; index < dirCount; index++ {
		sub := filepath.Join(root, "d"+strconv.Itoa(index))
		mustMkdir(t, sub)
		mustWrite(t, filepath.Join(sub, "f.txt"), "payload")
	}
	mustWrite(t, filepath.Join(root, "top.txt"), "top")

	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		var entries int64
		result, err := Walk(context.Background(), Options{
			Root:       root,
			CasePolicy: model.CaseSensitive,
			RunID:      "run-wide",
			Workers:    1,
			BatchSize:  4,
			Sink: func(_ context.Context, batch []model.Entry) error {
				entries += int64(len(batch))
				return nil
			},
		})
		_ = entries
		done <- outcome{result: result, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("walk: %v", got.err)
		}
		// 根目錄 + 24 個子目錄，加上每個子目錄裡的一個檔案與根目錄的檔案。
		if want := int64(dirCount*2 + 2); got.result.Entries != want {
			t.Fatalf("entries = %d, want %d", got.result.Entries, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Walk did not finish within 30s: the task queue deadlocked")
	}
}
