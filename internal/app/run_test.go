package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/i18n"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
	"github.com/kagurazakayashi/EvernightIndex/internal/store"
)

// 本檔用合成條目驗證執行生命週期：發佈、取消、提交後才抵達的取消，
// 以及正式目標鎖的互斥與釋放。不接觸任何使用者資料。

// newTestDeps 建立帶記憶體輸出的依賴，並開啟 verbose 與每秒節拍。
func newTestDeps(t *testing.T) (Deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	console := output.New(&stdout, &stderr)
	console.Configure(false, true, true)
	return Deps{Console: console, Catalog: i18n.MustDefault(), Lang: i18n.LangZhCN}, &stdout, &stderr
}

// newTestRunSpec 建立指向測試專屬目錄的執行規格。
func newTestRunSpec(t *testing.T) RunSpec {
	t.Helper()
	dir := t.TempDir()
	return RunSpec{
		DBPath:      filepath.Join(dir, "index.db"),
		Root:        model.RootSpec{AbsPath: dir, SourcePlatform: "windows", CasePolicy: model.CaseInsensitive},
		Params:      ScanParams{SHA256: true, ReadRetries: 1, Excludes: []string{"*.tmp"}},
		LockTimeout: 2 * time.Second,
	}
}

// testEntry 建立一筆合成條目。
func testEntry(runID, relPath string, size int64) model.Entry {
	stamp := model.NowTimestamp()
	ext := filepath.Ext(relPath)
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

// TestRunPublishLifecycle 驗證完整生命週期：發佈、統計與鎖釋放。
func TestRunPublishLifecycle(t *testing.T) {
	deps, stdout, stderr := newTestDeps(t)
	spec := newTestRunSpec(t)
	ctx := context.Background()

	run, err := BeginRun(ctx, deps, spec)
	if err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if run.Mode() != model.RunModeCreate || run.Baseline() != 0 {
		t.Errorf("mode/baseline = %s/%d, want create/0", run.Mode(), run.Baseline())
	}
	if err := run.Stage(ctx, []model.Entry{testEntry(run.ID(), "a.txt", 3), testEntry(run.ID(), "b.txt", 4)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	run.Stats().AddDiscovered(2)
	run.Stats().AddProcessed(2)
	run.Stats().AddSucceeded(2)

	published, err := run.Publish(ctx, Publication{Coverage: model.CoverageComplete})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Generation != 1 || published.Entries != 2 {
		t.Errorf("published = %+v, want generation 1 with 2 entries", published)
	}
	if exitErr := published.ExitError(); exitErr != nil {
		t.Errorf("a complete publication must not report partial failure: %v", exitErr)
	}
	if err := run.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if stdout.Len() != 0 {
		t.Errorf("progress output must go to stderr, stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "已发布代次 1") {
		t.Errorf("final report is missing from stderr: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "完成") {
		t.Errorf("final statistics are missing from stderr: %q", stderr.String())
	}

	// 關閉之後鎖必須釋放，讓下一個執行能接手。
	lock, err := store.AcquireLock(ctx, spec.DBPath, store.LockOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("lock must be released after Close: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
}

// TestRunAbandonInterrupted 驗證取消不會發佈，且記錄中斷狀態。
func TestRunAbandonInterrupted(t *testing.T) {
	deps, _, stderr := newTestDeps(t)
	spec := newTestRunSpec(t)
	ctx := context.Background()

	run, err := BeginRun(ctx, deps, spec)
	if err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if err := run.Stage(ctx, []model.Entry{testEntry(run.ID(), "partial.txt", 1)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	run.Stats().AddProcessed(1)
	if err := run.Abandon(ctx, AbandonCause{Interrupted: true, Coverage: model.CoverageUnknown}); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	runID := run.ID()
	if err := run.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(stderr.String(), "已中断") {
		t.Errorf("interrupted summary is missing: %q", stderr.String())
	}

	reopened, err := store.Open(ctx, spec.DBPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	generation, err := reopened.PublishedGeneration(ctx)
	if err != nil || generation != 0 {
		t.Fatalf("generation = %d, err = %v; an interrupted first run must not publish", generation, err)
	}
	if total, err := reopened.CountPublished(ctx, 1); err != nil || total != 0 {
		t.Fatalf("published entries = %d, err = %v; want 0", total, err)
	}
	recorded, found, err := reopened.Run(ctx, runID)
	if err != nil || !found {
		t.Fatalf("read run: found=%v err=%v", found, err)
	}
	if recorded.Status != model.RunStatusInterrupted {
		t.Errorf("run status = %s, want interrupted", recorded.Status)
	}
}

// TestRunCancelAfterCommitIsNotRolledBack 驗證提交後才收到的取消不改寫狀態。
func TestRunCancelAfterCommitIsNotRolledBack(t *testing.T) {
	deps, _, stderr := newTestDeps(t)
	spec := newTestRunSpec(t)
	ctx := context.Background()

	run, err := BeginRun(ctx, deps, spec)
	if err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if err := run.Stage(ctx, []model.Entry{testEntry(run.ID(), "committed.txt", 2)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := run.Publish(ctx, Publication{Coverage: model.CoverageComplete}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := run.Abandon(ctx, AbandonCause{Interrupted: true, Err: context.Canceled}); err != nil {
		t.Fatalf("abandon after commit: %v", err)
	}
	if err := run.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(stderr.String(), "提交之后到达") {
		t.Errorf("late interrupt must be reported explicitly: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "已中断") {
		t.Errorf("a committed run must not be reported as interrupted: %q", stderr.String())
	}

	reopened, err := store.Open(ctx, spec.DBPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if generation, err := reopened.PublishedGeneration(ctx); err != nil || generation != 1 {
		t.Fatalf("generation = %d, err = %v; want the committed generation", generation, err)
	}
}

// TestPublishBarrierAbortsPublish 驗證發佈前協調失敗不會改變舊代次。
func TestPublishBarrierAbortsPublish(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	spec := newTestRunSpec(t)
	ctx := context.Background()

	first, err := BeginRun(ctx, deps, spec)
	if err != nil {
		t.Fatalf("begin first run: %v", err)
	}
	if err := first.Stage(ctx, []model.Entry{testEntry(first.ID(), "kept.txt", 3)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := first.Publish(ctx, Publication{Coverage: model.CoverageComplete}); err != nil {
		t.Fatalf("publish baseline: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := BeginRun(ctx, deps, spec)
	if err != nil {
		t.Fatalf("begin second run: %v", err)
	}
	if err := second.Stage(ctx, []model.Entry{testEntry(second.ID(), "blocked.txt", 4)}); err != nil {
		t.Fatalf("stage second: %v", err)
	}
	sentinel := errors.New("baseline changed")
	_, err = second.Publish(ctx, Publication{
		Coverage:      model.CoverageComplete,
		BeforePublish: func(context.Context) error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("publish must fail with the barrier error, got %v", err)
	}
	if err := second.Abandon(ctx, AbandonCause{Err: sentinel, PublishFailed: true, Coverage: model.CoverageUnknown}); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}

	reopened, err := store.Open(ctx, spec.DBPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if generation, err := reopened.PublishedGeneration(ctx); err != nil || generation != 1 {
		t.Fatalf("generation = %d, err = %v; want the previous generation", generation, err)
	}
	if total, err := reopened.CountPublished(ctx, 1); err != nil || total != 1 {
		t.Fatalf("published entries = %d, err = %v; want 1", total, err)
	}
}

// TestRunLockContention 驗證同一正式資料庫的第二次執行會被鎖擋下。
func TestRunLockContention(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	spec := newTestRunSpec(t)
	ctx := context.Background()

	first, err := BeginRun(ctx, deps, spec)
	if err != nil {
		t.Fatalf("begin first run: %v", err)
	}
	contended := spec
	contended.LockTimeout = 0
	_, err = BeginRun(ctx, deps, contended)
	requireCode(t, err, model.CodeDatabaseLocked)

	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	second, err := BeginRun(ctx, deps, spec)
	if err != nil {
		t.Fatalf("begin after release: %v", err)
	}
	if err := second.Abandon(ctx, AbandonCause{Interrupted: true}); err != nil {
		t.Fatalf("abandon second: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}
}

// TestPublishedExitCodes 驗證部分完成與待匯出對應部分失敗退出碼。
func TestPublishedExitCodes(t *testing.T) {
	if err := (Published{Generation: 1, Partial: true}).ExitError(); model.ExitCodeOf(err) != model.ExitPartial {
		t.Errorf("partial publication must map to exit code 3, got %v", err)
	}
	if err := (Published{Generation: 1, ExportPending: true}).ExitError(); model.ExitCodeOf(err) != model.ExitPartial {
		t.Errorf("pending export must map to exit code 3, got %v", err)
	}
	if err := (Published{Generation: 1}).ExitError(); err != nil {
		t.Errorf("complete publication must not report an error, got %v", err)
	}
}

// TestRunInfoReportsIncompleteIndex 驗證 info 會指出尚未發佈的空庫。
func TestRunInfoReportsIncompleteIndex(t *testing.T) {
	deps, stdout, _ := newTestDeps(t)
	spec := newTestRunSpec(t)
	ctx := context.Background()

	run, err := BeginRun(ctx, deps, spec)
	if err != nil {
		t.Fatalf("begin run: %v", err)
	}
	if err := run.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	stdout.Reset()
	service := New(deps)
	if err := service.Run(ctx, Request{Command: "info", Options: Options{DB: spec.DBPath}}); err != nil {
		t.Fatalf("info: %v", err)
	}
	report := stdout.String()
	for _, want := range []string{"schema_version: 2", "generation: -", "index_state: incomplete", "entries: 0"} {
		if !strings.Contains(report, want) {
			t.Errorf("info output is missing %q: %s", want, report)
		}
	}
}

// TestReporterUsesUnifiedChannel 驗證逐條操作記錄走標準錯誤，不污染標準輸出。
func TestReporterUsesUnifiedChannel(t *testing.T) {
	deps, stdout, stderr := newTestDeps(t)
	reporter := newReporter(deps, output.NewStats())
	reporter.operation(model.OpAdded, "C:/root/a.txt")
	reporter.operation(model.OpExcluded, "C:/root/b.tmp")
	if stdout.Len() != 0 {
		t.Errorf("verbose operations must not reach stdout: %q", stdout.String())
	}
	for _, want := range []string{"新增 C:/root/a.txt", "排除 C:/root/b.tmp"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr is missing %q: %q", want, stderr.String())
		}
	}
}

// TestQuietSuppressesProgressButNotErrors 驗證 --quiet 只關掉常規進度。
func TestQuietSuppressesProgressButNotErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	console := output.New(&stdout, &stderr)
	console.Configure(true, false, true)
	deps := Deps{Console: console, Catalog: i18n.MustDefault(), Lang: i18n.LangZhCN}
	reporter := newReporter(deps, output.NewStats())
	reporter.summary(output.Snapshot{}, "stats.summary")
	console.Error("boom")
	if strings.Contains(stderr.String(), "完成") {
		t.Errorf("--quiet must suppress the final summary: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("--quiet must never suppress errors: %q", stderr.String())
	}
}

// requireCode 確認錯誤帶有指定錯誤碼。
func requireCode(t *testing.T, err error, want model.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var appErr *model.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *model.Error, got %T: %v", err, err)
	}
	if appErr.Code != want {
		t.Fatalf("code = %s, want %s (%v)", appErr.Code, want, err)
	}
}
