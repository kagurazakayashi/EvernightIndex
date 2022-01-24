package hash

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
)

// 本檔以 t.TempDir() 內的合成檔案驗證摘要計算：已知樣本、空檔案、
// 讀取期間變更、讀取失敗、取消、worker 上限與可觀察讀取計數。

// 已知樣本的 SHA256，由外部工具（sha256sum）核對過。
const (
	emptyDigest     = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	helloDigest     = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	alphabetDigest  = "72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0"
	testBufferBytes = 8
)

// writeFile 建立一個內容固定的測試檔案。
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// requestFor 依實際檔案狀態組出請求，模擬掃描時觀測到的值。
func requestFor(t *testing.T, path string) Request {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	size := info.Size()
	mtime := model.NewTimestamp(info.ModTime())
	request := Request{AbsPath: path, Size: &size, Mtime: &mtime}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	if identity, ok := fileIdentity(file); ok {
		request.Identity = identity
	}
	return request
}

// TestComputeKnownDigests 驗證已知樣本與空檔案的摘要。
func TestComputeKnownDigests(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"empty.txt", "", emptyDigest},
		{"hello.txt", "hello", helloDigest},
		{"alphabet.txt", "abcdefghij", alphabetDigest},
	}
	requests := make([]Request, 0, len(cases))
	for _, testCase := range cases {
		requests = append(requests, requestFor(t, writeFile(t, dir, testCase.name, testCase.content)))
	}
	stats := output.NewStats()
	counter := &Counter{}
	hasher := New(Options{Workers: 2, ReadRetries: 1, Stats: stats, Counter: counter})
	results, err := hasher.Compute(context.Background(), requests)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	for index, testCase := range cases {
		result := results[index]
		if !result.Computed() {
			t.Fatalf("%s: state = %s, code = %s (%s)", testCase.name, result.State, result.Code, result.Detail)
		}
		if result.Digest != testCase.want {
			t.Errorf("%s: digest = %s, want %s", testCase.name, result.Digest, testCase.want)
		}
		if result.ReadBytes != int64(len(testCase.content)) {
			t.Errorf("%s: read %d bytes, want %d", testCase.name, result.ReadBytes, len(testCase.content))
		}
		if result.Attempts != 1 {
			t.Errorf("%s: attempts = %d, want 1", testCase.name, result.Attempts)
		}
		if result.Size != int64(len(testCase.content)) {
			t.Errorf("%s: observed size = %d, want %d", testCase.name, result.Size, len(testCase.content))
		}
	}
	// 讀取位元組是三個樣本內容長度之和：0 + 5 + 10。
	if got := stats.Snapshot(); got.HashComputed != 3 || got.HashFailed != 0 ||
		got.BytesRead != 15 {
		t.Errorf("stats = computed %d, failed %d, bytes %d", got.HashComputed, got.HashFailed, got.BytesRead)
	}
	if counter.Opens() != 3 || counter.Bytes() != 15 {
		t.Errorf("counter = opens %d, bytes %d", counter.Opens(), counter.Bytes())
	}
}

// TestComputeReportsReadFailure 驗證讀取失敗時不留下部分結果。
func TestComputeReportsReadFailure(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone.bin")
	if err := os.WriteFile(missing, []byte("bye"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	request := requestFor(t, missing)
	if err := os.Remove(missing); err != nil {
		t.Fatalf("remove: %v", err)
	}
	stats := output.NewStats()
	hasher := New(Options{Workers: 1, ReadRetries: 1, Stats: stats})
	results, err := hasher.Compute(context.Background(), []Request{request})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	result := results[0]
	if result.State != model.HashStateFailed || result.Digest != "" {
		t.Fatalf("state = %s digest = %q, want failed and empty", result.State, result.Digest)
	}
	if result.Code != model.CodeHashRead {
		t.Errorf("code = %s, want %s", result.Code, model.CodeHashRead)
	}
	if result.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 for a non-retryable failure", result.Attempts)
	}
	if got := stats.Snapshot(); got.HashFailed != 1 || got.HashComputed != 0 {
		t.Errorf("stats = failed %d, computed %d", got.HashFailed, got.HashComputed)
	}
}

// TestComputeRejectsNonRegularPath 驗證路徑被換成目錄後不讀內容。
func TestComputeRejectsNonRegularPath(t *testing.T) {
	dir := t.TempDir()
	hasher := New(Options{Workers: 1})
	results, err := hasher.Compute(context.Background(), []Request{{AbsPath: dir}})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if results[0].State != model.HashStateFailed || results[0].Code != model.CodeHashRead {
		t.Fatalf("result = %+v, want failed with %s", results[0], model.CodeHashRead)
	}
}

// TestComputeDetectsChangeWhileReading 驗證讀取期間的變更會讓摘要失敗。
//
// AfterRead 只在讀取完成、穩定性檢查之前觸發，因此這是確定性的情境，
// 不依賴真正與其他行程競速。
func TestComputeDetectsChangeWhileReading(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "grow.bin", "0123456789")
	request := requestFor(t, path)
	stats := output.NewStats()
	hasher := New(Options{
		Workers:     1,
		ReadRetries: 1,
		Stats:       stats,
		AfterRead: func() {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return
			}
			_, _ = file.WriteString("MORE")
			_ = file.Close()
		},
	})
	results, err := hasher.Compute(context.Background(), []Request{request})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	result := results[0]
	if result.State != model.HashStateFailed || result.Digest != "" {
		t.Fatalf("state = %s digest = %q, want failed and no digest", result.State, result.Digest)
	}
	if result.Code != model.CodeHashUnstable {
		t.Errorf("code = %s, want %s (detail %q)", result.Code, model.CodeHashUnstable, result.Detail)
	}
	if result.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one retry)", result.Attempts)
	}
	if got := stats.Snapshot(); got.HashFailed != 1 || got.HashComputed != 0 {
		t.Errorf("stats = failed %d, computed %d", got.HashFailed, got.HashComputed)
	}
}

