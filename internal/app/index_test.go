package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/store"
)

// 本檔以 t.TempDir() 內的合成目錄驗證 index 命令的編排：
// 首次建立、既有索引拒絕、未實作選項拒絕、根目錄不可用、等鎖時取消、
// 資料庫寫入失敗、未完成資料庫的重建，以及摘要失敗造成的部分完成。

// indexOptions 建立 index 請求的預設選項。
func indexOptions() Options {
	return Options{SHA256: true, ReadRetries: 1, LockTimeout: 2 * time.Second}
}

// writeSample 建立一個內容固定的檔案。
func writeSample(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// runIndexCommand 直接呼叫應用服務執行 index。
func runIndexCommand(t *testing.T, service *Service, ctx context.Context, root string, options Options) error {
	t.Helper()
	return service.Run(ctx, Request{Command: "index", Args: []string{root}, Options: options})
}

// TestIndexCommandCreatesDatabase 驗證首次建立：完整中繼資料、摘要與輸出分流。
func TestIndexCommandCreatesDatabase(t *testing.T) {
	deps, stdout, stderr := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "hello.txt", "hello")
	writeSample(t, rootDir, "zero.txt", "")
	if err := os.MkdirAll(filepath.Join(rootDir, "empty"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ctx := context.Background()
	if err := runIndexCommand(t, service, ctx, rootDir, indexOptions()); err != nil {
		t.Fatalf("index: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("index must not write results to stdout: %q", stdout.String())
	}
	// 進度與最終統計走標準錯誤，且必須在開始前說清楚會不會讀取內容。
	for _, want := range []string{"SHA256", "完成", "已发布代次"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr is missing %q: %q", want, stderr.String())
		}
	}

	dbPath := filepath.Join(rootDir, "index.db")
	storage, err := store.Open(ctx, dbPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = storage.Close() }()

	generation, err := storage.PublishedGeneration(ctx)
	if err != nil {
		t.Fatalf("generation: %v", err)
	}
	if generation != 1 {
		t.Errorf("published generation = %d, want 1", generation)
	}
	bound, hasRoot, err := storage.Root(ctx)
	if err != nil || !hasRoot {
		t.Fatalf("root: %v (bound %v)", err, hasRoot)
	}
	if bound.AbsPath != rootDir {
		t.Errorf("bound root = %q, want %q", bound.AbsPath, rootDir)
	}
	summary, err := storage.EntrySummary(ctx, bound.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Total != 4 || summary.Files != 2 || summary.Dirs != 2 {
		t.Errorf("summary = %+v, want 4 entries (2 files, 2 dirs)", summary)
	}
	if summary.HashComputed != 2 || summary.HashNotApplicable != 2 {
		t.Errorf("hash states = computed %d, not_applicable %d", summary.HashComputed, summary.HashNotApplicable)
	}

	entry, ok, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("hello.txt", bound.CasePolicy))
	if err != nil || !ok {
		t.Fatalf("published entry: %v (found %v)", err, ok)
	}
	if entry.HashState != model.HashStateComputed || entry.HashSHA256 == nil ||
		*entry.HashSHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("hello.txt hash = %s %v", entry.HashState, entry.HashSHA256)
	}
	if entry.HashComputedAt == nil || entry.HashSize == nil || entry.HashMTime == nil {
		t.Error("hash metadata must record when and against which size/mtime it was computed")
	}
	emptyEntry, ok, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("zero.txt", bound.CasePolicy))
	if err != nil || !ok {
		t.Fatalf("empty file entry: %v (found %v)", err, ok)
	}
	if emptyEntry.HashSHA256 == nil ||
		*emptyEntry.HashSHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("empty file digest = %v", emptyEntry.HashSHA256)
	}
	if emptyEntry.Size == nil || *emptyEntry.Size != 0 {
		t.Errorf("empty file size = %v, want 0", emptyEntry.Size)
	}
	if _, ok, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("index.db", bound.CasePolicy)); err != nil || ok {
		t.Errorf("the database itself must not be indexed (found %v, err %v)", ok, err)
	}
	if _, ok, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey(store.LockPath(dbPath), bound.CasePolicy)); err != nil || ok {
		t.Errorf("the lock file must not be indexed (found %v, err %v)", ok, err)
	}
}

