package cli

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// codeOf 取出錯誤碼，方便表格測試斷言。
func codeOf(t *testing.T, err error) model.ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var appErr *model.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *model.Error, got %T: %v", err, err)
	}
	return appErr.Code
}

func mustParse(t *testing.T, argv ...string) *options {
	t.Helper()
	opts, err := parse(argv)
	if err != nil {
		t.Fatalf("parse(%v) failed: %v", argv, err)
	}
	return opts
}

// TestLongAndShortFormsAreEquivalent 確認長短選項解析結果完全一致。
func TestLongAndShortFormsAreEquivalent(t *testing.T) {
	cases := [][2][]string{
		{{"index", "dir", "--db=abc.db"}, {"index", "dir", "-d", "abc.db"}},
		{{"index", "dir", "--db", "abc.db"}, {"index", "dir", "-d=abc.db"}},
		{{"index", "dir", "--sha256=false"}, {"index", "dir", "-s=false"}},
		{{"index", "dir", "--deep"}, {"index", "dir", "-D"}},
		{{"index", "dir", "--change-log", "--log-dir", "logs"}, {"index", "dir", "-c", "-L", "logs"}},
		{{"find", "--db", "x.db", "--null"}, {"find", "-d", "x.db", "-z"}},
		{{"dupes", "a", "b", "--match=sha256", "--verify=sha256"}, {"dupes", "a", "b", "-m", "sha256", "-r", "sha256"}},
	}
	for _, pair := range cases {
		long := mustParse(t, pair[0]...)
		short := mustParse(t, pair[1]...)
		if long.command.name != short.command.name || len(long.values) != len(short.values) {
			t.Fatalf("%v and %v produced different commands or option counts", pair[0], pair[1])
		}
		for name := range long.values {
			if long.str(name) != short.str(name) {
				t.Errorf("option %q: long %q, short %q", name, long.str(name), short.str(name))
			}
		}
	}
}

// TestDefaultsAreApplied 確認預設值集中在宣告中並被正確讀取。
func TestDefaultsAreApplied(t *testing.T) {
	opts := mustParse(t, "index", "dir")
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"lang", opts.str("lang"), "auto"},
		{"sha256", opts.boolValue("sha256"), true},
		{"deep", opts.boolValue("deep"), false},
		{"change-log", opts.boolValue("change-log"), false},
		{"progress", opts.boolValue("progress"), true},
		{"workers", opts.intValue("workers"), 0},
		{"lock-timeout", opts.durationValue("lock-timeout"), 30 * time.Second},
		{"read-retries", opts.intValue("read-retries"), 1},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s default = %v, want %v", check.name, check.got, check.want)
		}
	}
	dupes := mustParse(t, "dupes", "a", "b")
	if got := dupes.str("match"); got != "auto" {
		t.Errorf("match default = %q, want auto", got)
	}
	if got := dupes.str("verify"); got != "none" {
		t.Errorf("verify default = %q, want none", got)
	}
	if got := dupes.str("format"); got != "text" {
		t.Errorf("format default = %q, want text", got)
	}
	if got := dupes.intValue("limit"); got != 0 {
		t.Errorf("limit default = %d, want 0", got)
	}
}

// TestScalarLastWinsAndListAccumulates 確認重複指定的規則。
func TestScalarLastWinsAndListAccumulates(t *testing.T) {
	opts := mustParse(t, "index", "dir", "--db=first.db", "-d", "second.db", "--db", "third.db")
	if got := opts.str("db"); got != "third.db" {
		t.Errorf("scalar last-wins failed: %q", got)
	}
	opts = mustParse(t, "index", "dir", "-e", "a/**", "--exclude", "b/**", "-e=c/**")
	if got := opts.list("exclude"); len(got) != 3 || got[0] != "a/**" || got[2] != "c/**" {
		t.Errorf("list accumulation = %v", got)
	}
}

