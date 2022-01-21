//go:build !windows

package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// TestWalkNonUTF8Name 驗證 Unix 非 UTF-8 名稱無損保存。
//
// 這是 Windows 無法表達的樣本：位元組 0xFF 在 UTF-8 中不合法，
// 但 ext4/APFS 允許。索引必須保留原始位元組，顯示時才轉義。
func TestWalkNonUTF8Name(t *testing.T) {
	root := t.TempDir()
	rawName := "invalid-\xff-'name'.txt"
	if err := os.WriteFile(filepath.Join(root, rawName), []byte("x"), 0o644); err != nil {
		t.Skipf("filesystem rejects non-UTF-8 names: %v", err)
	}
	collected := runWalk(t, Options{Root: root, CasePolicy: model.CaseSensitive, RunID: "run-nonutf8"}, context.Background())
	if collected.err != nil {
		t.Fatalf("Walk: %v", collected.err)
	}
	entry, ok := collected.entries[rawName]
	if !ok {
		t.Fatalf("non-UTF-8 name was not indexed losslessly; got %v", sortedKeys(collected.entries))
	}
	if entry.RelPath != rawName || entry.PathKey != rawName || entry.Name != rawName {
		t.Fatalf("name bytes changed: rel=%q key=%q name=%q", entry.RelPath, entry.PathKey, entry.Name)
	}
	display := model.EscapeDisplay(entry.RelPath)
	if display == entry.RelPath {
		t.Fatal("display form should escape invalid UTF-8")
	}
	restored, err := model.UnescapeDisplay(display)
	if err != nil {
		t.Fatalf("UnescapeDisplay: %v", err)
	}
	if restored != rawName {
		t.Fatalf("display escaping is not reversible: %q", restored)
	}
	if strings.ContainsRune(display, 0xff) {
		t.Fatal("display form must not carry the raw invalid byte")
	}
}

// TestWalkUnreadableDirectory 用真實權限拒絕驗證「列舉失敗不等於刪除」。
func TestWalkUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission denial cannot be constructed")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	mustWrite(t, filepath.Join(blocked, "hidden.txt"), "x")
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Skipf("chmod failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	collected := runWalk(t, Options{Root: root, CasePolicy: model.CaseSensitive, RunID: "run-perm"}, context.Background())
	if collected.err != nil {
		t.Fatalf("Walk: %v", collected.err)
	}
	if _, ok := collected.entries["blocked"]; !ok {
		t.Fatal("the blocked directory itself should still be indexed")
	}
	if _, ok := collected.entries["blocked/hidden.txt"]; ok {
		t.Fatal("entries below an unreadable directory must not be indexed")
	}
	if collected.result.DirsFailed != 1 {
		t.Fatalf("dirs failed = %d, want 1", collected.result.DirsFailed)
	}
	if len(collected.result.Coverage.FailedDirs) != 1 {
		t.Fatalf("coverage gaps = %+v", collected.result.Coverage.FailedDirs)
	}
	if collected.result.Coverage.FailedDirs[0].RelPath != "blocked" {
		t.Fatalf("gap rel path = %q", collected.result.Coverage.FailedDirs[0].RelPath)
	}
	if collected.result.Coverage.Complete() {
		t.Fatal("coverage with an unreadable directory must not be complete")
	}
}

// TestWalkFIFO 驗證特殊檔只記中繼資料、不被開啟讀取。
func TestWalkFIFO(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := mkfifo(fifo); err != nil {
		t.Skipf("mkfifo not available: %v", err)
	}
	collected := runWalk(t, Options{Root: root, CasePolicy: model.CaseSensitive, RunID: "run-fifo"}, context.Background())
	if collected.err != nil {
		t.Fatalf("Walk: %v", collected.err)
	}
	entry, ok := collected.entries["pipe"]
	if !ok {
		t.Fatal("FIFO should be indexed as an entry")
	}
	if entry.Type != model.EntryTypeSpecial {
		t.Fatalf("fifo type = %q, want special", entry.Type)
	}
	if entry.Size != nil {
		t.Fatal("special file must not report a size")
	}
}