// openIndex 以唯讀方式開啟索引，測試結束時自動關閉。
func openIndex(t *testing.T, ctx context.Context, dbPath string) (*store.Store, model.Root) {
	t.Helper()
	storage, err := store.Open(ctx, dbPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	bound, found, err := storage.Root(ctx)
	if err != nil || !found {
		t.Fatalf("read bound root: %v (found %v)", err, found)
	}
	return storage, bound
}

// TestIndexUpdatesExistingIndex 驗證第二次執行是增量更新，不是重建。
func TestIndexUpdatesExistingIndex(t *testing.T) {
	deps, _, stderr := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "a.txt", "a")
	ctx := context.Background()
	if err := runIndexCommand(t, service, ctx, rootDir, indexOptions()); err != nil {
		t.Fatalf("first index: %v", err)
	}
	stderr.Reset()
	writeSample(t, rootDir, "b.txt", "b")
	// 未變動的檔案不該被重新讀取；新增的檔案必須計算摘要。
	if err := runIndexCommand(t, service, ctx, rootDir, indexOptions()); err != nil {
		t.Fatalf("second index run must update the existing index: %v", err)
	}

	dbPath := filepath.Join(rootDir, "index.db")
	storage, bound := openIndex(t, ctx, dbPath)
	generation, err := storage.PublishedGeneration(ctx)
	if err != nil || generation != 2 {
		t.Fatalf("published generation = %d (err %v), want 2", generation, err)
	}
	total, err := storage.CountPublished(ctx, bound.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 3 {
		t.Errorf("published entries = %d, want 3 (root, a.txt, b.txt)", total)
	}
	added, found, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("b.txt", bound.CasePolicy))
	if err != nil || !found {
		t.Fatalf("b.txt: %v (found %v)", err, found)
	}
	if added.HashState != model.HashStateComputed || added.HashSHA256 == nil {
		t.Errorf("b.txt hash = %s %v, want a computed digest", added.HashState, added.HashSHA256)
	}
	reused, _, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("a.txt", bound.CasePolicy))
	if err != nil {
		t.Fatalf("a.txt: %v", err)
	}
	if reused.HashState != model.HashStateReused {
		t.Errorf("a.txt hash state = %s, want reused (quick mode, unchanged criteria)", reused.HashState)
	}
	if reused.FirstSeenRun == added.FirstSeenRun {
		t.Errorf("first seen run must not be rewritten by an update (both %q)", reused.FirstSeenRun)
	}
	if !strings.Contains(stderr.String(), "变化") {
		t.Errorf("an update must report what changed: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "快速模式") {
		t.Errorf("quick mode must explain that it cannot detect every content change: %q", stderr.String())
	}
}

// TestIndexUpdateRemovesDeletedEntries 驗證消失的路徑在更新後從已發佈索引移除。
func TestIndexUpdateRemovesDeletedEntries(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "keep.txt", "keep")
	gone := writeSample(t, rootDir, "gone.txt", "gone")
	ctx := context.Background()
	if err := runIndexCommand(t, service, ctx, rootDir, indexOptions()); err != nil {
		t.Fatalf("first index: %v", err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := runIndexCommand(t, service, ctx, rootDir, indexOptions()); err != nil {
		t.Fatalf("update: %v", err)
	}
	dbPath := filepath.Join(rootDir, "index.db")
	storage, bound := openIndex(t, ctx, dbPath)
	if _, found, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("gone.txt", bound.CasePolicy)); err != nil || found {
		t.Errorf("gone.txt is still published (found %v, err %v)", found, err)
	}
	if _, found, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("keep.txt", bound.CasePolicy)); err != nil || !found {
		t.Errorf("keep.txt disappeared (found %v, err %v)", found, err)
	}
}

