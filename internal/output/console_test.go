package output

import (
	"bytes"
	"strings"
	"testing"
)

// TestStreamSeparation 確認結果走標準輸出、診斷走標準錯誤。
func TestStreamSeparation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	console := New(&stdout, &stderr)
	console.Configure(false, false, true)

	console.Result("a/path")
	console.Note("progress")
	console.Error("problem")

	if got := stdout.String(); got != "a/path\n" {
		t.Errorf("stdout = %q, want only the result line", got)
	}
	if got := stderr.String(); got != "progress\nproblem\n" {
		t.Errorf("stderr = %q, want progress and error lines", got)
	}
}

// TestQuietSuppressesNotesOnly 確認 --quiet 只關閉一般訊息。
func TestQuietSuppressesNotesOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	console := New(&stdout, &stderr)
	console.Configure(true, false, true)

	console.Note("progress")
	console.Error("still here")

	if got := stderr.String(); got != "still here\n" {
		t.Errorf("stderr = %q, want only the error", got)
	}
	if console.ProgressEnabled() {
		t.Error("--quiet must disable regular progress")
	}
}

// TestVerboseOutput 確認逐路徑記錄只在 --verbose 時輸出。
func TestVerboseOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	console := New(&stdout, &stderr)
	console.Configure(false, false, true)
	console.Verbosef("add %s", "x")
	if stderr.Len() != 0 {
		t.Errorf("verbose output leaked while disabled: %q", stderr.String())
	}

	stderr.Reset()
	console.Configure(false, true, false)
	console.Verbosef("add %s", "x")
	if !strings.Contains(stderr.String(), "add x") {
		t.Errorf("verbose output missing: %q", stderr.String())
	}
	if !console.VerboseEnabled() {
		t.Error("VerboseEnabled must report the configured state")
	}
}
