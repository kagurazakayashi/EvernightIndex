// Package i18n 提供四語言訊息資源、語言解析與資源缺項檢查。
//
// 訊息以「鍵 + 佔位符」表示，不允許拼接片段，以免語序錯誤。機器欄位
// （參數名、子命令、列舉值、錯誤碼、CSV 標題、JSONL 鍵）一律不翻譯。
package i18n

import "strings"

// Lang 是訊息語言標籤。
type Lang string

const (
	// LangAuto 表示依環境自動判斷。
	LangAuto Lang = "auto"
	// LangZhCN 是簡體中文。
	LangZhCN Lang = "zh-CN"
	// LangZhTW 是繁體中文。
	LangZhTW Lang = "zh-TW"
	// LangEnUS 是英語。
	LangEnUS Lang = "en-US"
	// LangJaJP 是日語。
	LangJaJP Lang = "ja-JP"
)

// Fallback 是缺少資源或語言不受支援時的回退語言。
const Fallback = LangEnUS

// Supported 依固定順序列出可直接選用的語言（不含 auto）。
var Supported = []Lang{LangZhCN, LangZhTW, LangEnUS, LangJaJP}

// aliases 把常見語言標籤映射到支援的語言，README 需同步列出此映射。
var aliases = map[string]Lang{
	"auto":       LangAuto,
	"zh":         LangZhCN,
	"zh-cn":      LangZhCN,
	"zh_cn":      LangZhCN,
	"zh-hans":    LangZhCN,
	"zh-hans-cn": LangZhCN,
	"zh-sg":      LangZhCN,
	"zh-tw":      LangZhTW,
	"zh_tw":      LangZhTW,
	"zh-hant":    LangZhTW,
	"zh-hant-tw": LangZhTW,
	"zh-hk":      LangZhTW,
	"zh-mo":      LangZhTW,
	"en":         LangEnUS,
	"en-us":      LangEnUS,
	"en_us":      LangEnUS,
	"en-gb":      LangEnUS,
	"en_gb":      LangEnUS,
	"ja":         LangJaJP,
	"ja-jp":      LangJaJP,
	"ja_jp":      LangJaJP,
}

// Parse 解析語言標籤；第二個回傳值表示是否為受支援的標籤。
func Parse(tag string) (Lang, bool) {
	key := strings.ToLower(strings.TrimSpace(tag))
	if key == "" {
		return "", false
	}
	if l, ok := aliases[key]; ok {
		return l, true
	}
	return "", false
}

// String 回傳穩定的語言標籤，供機器輸出與 README 對照使用。
func (l Lang) String() string { return string(l) }

// Allowed 回傳錯誤訊息中列出的可選值清單；語言標籤本身固定為英語標識。
func Allowed() string {
	parts := make([]string, 0, len(Supported)+1)
	parts = append(parts, LangAuto.String())
	for _, l := range Supported {
		parts = append(parts, l.String())
	}
	return strings.Join(parts, ", ")
}
