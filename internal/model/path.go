package model

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// 本檔處理路徑的文字表示：「穩定識別」與「顯示文字」必須分開。
//
// 檔案系統名稱不必是合法 UTF-8（Unix 允許任意位元組，Windows 可能帶未配對代理），
// 因此資料庫主鍵與相對路徑一律保留原始位元組，不做有損替換；
// 顯示、記錄與 -v 輸出則走 EscapeDisplay，需要還原時用 UnescapeDisplay。
// 轉義只用於呈現，任何寫回檔案系統的路徑都必須用原始字串。

// LockFileSuffix 是本程式正式資料庫跨行程寫鎖的檔名後綴。
//
// 放在共用模型層的理由：掃描層必須排除鎖檔，但不應該為了這個字串而依賴
// 儲存層（那會把 SQLite 驅動連同整個持久化實作拉進掃描）。儲存層以本常數為唯一來源。
const LockFileSuffix = ".evernightindex.lock"

// escapeHexDigits 是十六進位轉義用的字元表。
const escapeHexDigits = "0123456789ABCDEF"

// EscapeDisplay 把原始名稱轉成單行且可逆的顯示文字。
//
// 規則：
//   - 反斜線寫成 \\，讓轉義序列不會與原本的反斜線混淆；
//   - 控制字元（0x00-0x1F、0x7F）與無效 UTF-8 位元組寫成 \xNN；
//   - 其餘字元（含空白、非 ASCII、以連字號開頭的名稱）原樣保留。
func EscapeDisplay(raw string) string {
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw) + 8)
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRuneInString(raw[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			writeHexEscape(&b, raw[i])
		case r == '\\':
			b.WriteString("\\\\")
		case r < 0x20 || r == 0x7f:
			writeHexEscape(&b, byte(r))
		default:
			b.WriteString(raw[i : i+size])
		}
		i += size
	}
	return b.String()
}

// writeHexEscape 寫入一個 \xNN 形式的位元組轉義。
func writeHexEscape(b *strings.Builder, value byte) {
	b.WriteString("\\x")
	b.WriteByte(escapeHexDigits[value>>4])
	b.WriteByte(escapeHexDigits[value&0x0f])
}

// UnescapeDisplay 還原 EscapeDisplay 產生的文字。
//
// 用於把顯示或日誌中的路徑取回原始位元組；遇到不認得的轉義時回傳錯誤，
// 不猜測、不靜默丟棄位元組。
func UnescapeDisplay(text string) (string, error) {
	if text == "" {
		return "", nil
	}
	if !strings.ContainsRune(text, '\\') {
		return text, nil
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' {
			b.WriteByte(text[i])
			continue
		}
		if i+1 >= len(text) {
			return "", errors.New("escape sequence ends the string")
		}
		switch text[i+1] {
		case '\\':
			b.WriteByte('\\')
			i++
		case 'x':
			if i+3 >= len(text) {
				return "", errors.New("truncated \\xNN escape")
			}
			high, okHigh := hexValue(text[i+2])
			low, okLow := hexValue(text[i+3])
			if !okHigh || !okLow {
				return "", errors.New("invalid \\xNN escape")
			}
			b.WriteByte(high<<4 | low)
			i += 3
		default:
			return "", errors.New("unknown escape sequence")
		}
	}
	return b.String(), nil
}

// hexValue 把一個十六進位字元轉成數值。
func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}
