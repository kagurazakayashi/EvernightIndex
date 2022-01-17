package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// 這些測試以記憶體輸出直接呼叫 Main，讓退出碼、訊息語言與檔案副作用
// 都能在不啟動子程序的情況下斷言。

func testBuildInfo() BuildInfo {
	return BuildInfo{
		Version:   "0.1.0-dev",
		Commit:    "abc1234",
		BuildDate: "2026-11-11",
		GoVersion: "go1.26.5",
		GOOS:      "windows",
		GOARCH:    "amd64",
	}
}

// withSystemLanguage 固定系統語言來源，讓語言測試不依賴執行機器。
func withSystemLanguage(t *testing.T, tag string) {
	t.Helper()
	original := systemLanguage
	systemLanguage = func() string { return tag }
	t.Cleanup(func() { systemLanguage = original })
}

type runResult struct {
	stdout string
	stderr string
	code   int
}

func runCLI(t *testing.T, argv []string, env map[string]string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string { return env[key] }
	code := Main(context.Background(), testBuildInfo(), argv, &stdout, &stderr, getenv)
	return runResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func requireEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("directory must stay untouched, found %v", names)
	}
}

// TestHelpDoesNotCreateDatabase 驗證取得說明不會建立資料庫或掃描目錄。
func TestHelpDoesNotCreateDatabase(t *testing.T) {
	withSystemLanguage(t, "")
	dir := t.TempDir()
	cases := [][]string{
		{"--help"},
		{"-h"},
		{"index", dir, "--help"},
		{"index", dir, "-h"},
		{"find", "--help"},
		{"dupes", dir, dir, "--help"},
		{"info", "--help"},
		{"export-log", "--help"},
		{"recover", "--help"},
	}
	for _, argv := range cases {
		result := runCLI(t, argv, nil)
		if result.code != 0 {
			t.Errorf("%v: exit code = %d, want 0 (stderr: %s)", argv, result.code, result.stderr)
		}
		if !strings.Contains(result.stdout, "Usage") {
			t.Errorf("%v: help must be printed to stdout", argv)
		}
	}
	requireEmptyDir(t, dir)
}

// TestVersionDoesNotCreateDatabase 驗證版本輸出與其內容。
func TestVersionDoesNotCreateDatabase(t *testing.T) {
	withSystemLanguage(t, "")
	dir := t.TempDir()
	for _, argv := range [][]string{{"--version"}, {"-V"}, {"index", dir, "--version"}} {
		result := runCLI(t, argv, nil)
		if result.code != 0 {
			t.Errorf("%v: exit code = %d, want 0", argv, result.code)
		}
		for _, want := range []string{"EvernightIndex 0.1.0-dev", "license: MulanPSL-2.0", "author: KagurazakaMiyabi(KagurazakaYashi)"} {
			if !strings.Contains(result.stdout, want) {
				t.Errorf("%v: version output missing %q", argv, want)
			}
		}
	}
	requireEmptyDir(t, dir)
}

// TestHelpDocumentsEveryFlag 驗證說明完整列出宣告的選項與命令。
func TestHelpDocumentsEveryFlag(t *testing.T) {
	withSystemLanguage(t, "")
	program := runCLI(t, []string{"--help"}, nil)
	if program.code != 0 {
		t.Fatalf("program help failed: %d", program.code)
	}
	for _, spec := range globalFlags() {
		if !strings.Contains(program.stdout, "--"+spec.name) {
			t.Errorf("program help is missing --%s", spec.name)
		}
	}
	for _, command := range commandRegistry {
		if !strings.Contains(program.stdout, command.name) {
			t.Errorf("program help is missing command %s", command.name)
		}
	}
	for _, command := range commandRegistry {
		result := runCLI(t, []string{command.name, "--help"}, nil)
		if result.code != 0 {
			t.Fatalf("%s --help failed: %d", command.name, result.code)
		}
		for _, spec := range flagsForCommand(command.name) {
			if !strings.Contains(result.stdout, "--"+spec.name) {
				t.Errorf("%s help is missing --%s", command.name, spec.name)
			}
		}
		if !strings.Contains(result.stdout, command.name) {
			t.Errorf("%s help does not name the command", command.name)
		}
	}
}

// TestNotImplementedCommandsFailLoudly 驗證未實作命令以非零狀態明確失敗。
func TestNotImplementedCommandsFailLoudly(t *testing.T) {
	withSystemLanguage(t, "")
	dir := t.TempDir()
	cases := [][]string{
		{"index", dir},
		{"find"},
		{"dupes", dir, dir},
		{"info"},
		{"export-log"},
		{"recover"},
	}
	for _, argv := range cases {
		result := runCLI(t, argv, nil)
		if result.code == 0 {
			t.Errorf("%v: unimplemented command must not exit 0", argv)
		}
		if result.stdout != "" {
			t.Errorf("%v: unimplemented command must not write results to stdout: %q", argv, result.stdout)
		}
		for _, want := range []string{"E_NOT_IMPLEMENTED", "not implemented"} {
			if !strings.Contains(result.stderr, want) {
				t.Errorf("%v: stderr missing %q: %q", argv, want, result.stderr)
			}
		}
	}
	requireEmptyDir(t, dir)
}

