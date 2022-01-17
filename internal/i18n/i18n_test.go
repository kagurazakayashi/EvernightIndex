package i18n

import (
	"strings"
	"testing"
)

// TestCatalogConsistency 檢查四語言鍵集、空值與佔位符一致。
func TestCatalogConsistency(t *testing.T) {
	catalog := MustDefault()
	if problems := catalog.Check(); len(problems) > 0 {
		for _, problem := range problems {
			t.Errorf("%s: lang=%s key=%s %s", problem.Kind, problem.Lang, problem.Key, problem.Detail)
		}
	}
	if keys := catalog.Keys(); len(keys) < 40 {
		t.Fatalf("expected a meaningful message set, got %d keys", len(keys))
	}
}

// TestEveryLanguageHasSameKeys 確認每種語言都載入了同一批鍵。
func TestEveryLanguageHasSameKeys(t *testing.T) {
	catalog := MustDefault()
	for _, lang := range Supported {
		for _, key := range catalog.Keys() {
			if got := catalog.T(lang, key, nil); got == key {
				t.Errorf("%s: message %q is missing", lang, key)
			}
		}
	}
}

// TestParseLanguageTags 檢查標籤與別名映射。
func TestParseLanguageTags(t *testing.T) {
	cases := []struct {
		tag  string
		want Lang
		ok   bool
	}{
		{"auto", LangAuto, true},
		{"AUTO", LangAuto, true},
		{"zh-CN", LangZhCN, true},
		{"zh_cn", LangZhCN, true},
		{"zh-Hans", LangZhCN, true},
		{"zh-TW", LangZhTW, true},
		{"zh-Hant-TW", LangZhTW, true},
		{"zh-HK", LangZhTW, true},
		{"en-US", LangEnUS, true},
		{"EN_gb", LangEnUS, true},
		{"ja-JP", LangJaJP, true},
		{"ja", LangJaJP, true},
		{"fr-FR", "", false},
		{"", "", false},
		{"zh-XX", "", false},
	}
	for _, testCase := range cases {
		got, ok := Parse(testCase.tag)
		if ok != testCase.ok || got != testCase.want {
			t.Errorf("Parse(%q) = (%q, %v), want (%q, %v)", testCase.tag, got, ok, testCase.want, testCase.ok)
		}
	}
}

// TestSubstitution 檢查佔位符替換與未知佔位符保留。
func TestSubstitution(t *testing.T) {
	catalog := MustDefault()
	got := catalog.T(LangEnUS, "err.unknownFlag", map[string]string{"flag": "--bogus"})
	if !strings.Contains(got, "--bogus") {
		t.Errorf("substitution lost the argument: %q", got)
	}
	if got == "err.unknownFlag" {
		t.Error("message key was not resolved")
	}
	// 未提供參數時保留原樣，避免顯示空白造成誤解。
	raw := catalog.T(LangEnUS, "err.unknownFlag", nil)
	if !strings.Contains(raw, "{flag}") {
		t.Errorf("unexpected message without arguments: %q", raw)
	}
}

// TestFallbackLanguage 檢查缺失語言的鍵回退英語。
func TestFallbackLanguage(t *testing.T) {
	catalog := MustDefault()
	got := catalog.T(Lang("fr-FR"), "err.notImplemented", map[string]string{"command": "index"})
	want := catalog.T(Fallback, "err.notImplemented", map[string]string{"command": "index"})
	if got != want {
		t.Errorf("fallback failed: got %q, want %q", got, want)
	}
}

// TestPlaceholders 檢查佔位符解析。
func TestPlaceholders(t *testing.T) {
	got := Placeholders("a {x} b {y} c {x}")
	want := []string{"x", "x", "y"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Placeholders = %v, want %v", got, want)
	}
	if names := Placeholders("no placeholders"); len(names) != 0 {
		t.Errorf("expected no placeholders, got %v", names)
	}
}
