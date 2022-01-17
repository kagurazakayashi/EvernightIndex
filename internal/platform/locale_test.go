package platform

import "testing"

// TestNormalizeLocale 檢查 POSIX 與 BCP47 標籤正規化。
func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		"zh_CN.UTF-8":      "zh-CN",
		"zh_TW.UTF-8":      "zh-TW",
		"ja_JP.eucJP":      "ja-JP",
		"en_US.UTF-8@euro": "en-US",
		"en_GB":            "en-GB",
		"zh-Hant-TW":       "zh-Hant-TW",
		"zh-HK":            "zh-HK",
		"ja":               "ja",
		"C":                "",
		"POSIX":            "",
		"c.UTF-8":          "",
		"":                 "",
		"  ":               "",
	}
	for input, want := range cases {
		if got := NormalizeLocale(input); got != want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestFromEnvPriority 檢查 LC_ALL、LC_MESSAGES、LANG 的優先順序與 C 語系語義。
func TestFromEnvPriority(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"all empty", map[string]string{}, ""},
		{"lang only", map[string]string{"LANG": "zh_CN.UTF-8"}, "zh-CN"},
		{"messages wins over lang", map[string]string{"LC_MESSAGES": "zh_TW.UTF-8", "LANG": "ja_JP.UTF-8"}, "zh-TW"},
		{"lc_all wins", map[string]string{"LC_ALL": "ja_JP.UTF-8", "LC_MESSAGES": "zh_TW.UTF-8", "LANG": "zh_CN.UTF-8"}, "ja-JP"},
		{"c locale stops fallback", map[string]string{"LC_ALL": "C", "LANG": "zh_CN.UTF-8"}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			getenv := func(key string) string { return testCase.env[key] }
			if got := FromEnv(getenv); got != testCase.want {
				t.Errorf("FromEnv() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestSystemLanguageIsCallable 確認平台實作可呼叫（各平台實作不同）。
func TestSystemLanguageIsCallable(t *testing.T) {
	_ = SystemLanguage()
}
