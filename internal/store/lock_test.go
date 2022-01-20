package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔驗證正式目標的跨行程寫入鎖：互斥、逾時、可取消，
// 以及持有鎖的行程結束後鎖會被作業系統釋放且舊索引保持完整。
//
// 跨行程測試以「重新執行同一個測試二進位」的方式建立真正的第二個行程，
// 全程有牆鐘上限，結束後殺掉並回收子行程。

// lockHelperEnv 是子行程的環境變數鍵，值是資料庫路徑。
const lockHelperEnv = "EVERNIGHTINDEX_LOCK_HELPER_DB"

// TestLockHelperProcess 只在被父測試以子行程方式啟動時執行。
func TestLockHelperProcess(t *testing.T) {
	path := os.Getenv(lockHelperEnv)
	if path == "" {
		t.Skip("helper process for lock tests")
	}
	lock, err := AcquireLock(context.Background(), path, LockOptions{Timeout: 10 * time.Second, RunID: "helper-process"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper could not acquire lock: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = lock.Release() }()
	fmt.Println("locked")
	time.Sleep(30 * time.Second)
}

// TestLockExcludesSecondWriter 驗證同一個目標同時只能有一個寫入者。
func TestLockExcludesSecondWriter(t *testing.T) {
	_, path := newStore(t)
	ctx := context.Background()
	first, err := AcquireLock(ctx, path, LockOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	_, err = AcquireLock(ctx, path, LockOptions{Timeout: 0})
	requireCode(t, err, model.CodeDatabaseLocked)

	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := AcquireLock(ctx, path, LockOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release second: %v", err)
	}
}

// TestLockWaitIsCancellable 驗證等待鎖可被 context 取消，且回報中斷錯誤碼。
func TestLockWaitIsCancellable(t *testing.T) {
	_, path := newStore(t)
	holder, err := AcquireLock(context.Background(), path, LockOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	defer func() { _ = holder.Release() }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = AcquireLock(ctx, path, LockOptions{Timeout: 10 * time.Second, Poll: 10 * time.Millisecond})
	requireCode(t, err, model.CodeInterrupted)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("cancelled wait took %s; cancellation must not depend on the lock timeout", elapsed)
	}
}

// TestLockTimeoutReportsOwner 驗證逾時會通知上層並附上持有者資訊。
func TestLockTimeoutReportsOwner(t *testing.T) {
	_, path := newStore(t)
	holder, err := AcquireLock(context.Background(), path, LockOptions{Timeout: time.Second, RunID: "holder-run"})
	if err != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	defer func() { _ = holder.Release() }()

	notified := false
	_, err = AcquireLock(context.Background(), path, LockOptions{
		Timeout: 200 * time.Millisecond,
		Poll:    20 * time.Millisecond,
		Notify:  func(time.Duration) { notified = true },
	})
	requireCode(t, err, model.CodeDatabaseLocked)
	if !notified {
		t.Error("waiting for the lock must notify the caller once")
	}
	var appErr *model.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *model.Error, got %T", err)
	}
	if owner := appErr.Args["owner"]; !strings.Contains(owner, "holder-run") {
		t.Errorf("owner = %q, want the holding run id", owner)
	}
}

// TestLockHeldByAnotherProcess 驗證跨行程互斥，以及持有者結束後鎖會釋放。
func TestLockHeldByAnotherProcess(t *testing.T) {
	store, path := newStore(t)
	ctx := context.Background()
	bindTestRoot(t, store, "C:/root")
	const runID = "run-before-contention"
	startTestRun(t, store, runID, 0)
	if err := store.StageEntries(ctx, runID, []model.Entry{sampleEntry(runID, "old.txt", 4)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := store.Publish(ctx, PublishRequest{RunID: runID, ExpectedGeneration: 0, Coverage: model.CoverageComplete}); err != nil {
		t.Fatalf("publish baseline: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	finished := false
	defer func() {
		if finished || cmd.Process == nil {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// 等到子行程明確回報已持鎖，避免依賴固定睡眠時間。
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if !strings.Contains(line, "locked") {
			t.Fatalf("helper did not report holding the lock: %q", line)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("helper did not report holding the lock in time")
	}

	_, err = AcquireLock(ctx, path, LockOptions{Timeout: 300 * time.Millisecond, Poll: 20 * time.Millisecond})
	requireCode(t, err, model.CodeDatabaseLocked)

	// 結束持鎖行程：作業系統必須釋放檔案鎖。
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait()
	finished = true

	lock, err := AcquireLock(ctx, path, LockOptions{Timeout: 10 * time.Second, Poll: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("lock must be released when the holder process ends: %v", err)
	}
	defer func() { _ = lock.Release() }()

	reopened, err := OpenTarget(ctx, path, Options{Create: true, BusyTimeout: time.Second}, lock)
	if err != nil {
		t.Fatalf("reopen target: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	generation, err := reopened.PublishedGeneration(ctx)
	if err != nil || generation != 1 {
		t.Fatalf("published generation = %d, err = %v; the previous index must stay intact", generation, err)
	}
	if total, err := reopened.CountPublished(ctx, 1); err != nil || total != 1 {
		t.Fatalf("published entries = %d, err = %v; want 1", total, err)
	}
	if err := reopened.IntegrityCheck(ctx); err != nil {
		t.Fatalf("integrity after contention: %v", err)
	}
}

// TestOpenTargetRequiresMatchingLock 驗證寫入開啟必須持有同一目標的鎖。
func TestOpenTargetRequiresMatchingLock(t *testing.T) {
	store, path := newStore(t)
	ctx := context.Background()
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err := OpenTarget(ctx, path, Options{Create: true}, nil)
	requireCode(t, err, model.CodeInternal)

	lock, err := AcquireLock(ctx, path, LockOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	other := filepath.Join(t.TempDir(), "other.db")
	_, err = OpenTarget(ctx, other, Options{Create: true}, lock)
	requireCode(t, err, model.CodeInternal)

	target, err := OpenTarget(ctx, path, Options{Create: true, BusyTimeout: time.Second}, lock)
	if err != nil {
		t.Fatalf("open target with matching lock: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close target: %v", err)
	}
}
