package model

import (
	"testing"
)

// TestEscapeDisplayPreservesBytes 檢查顯示轉義對常見與惡劣名稱都是可逆的。
func TestEscapeDisplayPreservesBytes(t *testing.T) {
	cases := []string{
		"",
		"plain.txt",
		"with space.txt",
		"line\nbreak.txt",
		"-leading-dash.txt",
		"日本語テキスト.txt",
		"tab\there",
		"back\\slash",
		"windows\\path\\file",
		"invalid-\xff-\xfe",
		"trailing-backslash\\",
		"\x00nul",
	}
	for _, raw := range cases {
		escaped := EscapeDisplay(raw)
		restored, err := UnescapeDisplay(escaped)
		if err != nil {
			t.Fatalf("UnescapeDisplay(%q) failed: %v", escaped, err)
		}
		if restored != raw {
			t.Fatalf("round trip changed bytes: %q -> %q -> %q", raw, escaped, restored)
		}
	}
}

// TestEscapeDisplayEscapesControlAndInvalid 檢查轉義結果不會讓控制字元或無效位元組原樣流入輸出。
func TestEscapeDisplayEscapesControlAndInvalid(t *testing.T) {
	cases := map[string]string{
		"line\nbreak":  "line\\x0Abreak",
		"invalid-\xff": "invalid-\\xFF",
		"back\\slash":  "back\\\\slash",
		"plain.txt":    "plain.txt",
		"日本語.txt":      "日本語.txt",
	}
	for raw, want := range cases {
		if got := EscapeDisplay(raw); got != want {
			t.Fatalf("EscapeDisplay(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestUnescapeDisplayRejectsBrokenEscapes 檢查壞掉的轉義不被猜測。
func TestUnescapeDisplayRejectsBrokenEscapes(t *testing.T) {
	broken := []string{"trailing\\", "\\x1", "\\xZZ", "\\q"}
	for _, text := range broken {
		if _, err := UnescapeDisplay(text); err == nil {
			t.Fatalf("UnescapeDisplay(%q) should fail", text)
		}
	}
}

// TestPathKeyIsIndependentOfDisplayEscaping 檢查比較鍵用的是原始位元組。
func TestPathKeyIsIndependentOfDisplayEscaping(t *testing.T) {
	raw := "dir/invalid-\xff.txt"
	key := PathKey(raw, CaseSensitive)
	if key != raw {
		t.Fatalf("PathKey changed raw bytes: %q -> %q", raw, key)
	}
	if EscapeDisplay(raw) == raw {
		t.Fatal("display escaping should differ from the raw bytes for invalid UTF-8")
	}
}