// TestIndexUpdateMarksExcludedEntries 驗證排除規則改變是 excluded，不是刪除。
func TestIndexUpdateMarksExcludedEntries(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "keep.txt", "keep")
	writeSample(t, rootDir, "skip.tmp", "skip")
	ctx := context.Background()
	if err := runIndexCommand(t, service, ctx, rootDir, indexOptions()); err != nil {
		t.Fatalf("first index: %v", err)
	}
	options := indexOptions()
	options.Excludes = []string{"*.tmp"}
	if err := runIndexCommand(t, service, ctx, rootDir, options); err != nil {
		t.Fatalf("update with an exclusion: %v", err)
	}
	dbPath := filepath.Join(rootDir, "index.db")
	storage, bound := openIndex(t, ctx, dbPath)
	entry, found, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("skip.tmp", bound.CasePolicy))
	if err != nil || !found {
		t.Fatalf("skip.tmp was deleted instead of being marked excluded (found %v, err %v)", found, err)
	}
	if entry.Status != model.StatusExcluded {
		t.Errorf("skip.tmp status = %s, want excluded", entry.Status)
	}
}

// TestIndexRejectsWrongRoot 驗證資料庫綁定的根目錄不同時拒絕更新。
func TestIndexRejectsWrongRoot(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	firstDir := t.TempDir()
	writeSample(t, firstDir, "a.txt", "a")
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	ctx := context.Background()
	options := indexOptions()
	options.DB = dbPath
	if err := runIndexCommand(t, service, ctx, firstDir, options); err != nil {
		t.Fatalf("first index: %v", err)
	}
	otherDir := t.TempDir()
	writeSample(t, otherDir, "b.txt", "b")
	err := runIndexCommand(t, service, ctx, otherDir, options)
	appErr, ok := err.(*model.Error)
	if !ok || appErr.Code != model.CodeRootMismatch {
		t.Fatalf("error = %v, want %s", err, model.CodeRootMismatch)
	}
	storage, _ := openIndex(t, ctx, dbPath)
	generation, _ := storage.PublishedGeneration(ctx)
	if generation != 1 {
		t.Errorf("published generation = %d, want 1 (a refused run must change nothing)", generation)
	}
}

// TestIndexUpdateFailureKeepsPreviousGeneration 驗證更新失敗時舊版完整可用。
func TestIndexUpdateFailureKeepsPreviousGeneration(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "a.txt", "a")
	ctx := context.Background()
	if err := runIndexCommand(t, service, ctx, rootDir, indexOptions()); err != nil {
		t.Fatalf("first index: %v", err)
	}
	writeSample(t, rootDir, "b.txt", "b")
	dbPath := filepath.Join(rootDir, "index.db")

	// 以另一個連線持有寫入交易，模擬「資料庫目前不能寫入」。
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	runErr := runIndexCommand(t, service, ctx, rootDir, indexOptions())
	if _, rollbackErr := conn.ExecContext(context.Background(), "ROLLBACK"); rollbackErr != nil {
		t.Fatalf("rollback: %v", rollbackErr)
	}
	if runErr == nil {
		t.Fatal("a database write failure must be reported")
	}
	if code := model.ExitCodeOf(runErr); code != model.ExitFatal {
		t.Errorf("exit code = %d, want %d (error %v)", code, model.ExitFatal, runErr)
	}
	storage, bound := openIndex(t, ctx, dbPath)
	generation, err := storage.PublishedGeneration(ctx)
	if err != nil || generation != 1 {
		t.Errorf("published generation = %d (err %v), want 1", generation, err)
	}
	total, err := storage.CountPublished(ctx, bound.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Errorf("published entries = %d, want 2 (the failed update must not publish)", total)
	}
	if _, found, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("b.txt", bound.CasePolicy)); err != nil || found {
		t.Errorf("b.txt was published by a failed run (found %v, err %v)", found, err)
	}
	latest, found, err := storage.LatestRun(ctx)
	if err != nil {
		t.Fatalf("latest run: %v", err)
	}
	if found && latest.Generation != nil && *latest.Generation > 1 {
		t.Errorf("latest run published generation %d; a failed update must not publish", *latest.Generation)
	}
}

