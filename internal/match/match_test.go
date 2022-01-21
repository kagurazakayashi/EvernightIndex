package match_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kagurazakayashi/EvernightIndex/internal/match"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

func mustCompile(t *testing.T, pattern string, opts match.Options) *match.Pattern {
	t.Helper()
	p, err := match.Compile(pattern, opts)
	if err != nil {
		t.Fatalf("Compile(%q) 失敗：%v", pattern, err)
	}
	return p
}

// TestCompileErrors 驗證第 10 條：錯誤碼、訊息鍵與固定英語原因，並涵蓋
// 空模式、未閉合字元類別、空字元類別、segment 內部的 "**" 與顛倒範圍。
func TestCompileErrors(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		reason  string
	}{
		{name: "空模式", pattern: "", reason: "empty pattern"},
		{name: "只有分隔符", pattern: "/", reason: "empty pattern"},
		{name: "未閉合字元類別", pattern: "[abc", reason: "unclosed character class"},
		{name: "只有左括號", pattern: "x[", reason: "unclosed character class"},
		{name: "空字元類別", pattern: "[]", reason: "empty character class"},
		{name: "取反後空字元類別", pattern: "[!]", reason: "empty character class"},
		{name: "segment 內部雙星", pattern: "a**b", reason: "** must be a whole path segment"},
		{name: "三顆星", pattern: "***", reason: "** must be a whole path segment"},
		{name: "錨定後內部雙星", pattern: "x/a**b", reason: "** must be a whole path segment"},
		{name: "顛倒範圍", pattern: "[z-a]", reason: "invalid character range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := match.Compile(tc.pattern, match.Options{})
			if err == nil {
				t.Fatalf("Compile(%q) 應該失敗，卻回傳 %v", tc.pattern, p)
			}
			if p != nil {
				t.Fatalf("Compile(%q) 失敗時應回傳 nil 模式", tc.pattern)
			}
			var me *model.Error
			if !errors.As(err, &me) {
				t.Fatalf("Compile(%q) 錯誤型別應為 *model.Error，得到 %T", tc.pattern, err)
			}
			if me.Code != model.CodeInvalidPattern {
				t.Errorf("Code = %q，want %q", me.Code, model.CodeInvalidPattern)
			}
			if me.Key != "err.invalidPattern" {
				t.Errorf("Key = %q，want %q", me.Key, "err.invalidPattern")
			}
			if me.Exit != model.ExitUsage {
				t.Errorf("Exit = %d，want %d", me.Exit, model.ExitUsage)
			}
			if got := me.Args["pattern"]; got != tc.pattern {
				t.Errorf("Args[pattern] = %q，want %q", got, tc.pattern)
			}
			if got := me.Args["reason"]; got != tc.reason {
				t.Errorf("Args[reason] = %q，want %q", got, tc.reason)
			}
		})
	}
}

// TestPatternProperties 驗證第 5、6、7 條對外可見的模式屬性。
func TestPatternProperties(t *testing.T) {
	cases := []struct {
		pattern      string
		wantString   string
		wantDirOnly  bool
		wantAnchored bool
	}{
		{pattern: "a", wantString: "a"},
		{pattern: "*.go", wantString: "*.go"},
		{pattern: "a/", wantString: "a/", wantDirOnly: true},
		{pattern: "/a", wantString: "/a", wantAnchored: true},
		{pattern: "/a/b/", wantString: "/a/b/", wantDirOnly: true, wantAnchored: true},
		{pattern: `a\b`, wantString: `a\b`, wantAnchored: true},
		{pattern: "a//b", wantString: "a//b", wantAnchored: true},
		{pattern: "**", wantString: "**"},
		{pattern: "**/x", wantString: "**/x", wantAnchored: true},
		{pattern: "[!a]", wantString: "[!a]"},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			p := mustCompile(t, tc.pattern, match.Options{})
			if got := p.String(); got != tc.wantString {
				t.Errorf("String() = %q，want %q", got, tc.wantString)
			}
			if got := p.DirOnly(); got != tc.wantDirOnly {
				t.Errorf("DirOnly() = %v，want %v", got, tc.wantDirOnly)
			}
			if got := p.Anchored(); got != tc.wantAnchored {
				t.Errorf("Anchored() = %v，want %v", got, tc.wantAnchored)
			}
		})
	}

	// 大小寫折疊不影響 String() 回傳的原始模式。
	p := mustCompile(t, "Dir/*.TXT", match.Options{Insensitive: true})
	if got := p.String(); got != "Dir/*.TXT" {
		t.Errorf("Insensitive 模式下 String() = %q，want %q", got, "Dir/*.TXT")
	}
}

