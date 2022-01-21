package fsmeta

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// writeFile 建立一個測試檔案並回傳內容長度。
func writeFile(t *testing.T, path string, size int) int64 {
	t.Helper()
	content := bytes.Repeat([]byte("x"), size)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return int64(len(content))
}

// TestCollectRegularFile 用真實檔案檢查基本欄位。
func TestCollectRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.bin")
	wantSize := writeFile(t, path, 4321)

	info, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if info.Type != model.EntryTypeFile {
		t.Fatalf("type = %q, want file", info.Type)
	}
	if info.Size == nil || *info.Size != wantSize {
		t.Fatalf("size = %v, want %d", info.Size, wantSize)
	}
	if info.Mode == nil || !os.FileMode(*info.Mode).IsRegular() {
		t.Fatalf("mode = %v, want regular file bits", info.Mode)
	}
	if info.Mtime == nil || info.Atime == nil || info.Ctime == nil {
		t.Fatalf("missing timestamps: mtime=%v atime=%v ctime=%v", info.Mtime, info.Atime, info.Ctime)
	}
	if info.Incomplete {
		t.Fatalf("incomplete collection: %s", info.IncompleteReason)
	}
	if info.LinkState != LinkStateNone || info.LinkTarget != nil {
		t.Fatalf("plain file should not be a link: state=%q target=%v", info.LinkState, info.LinkTarget)
	}
	if _, ok := IdentityOf(info); !ok {
		t.Fatal("identity should be available for a regular file")
	}
	if _, ok := VolumeOf(info); !ok {
		t.Fatal("volume identity should be available")
	}
	if info.Btime == nil {
		// Windows 與 macOS 的 lstat 都提供建立時間；Linux 需要 statx，
		// 舊核心或受限的容器可能沒有，屬於已記錄的能力限制。
		t.Logf("birth time unavailable on this platform (%s)", osName())
	}
}

// TestCollectDirectory 檢查目錄沒有大小、類型正確。
func TestCollectDirectory(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "sub")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	info, err := Collect(child)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("type = %q, want dir", info.Type)
	}
	if info.Size != nil {
		t.Fatalf("directory must not report a size, got %d", *info.Size)
	}
}

// TestCollectHardLinkIdentity 檢查兩個硬連結路徑指向同一個身份。
func TestCollectHardLinkIdentity(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	writeFile(t, first, 16)
	second := filepath.Join(dir, "second.txt")
	if err := os.Link(first, second); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	left, err := Collect(first)
	if err != nil {
		t.Fatalf("Collect first: %v", err)
	}
	right, err := Collect(second)
	if err != nil {
		t.Fatalf("Collect second: %v", err)
	}
	if !SameIdentity(left, right) {
		t.Fatalf("hard links should share an identity: %v vs %v", left.Inode, right.Inode)
	}
	if left.Nlink == nil || *left.Nlink < 2 {
		t.Fatalf("link count = %v, want >= 2", left.Nlink)
	}
	other := filepath.Join(dir, "other.txt")
	writeFile(t, other, 16)
	third, err := Collect(other)
	if err != nil {
		t.Fatalf("Collect other: %v", err)
	}
	if SameIdentity(left, third) {
		t.Fatal("different files must not share an identity")
	}
}

// TestCollectSymlinkAndBrokenLink 檢查連結本身與斷鏈的處理。
func TestCollectSymlinkAndBrokenLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	writeFile(t, target, 8)
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not permitted here: %v", err)
	}
	info, err := Collect(link)
	if err != nil {
		t.Fatalf("Collect link: %v", err)
	}
	if info.Type != model.EntryTypeSymlink {
		t.Fatalf("type = %q, want symlink", info.Type)
	}
	if info.LinkTarget == nil || *info.LinkTarget != target {
		t.Fatalf("target = %v, want %q", info.LinkTarget, target)
	}
	if info.LinkState != LinkStateResolved {
		t.Fatalf("state = %q, want resolved", info.LinkState)
	}
	if info.Size != nil {
		t.Fatalf("symlink must not report the target size, got %d", *info.Size)
	}

	broken := filepath.Join(dir, "broken.txt")
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), broken); err != nil {
		t.Skipf("symlinks are not permitted here: %v", err)
	}
	brokenInfo, err := Collect(broken)
	if err != nil {
		t.Fatalf("Collect broken link: %v", err)
	}
	if brokenInfo.Type != model.EntryTypeSymlink {
		t.Fatalf("broken link type = %q, want symlink", brokenInfo.Type)
	}
	if brokenInfo.LinkState != LinkStateDangling {
		t.Fatalf("broken link state = %q, want dangling", brokenInfo.LinkState)
	}
	if brokenInfo.LinkTarget == nil {
		t.Fatal("broken link should still report its target text")
	}
}

// TestProbeCasePolicyMatchesReality 檢查探測結果與檔案系統的實際行為一致。
func TestProbeCasePolicyMatchesReality(t *testing.T) {
	// t.TempDir 的最後一段可能只有數字，這裡自己建一個含 ASCII 字母的目錄，
	// 讓「反轉大小寫」的探測有確定可用的段落。
	dir := filepath.Join(t.TempDir(), "CaseProbeDir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	base := filepath.Base(dir)
	upper := strings.ToUpper(base)
	if upper == base {
		t.Skip("temporary directory name has no ASCII letters to toggle")
	}
	policy, probed, err := ProbeCasePolicy(dir)
	if err != nil {
		t.Fatalf("ProbeCasePolicy: %v", err)
	}
	if !probed {
		t.Skip("case policy could not be probed in this environment")
	}
	_, statErr := os.Stat(filepath.Join(filepath.Dir(dir), upper))
	wantInsensitive := statErr == nil
	gotInsensitive := policy == model.CaseInsensitive
	if gotInsensitive != wantInsensitive {
		t.Fatalf("policy = %q, but toggled-case path resolves = %v", policy, wantInsensitive)
	}
}

// TestRootIdentity 檢查根目錄身份可被採集。
func TestRootIdentity(t *testing.T) {
	dir := t.TempDir()
	volume, fileID, err := RootIdentity(dir)
	if err != nil {
		t.Fatalf("RootIdentity: %v", err)
	}
	if volume == nil || *volume == "" {
		t.Fatal("volume identity should be available for a temporary directory")
	}
	if fileID == nil || *fileID == "" {
		t.Fatal("file identity should be available for a temporary directory")
	}
}

// TestCollectMissingPath 檢查不存在的路徑回報錯誤而不是造出假條目。
func TestCollectMissingPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := Collect(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("Collect should fail for a missing path")
	}
}

// osName 回傳目前作業系統名稱，僅用於測試訊息。
func osName() string {
	return os.Getenv("OS")
}