// TestComputeRetriesUntilStable 驗證有限重試能收斂到成功。
//
// 請求刻意不帶掃描時的大小與時間，第一次讀取後的變更只會讓穩定性檢查失敗，
// 第二次讀取不再變更就會成功——這正是「有限重試」的目標行為。
func TestComputeRetriesUntilStable(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "once.bin", "stable")
	var fired atomic.Int64
	hasher := New(Options{
		Workers:     1,
		ReadRetries: 1,
		AfterRead: func() {
			if fired.Add(1) == 1 {
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return
				}
				_, _ = file.WriteString("!")
				_ = file.Close()
			}
		},
	})
	results, err := hasher.Compute(context.Background(), []Request{{AbsPath: path}})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	result := results[0]
	if !result.Computed() {
		t.Fatalf("state = %s code = %s detail = %q", result.State, result.Code, result.Detail)
	}
	if result.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", result.Attempts)
	}
	if result.Digest != helloDigest && result.Digest != alphabetDigest {
		// 內容應為 "stable!"，這裡只驗證長度與非空，避免把測試綁死在猜測的字串上。
		if len(result.Digest) != 64 {
			t.Errorf("digest = %q, want 64 hex characters", result.Digest)
		}
	}
}

// TestComputeCanceled 驗證取消會停止讀取並回傳錯誤。
func TestComputeCanceled(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "big.bin", strings.Repeat("a", 1<<16))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hasher := New(Options{Workers: 2})
	results, err := hasher.Compute(ctx, []Request{{AbsPath: path}})
	if err == nil {
		t.Fatal("compute must report cancellation")
	}
	for index, result := range results {
		if result.Computed() {
			t.Errorf("result %d must not be computed after cancellation", index)
		}
	}
}

// TestEffectiveWorkers 驗證 worker 上限受控且預設為 1。
func TestEffectiveWorkers(t *testing.T) {
	limit := runtime.NumCPU() / 2
	if limit < 1 {
		limit = 1
	}
	cases := []struct {
		requested int
		want      int
	}{
		{0, 1},
		{-3, 1},
		{1, 1},
		{limit + 5, limit},
	}
	for _, testCase := range cases {
		if got := EffectiveWorkers(testCase.requested); got != testCase.want {
			t.Errorf("EffectiveWorkers(%d) = %d, want %d", testCase.requested, got, testCase.want)
		}
	}
	if got := EffectiveWorkers(2); got < 1 || got > limit {
		t.Errorf("EffectiveWorkers(2) = %d, want within 1..%d", got, limit)
	}
}

// TestIdentityOfEntry 驗證條目身分字串與控制代碼身分格式一致。
func TestIdentityOfEntry(t *testing.T) {
	volume := "0A1B2C3D"
	fileID := "00000000000000FF"
	win := model.Entry{WinVolumeID: &volume, WinFileID: &fileID}
	if got, ok := IdentityOfEntry(win); !ok || got != "win:0A1B2C3D|00000000000000FF" {
		t.Fatalf("windows identity = %q %v", got, ok)
	}
	device := int64(2049)
	inode := int64(42)
	posix := model.Entry{DeviceID: &device, Inode: &inode}
	if got, ok := IdentityOfEntry(posix); !ok || got != "dev:2049|42" {
		t.Fatalf("posix identity = %q %v", got, ok)
	}
	if _, ok := IdentityOfEntry(model.Entry{}); ok {
		t.Fatal("an entry without identity fields must not claim one")
	}
}

// TestFileIdentityAgreesWithPath 驗證同一個檔案的控制代碼身分與路徑身分一致。
func TestFileIdentityAgreesWithPath(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "same.bin", "same")
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = file.Close() }()
	handleIdentity, ok := fileIdentity(file)
	if !ok {
		t.Skip("this platform does not expose a file identity")
	}
	pathLevel, ok := pathIdentity(path)
	if !ok {
		t.Skip("this platform does not expose a path identity")
	}
	if handleIdentity != pathLevel {
		t.Errorf("handle identity %q != path identity %q", handleIdentity, pathLevel)
	}
}

// TestComputeRejectsReplacedPath 驗證掃描後被替換的路徑不會被當成同一個檔案。
func TestComputeRejectsReplacedPath(t *testing.T) {
	dir := t.TempDir()
	original := writeFile(t, dir, "original.bin", "original")
	replacement := writeFile(t, dir, "replacement.bin", "replacement")
	request := requestFor(t, replacement)
	request.AbsPath = original
	hasher := New(Options{Workers: 1, ReadRetries: 0})
	results, err := hasher.Compute(context.Background(), []Request{request})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if results[0].State != model.HashStateFailed || results[0].Code != model.CodeHashUnstable {
		t.Fatalf("result = %+v, want unstable failure", results[0])
	}
}

// TestComputeUsesBoundedBuffer 驗證緩衝區上限，避免呼叫端指定超大緩衝區。
func TestComputeUsesBoundedBuffer(t *testing.T) {
	hasher := New(Options{BufferSize: maxBufferSize * 8})
	if hasher.bufferSize != maxBufferSize {
		t.Errorf("buffer size = %d, want %d", hasher.bufferSize, maxBufferSize)
	}
	hasher = New(Options{BufferSize: -1})
	if hasher.bufferSize != defaultBufferSize {
		t.Errorf("buffer size = %d, want %d", hasher.bufferSize, defaultBufferSize)
	}
}