// TestMatchPath 表驅動驗證第 1 至 9 條的路徑比對語意。
func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern string
		opt     match.Options
		path    string
		isDir   bool
		want    bool
	}{
		// 第 3 條：* 與 ? 不跨分隔符。
		{pattern: "*", path: "a", want: true},
		{pattern: "*", path: "a/b", want: true},
		{pattern: "*.txt", path: "dir/x.txt", want: true},
		{pattern: "*.txt", path: "dir/x.md", want: false},
		{pattern: "a*", path: "a", want: true},
		{pattern: "a*", path: "abc", want: true},
		{pattern: "a*", path: "bc", want: false},
		{pattern: "a?c", path: "abc", want: true},
		{pattern: "a?c", path: "ac", want: false},
		{pattern: "a?c", path: "abbc", want: false},
		{pattern: "a?c", path: "a/c", want: false},
		{pattern: "?x", path: "yx", want: true},

		// 第 3 條：字元類別、範圍與取反；首位 ] 是字面字元。
		{pattern: "[abc]", path: "b", want: true},
		{pattern: "[abc]", path: "d", want: false},
		{pattern: "[a-c]x", path: "bx", want: true},
		{pattern: "[a-c]x", path: "dx", want: false},
		{pattern: "[!abc]", path: "d", want: true},
		{pattern: "[!abc]", path: "a", want: false},
		{pattern: "[^abc]", path: "d", want: true},
		{pattern: "[^abc]", path: "a", want: false},
		{pattern: "[]]", path: "]", want: true},
		{pattern: "[]a]", path: "a", want: true},
		{pattern: "[a-]", path: "-", want: true},
		{pattern: "[-a]", path: "-", want: true},

		// 第 4 條：** 只在整個 segment 時跨 segment，且可為 0 段。
		{pattern: "a/**/b", path: "a/b", want: true},
		{pattern: "a/**/b", path: "a/x/b", want: true},
		{pattern: "a/**/b", path: "a/x/y/b", want: true},
		{pattern: "a/**/b", path: "a/x/y/c", want: false},
		{pattern: "a/**/b", path: "a", want: false},
		{pattern: "a/**", path: "a", want: true},
		{pattern: "a/**", path: "a/x", want: true},
		{pattern: "a/**", path: "a/x/y", want: true},
		{pattern: "**/x", path: "x", want: true},
		{pattern: "**/x", path: "a/x", want: true},
		{pattern: "**/x", path: "a/b/y", want: false},
		{pattern: "**", path: "a/b", want: true},
		{pattern: "**", path: ".", isDir: true, want: true},
		{pattern: "a/*/b", path: "a/x/b", want: true},
		{pattern: "a/*/b", path: "a/b", want: false},
		{pattern: "a/*/b", path: "a/x/y/b", want: false},

		// 第 5、6 條：錨定與「任意深度的單一名稱」。
		{pattern: "a/b", path: "a/b", want: true},
		{pattern: "/a/b", path: "a/b", want: true},
		{pattern: "a/b", path: "x/a/b", want: false},
		{pattern: "/a/b", path: "x/a/b", want: false},
		{pattern: "x", path: "a/x/b", want: true},
		{pattern: "*", path: ".", want: false},
		{pattern: "a/b", path: ".", want: false},

		// 第 7 條：結尾 / 只匹配目錄，不影響路徑比對。
		{pattern: "build/", path: "build", isDir: true, want: true},
		{pattern: "build/", path: "build", isDir: false, want: false},
		{pattern: "build/", path: "src/build", isDir: true, want: true},
		{pattern: "a/b/", path: "a/b", isDir: true, want: true},
		{pattern: "a/b/", path: "a/b", isDir: false, want: false},

		// 第 1、2 條：分隔符統一與路徑收斂。
		{pattern: `a\b`, path: "a/b", want: true},
		{pattern: "a//b", path: "a/b", want: true},
		{pattern: "a/b", path: `a\b`, want: true},
		{pattern: "a/b", path: "a//b", want: true},
		{pattern: "b", path: `a\b\c`, want: true},
		{pattern: "/a/b", path: "//a//b", want: true},

		// 第 8 條：大小寫折疊。
		{pattern: "*.TXT", path: "dir/x.txt", want: false},
		{pattern: "*.TXT", opt: match.Options{Insensitive: true}, path: "dir/x.txt", want: true},
		{pattern: "ABC", opt: match.Options{Insensitive: true}, path: "abc", want: true},
		{pattern: "[A-C]", path: "b", want: false},
		{pattern: "[A-C]", opt: match.Options{Insensitive: true}, path: "b", want: true},
		{pattern: "Dir/File", opt: match.Options{Insensitive: true}, path: "dir/file", want: true},

		// 第 9 條：非 UTF-8 位元組、空白、換行與前導 -。
		{pattern: "*", path: "\xff\xfe", want: true},
		{pattern: "?", path: "\xff", want: true},
		{pattern: "??", path: "\xff\xfe", want: true},
		{pattern: "?", path: "\xff\xfe", want: false},
		{pattern: "[a-c]", path: "\xff", want: false},
		{pattern: "a?b", path: "a\xffb", want: true},
		{pattern: "\xff*", path: "\xffabc", want: true},
		{pattern: "a b", path: "a b", want: true},
		{pattern: "a *", path: "a bc", want: true},
		{pattern: "line\nbreak", path: "line\nbreak", want: true},
		{pattern: "*\n*", path: "a\nb", want: true},
		{pattern: "-*", path: "-rf", want: true},
		{pattern: "-*", path: "x-rf", want: false},
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("%02d_%s", i, tc.pattern), func(t *testing.T) {
			p := mustCompile(t, tc.pattern, tc.opt)
			if got := p.MatchPath(tc.path, tc.isDir); got != tc.want {
				t.Fatalf("MatchPath(%q, isDir=%v) = %v，want %v（pattern %q, insensitive=%v）",
					tc.path, tc.isDir, got, tc.want, tc.pattern, tc.opt.Insensitive)
			}
		})
	}
}

