// Package platform 隔離各作業系統的原生能力，平台實作以建置標籤分檔。
//
// 本套件只負責取得系統事實（例如使用者介面語言），不決定業務行為；
// 語言選擇策略留在 i18n 與 CLI 層，因此這裡不會引用 i18n。
package platform

import "strings"

// localeEnvKeys 依 POSIX 優先順序排列語言環境變數。
var localeEnvKeys = []string{"LC_ALL", "LC_MESSAGES", "LANG"}

// NormalizeLocale 把 POSIX 或 BCP47 風格的語言標籤正規化成 language-TERRITORY 形式。
//
// 輸入可能是 zh_CN.UTF-8、en_US.UTF-8@euro、ja_JP.eucJP。C 與 POSIX 代表使用者
// 沒有指定語言，回傳空字串讓上層回退英語，而不是猜測其他語言。
func NormalizeLocale(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	switch strings.ToUpper(s) {
	case "C", "POSIX":
		return ""
	}
	if underscore := strings.IndexByte(s, '_'); underscore >= 0 {
		language, rest := s[:underscore], s[underscore+1:]
		territory := rest
		if i := strings.IndexByte(rest, '_'); i >= 0 {
			territory = rest[:i]
		}
		if territory == "" {
			return language
		}
		return language + "-" + territory
	}
	return s
}

// FromEnv 依 LC_ALL、LC_MESSAGES、LANG 的優先順序取出第一個已設定的語言標籤。
//
// 已設定但正規化為空字串（例如 LC_ALL=C）時立即回傳空字串：POSIX 規定
// LC_ALL 覆蓋其他變數，此時不應再採用 LANG 的語言。
func FromEnv(getenv func(string) string) string {
	for _, key := range localeEnvKeys {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			continue
		}
		return NormalizeLocale(raw)
	}
	return ""
}