// TestIndexRejectsUnimplementedOptions 驗證尚未實作的選項被明確拒絕。
func TestIndexRejectsUnimplementedOptions(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "a.txt", "a")
	cases := []struct {
		name    string
		options Options
	}{
		{"change log", Options{SHA256: true, ChangeLog: true, LockTimeout: time.Second}},
		{"temp dir", Options{SHA256: true, TempDir: t.TempDir(), LockTimeout: time.Second}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := runIndexCommand(t, service, context.Background(), rootDir, testCase.options)
			if err == nil {
				t.Fatal("an unimplemented option must not be silently ignored")
			}
			appErr, ok := err.(*model.Error)
			if !ok || appErr.Code != model.CodeNotImplemented {
				t.Fatalf("error = %v, want %s", err, model.CodeNotImplemented)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(rootDir, "index.db")); !os.IsNotExist(err) {
		t.Errorf("rejected runs must not create a database (stat err = %v)", err)
	}
}

// TestIndexRejectsDeepWithoutSHA256 驗證非法組合在掃描前被拒絕。
func TestIndexRejectsDeepWithoutSHA256(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "a.txt", "a")
	err := runIndexCommand(t, service, context.Background(), rootDir,
		Options{SHA256: false, Deep: true, LockTimeout: time.Second})
	appErr, ok := err.(*model.Error)
	if !ok || appErr.Code != model.CodeFlagConflict || appErr.ExitCode() != model.ExitUsage {
		t.Fatalf("error = %v, want %s with exit code 2", err, model.CodeFlagConflict)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, "index.db")); !os.IsNotExist(statErr) {
		t.Error("the conflicting request must not create a database")
	}
}

// TestIndexRejectsMissingRoot 驗證根目錄不可用時不建立資料庫。
func TestIndexRejectsMissingRoot(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	missing := filepath.Join(t.TempDir(), "absent")
	err := runIndexCommand(t, service, context.Background(), missing, indexOptions())
	appErr, ok := err.(*model.Error)
	if !ok || appErr.Code != model.CodeRootUnavailable {
		t.Fatalf("error = %v, want %s", err, model.CodeRootUnavailable)
	}
	if _, statErr := os.Stat(filepath.Join(missing, "index.db")); !os.IsNotExist(statErr) {
		t.Error("an unusable root must not produce an empty index")
	}
}

// TestIndexCustomDatabasePath 驗證 --db 可把資料庫放在掃描根目錄之外。
func TestIndexCustomDatabasePath(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "a.txt", "a")
	dbPath := filepath.Join(t.TempDir(), "custom.db")
	options := indexOptions()
	options.DB = dbPath
	if err := runIndexCommand(t, service, context.Background(), rootDir, options); err != nil {
		t.Fatalf("index: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("custom database was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, "index.db")); !os.IsNotExist(err) {
		t.Error("the default database must not be created when --db is given")
	}
}

// TestIndexCancelWhileWaitingForLock 驗證等鎖期間取消：不建資料庫、不發佈。
func TestIndexCancelWhileWaitingForLock(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "a.txt", "a")
	dbPath := filepath.Join(rootDir, "index.db")

	holder, err := store.AcquireLock(context.Background(), dbPath, store.LockOptions{
		Timeout: 0,
		RunID:   "test-holder",
	})
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer func() { _ = holder.Release() }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	options := indexOptions()
	options.LockTimeout = 30 * time.Second
	err = runIndexCommand(t, service, ctx, rootDir, options)
	cancel()
	if err == nil {
		t.Fatal("cancelling while waiting for the lock must fail the run")
	}
	if code := model.ExitCodeOf(err); code != model.ExitInterrupted {
		t.Errorf("exit code = %d, want %d (error %v)", code, model.ExitInterrupted, err)
	}
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Error("a run cancelled before opening the database must not create it")
	}
}