// TestNonUTF8 固定第 9 條要求的非 UTF-8 位元組行為。
func TestNonUTF8(t *testing.T) {
	// "?" 對無效位元組恰好前進 1 個位元組。
	p := mustCompile(t, "?", match.Options{})
	if !p.MatchName("\x80") {
		t.Error("? 應匹配單一無效 UTF-8 位元組")
	}
	// "*" 逐位元組匹配，不丟棄位元組。
	p = mustCompile(t, "a*b", match.Options{})
	if !p.MatchName("a\x80\x81b") {
		t.Error("* 應匹配任意無效 UTF-8 位元組序列")
	}
	// 字元類別不匹配無效位元組。
	p = mustCompile(t, "[a-z]", match.Options{})
	if p.MatchName("\x80") {
		t.Error("字元類別不應匹配無效 UTF-8 位元組")
	}
	// 大小寫折疊不得把無效位元組換成 U+FFFD。
	insensitive := mustCompile(t, "x*y", match.Options{Insensitive: true})
	if !insensitive.MatchName("X\x80Y") {
		t.Error("大小寫折疊應保留無效 UTF-8 位元組並完成折疊")
	}
}

// TestMatchName 驗證只用單一名稱的比對入口。
func TestMatchName(t *testing.T) {
	cases := []struct {
		pattern string
		opt     match.Options
		name    string
		want    bool
	}{
		{pattern: "*.go", name: "main.go", want: true},
		{pattern: "*.go", name: "main.gox", want: false},
		{pattern: "[!a]", name: "b", want: true},
		{pattern: "**", name: "anything", want: true},
		{pattern: "a/b", name: "b", want: false},
		{pattern: "/a", name: "a", want: false},
		{pattern: "build/", name: "build", want: true},
		{pattern: "x\ny", name: "x\ny", want: true},
		{pattern: "-v", name: "-v", want: true},
		{pattern: "ABC", opt: match.Options{Insensitive: true}, name: "abc", want: true},
		{pattern: "ABC", name: "abc", want: false},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%02d_%s", i, tc.pattern), func(t *testing.T) {
			p := mustCompile(t, tc.pattern, tc.opt)
			if got := p.MatchName(tc.name); got != tc.want {
				t.Fatalf("MatchName(%q) = %v，want %v（pattern %q）", tc.name, got, tc.want, tc.pattern)
			}
		})
	}
}