// TestUsageErrors 驗證參數錯誤的退出碼與錯誤碼。
func TestUsageErrors(t *testing.T) {
	withSystemLanguage(t, "")
	dir := t.TempDir()
	cases := []struct {
		name string
		argv []string
		code string
	}{
		{"no command", nil, "E_MISSING_COMMAND"},
		{"unknown command", []string{"indexx"}, "E_UNKNOWN_COMMAND"},
		{"unknown flag", []string{"index", dir, "--bogus"}, "E_UNKNOWN_FLAG"},
		{"deep with sha256 false", []string{"index", dir, "--deep", "--sha256=false"}, "E_FLAG_CONFLICT"},
		{"verify with wrong match", []string{"dupes", dir, dir, "--match=auto", "--verify=sha256"}, "E_FLAG_CONFLICT"},
		{"quiet with verbose", []string{"index", dir, "--quiet", "--verbose"}, "E_FLAG_CONFLICT"},
		{"null with jsonl", []string{"find", "--null", "--format=jsonl"}, "E_FLAG_CONFLICT"},
		{"missing value", []string{"find", "--db"}, "E_FLAG_NEEDS_VALUE"},
		{"clustered short options", []string{"index", dir, "-vq"}, "E_INVALID_VALUE"},
		{"glued short value", []string{"index", dir, "-dpath"}, "E_INVALID_VALUE"},
		{"abbreviated long flag", []string{"index", dir, "--work", "4"}, "E_UNKNOWN_FLAG"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := runCLI(t, testCase.argv, nil)
			if result.code != 2 {
				t.Errorf("%v: exit code = %d, want 2", testCase.argv, result.code)
			}
			if result.stdout != "" {
				t.Errorf("%v: usage errors must not write stdout: %q", testCase.argv, result.stdout)
			}
			if !strings.Contains(result.stderr, testCase.code) {
				t.Errorf("%v: stderr missing %s: %q", testCase.argv, testCase.code, result.stderr)
			}
		})
	}
	requireEmptyDir(t, dir)
}

// TestLanguageSelection 驗證顯式、環境、系統與英語回退的順序。
func TestLanguageSelection(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		system   string
		argv     []string
		wantText string
	}{
		{"explicit zh-CN", nil, "", []string{"--lang", "zh-CN", "--help"}, "全局选项"},
		{"explicit zh-TW", nil, "", []string{"-l", "zh-TW", "--help"}, "全域選項"},
		{"explicit ja-JP", nil, "", []string{"--lang=ja-JP", "--help"}, "グローバルオプション"},
		{"explicit wins over env", map[string]string{"EVERNIGHTINDEX_LANG": "ja-JP"}, "", []string{"--lang", "zh-CN", "--help"}, "全局选项"},
		{"env used", map[string]string{"EVERNIGHTINDEX_LANG": "ja-JP"}, "", []string{"--help"}, "グローバルオプション"},
		{"system used", nil, "zh-Hant-TW", []string{"--help"}, "全域選項"},
		{"alias used", nil, "", []string{"--lang", "zh", "--help"}, "全局选项"},
		{"unsupported system falls back to English", nil, "fr-FR", []string{"--help"}, "Global options"},
		{"c locale falls back to English", map[string]string{"LC_ALL": "C", "LANG": "ja_JP.UTF-8"}, "", []string{"--help"}, "Global options"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			withSystemLanguage(t, testCase.system)
			result := runCLI(t, testCase.argv, testCase.env)
			if result.code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", result.code, result.stderr)
			}
			if !strings.Contains(result.stdout, testCase.wantText) {
				t.Errorf("output does not contain %q: %q", testCase.wantText, firstLines(result.stdout, 6))
			}
		})
	}
}

// TestInvalidLanguageFails 驗證不合法語言值不會被靜默替換。
func TestInvalidLanguageFails(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		argv []string
	}{
		{"bad flag value", nil, []string{"--lang", "fr-FR", "--help"}},
		{"bad env value", map[string]string{"EVERNIGHTINDEX_LANG": "xx-YY"}, []string{"--help"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			withSystemLanguage(t, "")
			result := runCLI(t, testCase.argv, testCase.env)
			if result.code != 2 {
				t.Errorf("exit code = %d, want 2", result.code)
			}
			if !strings.Contains(result.stderr, "E_INVALID_LANGUAGE") {
				t.Errorf("stderr missing E_INVALID_LANGUAGE: %q", result.stderr)
			}
			if result.stdout != "" {
				t.Errorf("invalid language must not print help: %q", result.stdout)
			}
		})
	}
}

// TestInterruptedContextMapsToExit130 驗證取消對應使用者中斷退出碼。
func TestInterruptedContextMapsToExit130(t *testing.T) {
	withSystemLanguage(t, "")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr bytes.Buffer
	code := Main(ctx, testBuildInfo(), []string{"index", dir}, &stdout, &stderr, func(string) string { return "" })
	if code != 130 {
		t.Errorf("exit code = %d, want 130", code)
	}
	if !strings.Contains(stderr.String(), "E_INTERRUPTED") {
		t.Errorf("stderr missing E_INTERRUPTED: %q", stderr.String())
	}
	requireEmptyDir(t, dir)
}

func firstLines(text string, count int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, " | ")
}