// TestIndexReportsDatabaseWriteFailure 驗證資料庫寫入失敗被正確回報且不留發佈結果。
func TestIndexReportsDatabaseWriteFailure(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "a.txt", "a")
	dbPath := filepath.Join(rootDir, "index.db")

	ctx := context.Background()
	// 先建立一個合法但未發佈的資料庫，才有檔案可以上鎖。
	run, err := BeginRun(ctx, deps, RunSpec{
		DBPath:      dbPath,
		Root:        model.RootSpec{AbsPath: rootDir, SourcePlatform: "windows", CasePolicy: model.CaseInsensitive},
		Params:      ScanParams{SHA256: true},
		LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if abandonErr := run.Abandon(ctx, AbandonCause{Interrupted: true}); abandonErr != nil {
		t.Fatalf("abandon: %v", abandonErr)
	}
	if closeErr := run.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	// 以另一個連線持有寫入交易，模擬「資料庫目前不能寫入」。
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()

	err = runIndexCommand(t, service, ctx, rootDir, indexOptions())
	if err == nil {
		t.Fatal("a database write failure must be reported")
	}
	if code := model.ExitCodeOf(err); code != model.ExitFatal {
		t.Errorf("exit code = %d, want %d (error %v)", code, model.ExitFatal, err)
	}
	if appErr, ok := err.(*model.Error); ok && appErr.Code != model.CodeDatabaseIO &&
		appErr.Code != model.CodeDatabaseLocked {
		t.Errorf("error code = %s, want a database write failure", appErr.Code)
	}
}

// TestIndexReusesIncompleteDatabase 驗證被取消的首次建立可以重跑。
//
// 未發佈的資料庫沒有已發佈代次，因此不是「既有索引」：重跑等同重新建立，
// 不覆蓋任何已發佈結果。
func TestIndexReusesIncompleteDatabase(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	service := New(deps)
	rootDir := t.TempDir()
	writeSample(t, rootDir, "a.txt", "a")
	dbPath := filepath.Join(rootDir, "index.db")
	ctx := context.Background()

	run, err := BeginRun(ctx, deps, RunSpec{
		DBPath:      dbPath,
		Root:        model.RootSpec{AbsPath: rootDir, SourcePlatform: "windows", CasePolicy: model.CaseInsensitive},
		Params:      ScanParams{SHA256: true},
		LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if abandonErr := run.Abandon(ctx, AbandonCause{Interrupted: true}); abandonErr != nil {
		t.Fatalf("abandon: %v", abandonErr)
	}
	if closeErr := run.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	if err := runIndexCommand(t, service, ctx, rootDir, indexOptions()); err != nil {
		t.Fatalf("index over an unpublished database: %v", err)
	}
	storage, err := store.Open(ctx, dbPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = storage.Close() }()
	generation, err := storage.PublishedGeneration(ctx)
	if err != nil || generation != 1 {
		t.Fatalf("published generation = %d (err %v), want 1", generation, err)
	}
}

// TestIndexReportsPartialWhenContentUnreadable 驗證摘要失敗的部分完成狀態。
func TestIndexReportsPartialWhenContentUnreadable(t *testing.T) {
	lockedPath := filepath.Join(t.TempDir(), "probe.bin")
	writeSample(t, filepath.Dir(lockedPath), "probe.bin", "probe")
	release, ok := holdExclusiveFile(t, lockedPath)
	if !ok {
		t.Skip("this platform cannot hold a file exclusively")
	}
	defer release()

	deps, _, stderr := newTestDeps(t)
	service := New(deps)
	rootDir := filepath.Dir(lockedPath)
	writeSample(t, rootDir, "ok.txt", "fine")

	ctx := context.Background()
	err := runIndexCommand(t, service, ctx, rootDir, indexOptions())
	if err == nil {
		t.Fatal("an unreadable file must produce a partial result, not success")
	}
	if code := model.ExitCodeOf(err); code != model.ExitPartial {
		t.Fatalf("exit code = %d, want %d (error %v)", code, model.ExitPartial, err)
	}
	if !strings.Contains(stderr.String(), "finished") && !strings.Contains(stderr.String(), "完成") {
		t.Logf("stderr: %s", stderr.String())
	}

	storage, openErr := store.Open(ctx, filepath.Join(rootDir, "index.db"), store.Options{ReadOnly: true})
	if openErr != nil {
		t.Fatalf("open index: %v", openErr)
	}
	defer func() { _ = storage.Close() }()
	bound, _, err := storage.Root(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	entry, found, err := storage.PublishedEntry(ctx, bound.ID, model.PathKey("probe.bin", bound.CasePolicy))
	if err != nil || !found {
		t.Fatalf("published entry: %v (found %v)", err, found)
	}
	if entry.HashState != model.HashStateFailed || entry.HashSHA256 != nil {
		t.Errorf("hash state = %s digest = %v, want failed and empty", entry.HashState, entry.HashSHA256)
	}
	if entry.ErrorCode == nil || *entry.ErrorCode != string(model.CodeHashRead) {
		t.Errorf("error code = %v, want %s", entry.ErrorCode, model.CodeHashRead)
	}
	latest, _, err := storage.LatestRun(ctx)
	if err != nil {
		t.Fatalf("latest run: %v", err)
	}
	if latest.Coverage != model.CoveragePartial {
		t.Errorf("coverage = %s, want partial", latest.Coverage)
	}
	if latest.CoverageJSON == nil || !strings.Contains(*latest.CoverageJSON, "probe.bin") {
		t.Errorf("coverage JSON does not name the failing entry: %v", latest.CoverageJSON)
	}
	if latest.Counts.Failed != 1 || latest.Counts.ErrorEvents != 1 {
		t.Errorf("counts = failed %d, error events %d", latest.Counts.Failed, latest.Counts.ErrorEvents)
	}
}

// TestAbandonRecordsStatusAfterCancellation 驗證取消後仍能寫下收尾狀態。
//
// 迴歸來源：收尾交易原本沿用同一個（已取消的）context，
// 於是「已中斷」這筆紀錄寫不進去，資料庫永遠留下 status=running 的執行列。
func TestAbandonRecordsStatusAfterCancellation(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	ctx, cancel := context.WithCancel(context.Background())
	run, err := BeginRun(ctx, deps, RunSpec{
		DBPath:      dbPath,
		Root:        model.RootSpec{AbsPath: dir, SourcePlatform: "windows", CasePolicy: model.CaseInsensitive},
		Params:      ScanParams{SHA256: true},
		LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if err := run.Stage(ctx, []model.Entry{testEntry(run.ID(), "a.txt", 1)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	cancel()
	if err := run.Abandon(ctx, AbandonCause{Interrupted: true, Err: context.Canceled}); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if err := run.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	storage, err := store.Open(context.Background(), dbPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = storage.Close() }()
	latest, found, err := storage.LatestRun(context.Background())
	if err != nil || !found {
		t.Fatalf("latest run: %v (found %v)", err, found)
	}
	if latest.Status != model.RunStatusInterrupted {
		t.Errorf("run status = %s, want %s", latest.Status, model.RunStatusInterrupted)
	}
	generation, err := storage.PublishedGeneration(context.Background())
	if err != nil || generation != 0 {
		t.Errorf("published generation = %d (err %v), want none", generation, err)
	}
}
