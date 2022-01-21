//go:build !windows

package fsmeta

import (
	"syscall"
	"testing"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// TestCollectFIFODoesNotBlock 檢查特殊檔只讀中繼資料：建立一個沒有寫入端的 FIFO，
// 採集中繼資料必須立刻回傳，不能因為開啟它而阻塞。
func TestCollectFIFODoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	fifo := dir + "/pipe"
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo not available: %v", err)
	}
	type outcome struct {
		info Info
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		info, err := Collect(fifo)
		done <- outcome{info: info, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Collect fifo: %v", got.err)
		}
		if got.info.Type != model.EntryTypeSpecial {
			t.Fatalf("fifo type = %q, want special", got.info.Type)
		}
		if got.info.Size != nil {
			t.Fatalf("special file must not report a size")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Collect blocked on a FIFO without a writer")
	}
}