// TestDoubleDashTerminatesOptions 確認 -- 之後的內容都當成位置參數。
func TestDoubleDashTerminatesOptions(t *testing.T) {
	opts := mustParse(t, "index", "--", "-weird-dir")
	if len(opts.args) != 1 || opts.args[0] != "-weird-dir" {
		t.Fatalf("args = %v, want [-weird-dir]", opts.args)
	}
	if opts.boolValue("verbose") {
		t.Error("-- must not be interpreted as a flag")
	}
	opts = mustParse(t, "dupes", "a", "--", "-b")
	if len(opts.args) != 2 || opts.args[1] != "-b" {
		t.Fatalf("args = %v, want [a -b]", opts.args)
	}
	if _, err := parse([]string{"find", "--", "--name"}); codeOf(t, err) != model.CodeUnexpectedArgument {
		t.Error("positional arguments after -- must still respect the command arity")
	}
}

// TestLongAbbreviationIsRejected 確認長參數不接受含糊的前綴縮寫。
func TestLongAbbreviationIsRejected(t *testing.T) {
	_, err := parse([]string{"index", "dir", "--work", "4"})
	if code := codeOf(t, err); code != model.CodeUnknownFlag {
		t.Errorf("code = %s, want %s", code, model.CodeUnknownFlag)
	}
}

// TestShortOptionForms 確認短選項的支援與拒絕形式。
func TestShortOptionForms(t *testing.T) {
	if _, err := parse([]string{"index", "dir", "-vq"}); codeOf(t, err) != model.CodeInvalidValue {
		t.Error("clustered short options (-vq) must be rejected")
	}
	if _, err := parse([]string{"index", "dir", "-dpath"}); codeOf(t, err) != model.CodeInvalidValue {
		t.Error("glued short value (-dpath) must be rejected")
	}
	opts := mustParse(t, "index", "dir", "-v", "-q=false", "-d=abc.db")
	if !opts.boolValue("verbose") || opts.boolValue("quiet") || opts.str("db") != "abc.db" {
		t.Errorf("separate short options failed: %#v", opts.values)
	}
}

// TestBooleanFalseForms 確認布林 false 只接受明確的 = 形式。
func TestBooleanFalseForms(t *testing.T) {
	if got := mustParse(t, "index", "dir", "--sha256=false").boolValue("sha256"); got {
		t.Error("--sha256=false must disable SHA256")
	}
	if got := mustParse(t, "index", "dir", "-s=false").boolValue("sha256"); got {
		t.Error("-s=false must disable SHA256")
	}
	if _, err := parse([]string{"index", "dir", "--sha256=false", "--sha256=nope"}); codeOf(t, err) != model.CodeInvalidValue {
		t.Error("invalid boolean literal must be rejected")
	}
	// 空格後的 false 是位置參數，不是布林值。
	if _, err := parse([]string{"index", "dir", "--sha256", "false"}); codeOf(t, err) != model.CodeUnexpectedArgument {
		t.Error("a space-separated boolean value must be reported as an unexpected argument")
	}
}

// TestMissingValues 確認需要值的選項在被省略值時回報明確錯誤。
func TestMissingValues(t *testing.T) {
	cases := [][]string{
		{"find", "--db"},
		{"find", "--db", "--name", "x"},
		{"index", "dir", "--lock-timeout"},
		{"dupes", "a", "b", "--verify"},
	}
	for _, argv := range cases {
		if _, err := parse(argv); codeOf(t, err) != model.CodeFlagNeedsValue {
			t.Errorf("%v: expected %s", argv, model.CodeFlagNeedsValue)
		}
	}
}

// TestValueValidation 確認型別、列舉與值域檢查。
func TestValueValidation(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		code model.ErrorCode
	}{
		{"unknown enum", []string{"find", "--case", "Sensitive"}, model.CodeInvalidValue},
		{"bad format", []string{"find", "--format", "xml"}, model.CodeInvalidValue},
		{"bad match", []string{"dupes", "a", "b", "--match", "size"}, model.CodeInvalidValue},
		{"negative limit", []string{"find", "--limit=-1"}, model.CodeInvalidValue},
		{"bad duration", []string{"index", "dir", "--lock-timeout", "30"}, model.CodeInvalidValue},
		{"too many workers", []string{"index", "dir", "--workers", strconv.Itoa(MaxWorkers() + 1)}, model.CodeInvalidValue},
		{"bad language", []string{"--lang", "fr-FR", "index", "dir"}, model.CodeInvalidLanguage},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parse(testCase.argv)
			if code := codeOf(t, err); code != testCase.code {
				t.Errorf("code = %s, want %s", code, testCase.code)
			}
		})
	}
	if got := mustParse(t, "index", "dir", "--lock-timeout", "0").durationValue("lock-timeout"); got != 0 {
		t.Errorf("lock-timeout 0 must be accepted, got %v", got)
	}
}