// TestSet 驗證多模式集合的編譯、命中、長度、原始模式與失敗傳播。
func TestSet(t *testing.T) {
	patterns := []string{"*.tmp", "node_modules/", "/dist", "**/Thumbs.db"}
	set, err := match.CompileAll(patterns, match.Options{})
	if err != nil {
		t.Fatalf("CompileAll 失敗：%v", err)
	}
	if got := set.Len(); got != len(patterns) {
		t.Errorf("Len() = %d，want %d", got, len(patterns))
	}
	if set.Empty() {
		t.Error("Empty() 應為 false")
	}
	got := set.Patterns()
	if len(got) != len(patterns) {
		t.Fatalf("Patterns() 長度 = %d，want %d", len(got), len(patterns))
	}
	for i := range patterns {
		if got[i] != patterns[i] {
			t.Errorf("Patterns()[%d] = %q，want %q", i, got[i], patterns[i])
		}
	}
	// Patterns() 回傳副本，修改不影響集合。
	got[0] = "changed"
	if set.Patterns()[0] != patterns[0] {
		t.Error("Patterns() 應回傳副本")
	}

	pathCases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{path: "a/x.tmp", want: true},
		{path: "a/x.go", want: false},
		{path: "node_modules", isDir: true, want: true},
		{path: "node_modules", isDir: false, want: false},
		{path: "a/node_modules", isDir: true, want: true},
		{path: "dist", want: true},
		{path: "dist", isDir: true, want: true},
		{path: "a/dist", want: false},
		{path: "a/Thumbs.db", want: true},
	}
	for i, tc := range pathCases {
		t.Run(fmt.Sprintf("path_%02d", i), func(t *testing.T) {
			if got := set.MatchPath(tc.path, tc.isDir); got != tc.want {
				t.Fatalf("Set.MatchPath(%q, isDir=%v) = %v，want %v", tc.path, tc.isDir, got, tc.want)
			}
		})
	}

	nameCases := []struct {
		name string
		want bool
	}{
		{name: "x.tmp", want: true},
		{name: "x.go", want: false},
		{name: "node_modules", want: true},
		// "**/Thumbs.db" 與 "/dist" 是錨定模式，MatchName 一律回傳 false。
		{name: "Thumbs.db", want: false},
		{name: "dist", want: false},
	}
	for i, tc := range nameCases {
		t.Run(fmt.Sprintf("name_%02d", i), func(t *testing.T) {
			if got := set.MatchName(tc.name); got != tc.want {
				t.Fatalf("Set.MatchName(%q) = %v，want %v", tc.name, got, tc.want)
			}
		})
	}

	// 空集合。
	empty, err := match.CompileAll(nil, match.Options{})
	if err != nil {
		t.Fatalf("CompileAll(nil) 失敗：%v", err)
	}
	if !empty.Empty() || empty.Len() != 0 {
		t.Errorf("空集合應 Empty 且 Len 為 0，得到 Empty=%v Len=%d", empty.Empty(), empty.Len())
	}
	if empty.MatchPath("a", false) || empty.MatchName("a") {
		t.Error("空集合不應命中")
	}

	// 任一模式失敗即回傳錯誤，且錯誤中帶出錯的模式。
	if _, err := match.CompileAll([]string{"ok*", "bad**pat"}, match.Options{}); err == nil {
		t.Fatal("CompileAll 應回傳錯誤")
	} else {
		var me *model.Error
		if !errors.As(err, &me) {
			t.Fatalf("錯誤型別應為 *model.Error，得到 %T", err)
		}
		if me.Code != model.CodeInvalidPattern || me.Key != "err.invalidPattern" {
			t.Errorf("Code/Key = %q/%q，want %q/%q", me.Code, me.Key, model.CodeInvalidPattern, "err.invalidPattern")
		}
		if me.Args["pattern"] != "bad**pat" {
			t.Errorf("Args[pattern] = %q，want %q", me.Args["pattern"], "bad**pat")
		}
		if me.Args["reason"] != "** must be a whole path segment" {
			t.Errorf("Args[reason] = %q", me.Args["reason"])
		}
	}
}

// TestNilReceivers 驗證 nil 安全性：不 panic、不誤命中、不回傳 error。
func TestNilReceivers(t *testing.T) {
	var p *match.Pattern
	if p.MatchPath("a", true) {
		t.Error("nil Pattern 的 MatchPath 應為 false")
	}
	if p.MatchName("a") {
		t.Error("nil Pattern 的 MatchName 應為 false")
	}
	if p.String() != "" || p.DirOnly() || p.Anchored() {
		t.Error("nil Pattern 的屬性應為零值")
	}

	var s *match.Set
	if s.MatchPath("a", true) {
		t.Error("nil Set 的 MatchPath 應為 false")
	}
	if s.MatchName("a") {
		t.Error("nil Set 的 MatchName 應為 false")
	}
	if !s.Empty() {
		t.Error("nil Set 的 Empty 應為 true")
	}
	if s.Len() != 0 {
		t.Error("nil Set 的 Len 應為 0")
	}
	if s.Patterns() != nil {
		t.Error("nil Set 的 Patterns 應為 nil")
	}
}