// TestPositionalCounts 確認每個命令的位置參數數量規則。
func TestPositionalCounts(t *testing.T) {
	if _, err := parse([]string{"index"}); codeOf(t, err) != model.CodeMissingArgument {
		t.Error("index without a directory must report a missing argument")
	}
	if _, err := parse([]string{"index", "a", "b"}); codeOf(t, err) != model.CodeUnexpectedArgument {
		t.Error("index with two directories must report an unexpected argument")
	}
	if _, err := parse([]string{"dupes", "a"}); codeOf(t, err) != model.CodeMissingArgument {
		t.Error("dupes with one directory must report a missing argument")
	}
	if _, err := parse([]string{"find", "extra"}); codeOf(t, err) != model.CodeUnexpectedArgument {
		t.Error("find takes no positional arguments")
	}
}

// TestFlagScope 確認選項只在自己的命令範圍內有效。
func TestFlagScope(t *testing.T) {
	if _, err := parse([]string{"index", "dir", "--name", "*.go"}); codeOf(t, err) != model.CodeFlagNotApplicable {
		t.Error("--name must not apply to index")
	}
	if _, err := parse([]string{"--db", "x.db", "index", "dir"}); codeOf(t, err) != model.CodeFlagNotApplicable {
		t.Error("command options must come after the command name")
	}
	if _, err := parse([]string{"export-log", "--temp-dir", "tmp"}); codeOf(t, err) != model.CodeFlagNotApplicable {
		t.Error("--temp-dir must not apply to export-log")
	}
	if _, err := parse([]string{"info", "--run", "r1"}); codeOf(t, err) != model.CodeFlagNotApplicable {
		t.Error("--run must not apply to info")
	}
}

// TestInvalidCombinations 確認非法組合在解析階段就被拒絕。
func TestInvalidCombinations(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"deep without sha256", []string{"index", "dir", "--deep", "--sha256=false"}},
		{"quiet with verbose", []string{"index", "dir", "--quiet", "--verbose"}},
		{"verify sha256 with auto match", []string{"dupes", "a", "b", "--match=auto", "--verify=sha256"}},
		{"verify sha256 with metadata match", []string{"dupes", "a", "b", "--match=metadata", "--verify=sha256"}},
		{"null with details", []string{"find", "--null", "--details"}},
		{"null with jsonl", []string{"find", "--null", "--format=jsonl"}},
		{"null with csv", []string{"find", "--null", "--format=csv"}},
		{"log dir without change log", []string{"index", "dir", "--log-dir", "logs"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parse(testCase.argv)
			if code := codeOf(t, err); code != model.CodeFlagConflict {
				t.Errorf("code = %s, want %s", code, model.CodeFlagConflict)
			}
		})
	}
	// 合法組合必須通過。
	mustParse(t, "dupes", "a", "b", "--match=sha256", "--verify=sha256")
	mustParse(t, "index", "dir", "--change-log", "--log-dir", "logs")
	mustParse(t, "index", "dir", "--deep")
	mustParse(t, "find", "--null")
	mustParse(t, "find", "--details", "--format=csv")
}

// TestHelpAndVersionBypassArgumentRules 確認說明與版本不需要完整的位置參數。
func TestHelpAndVersionBypassArgumentRules(t *testing.T) {
	for _, argv := range [][]string{
		{"index", "--help"},
		{"dupes", "--help"},
		{"--help"},
		{"index", "--version"},
		{"--version"},
	} {
		if _, err := parse(argv); err != nil {
			t.Errorf("parse(%v) failed: %v", argv, err)
		}
	}
}

// TestMissingCommand 確認沒有子命令時回報明確錯誤。
func TestMissingCommand(t *testing.T) {
	if _, err := parse(nil); codeOf(t, err) != model.CodeMissingCommand {
		t.Error("empty command line must report a missing command")
	}
	if _, err := parse([]string{"unknown"}); codeOf(t, err) != model.CodeUnknownCommand {
		t.Error("unknown command must be reported as such")
	}
}
