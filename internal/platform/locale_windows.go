//go:build windows

package platform

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	systemLanguageOnce sync.Once
	systemLanguageTag  string
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procUserLocaleName = kernel32.NewProc("GetUserDefaultLocaleName")
	procDefaultUILang  = kernel32.NewProc("GetUserDefaultUILanguage")
)

// SystemLanguage 取得 Windows 使用者介面語言，例如 zh-TW、ja-JP、en-US。
//
// 先使用 GetUserDefaultLocaleName 取得 BCP47 標籤；若不可用，再退回
// GetUserDefaultUILanguage 的 LANGID 並只保留語言層級，讓上層回退英語。
// 未使用 CGO，僅透過 syscall 呼叫系統 DLL。
func SystemLanguage() string {
	systemLanguageOnce.Do(func() {
		const localeNameMaxLength = 85
		buffer := make([]uint16, localeNameMaxLength)
		if written, _, _ := procUserLocaleName.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer))); written > 0 {
			systemLanguageTag = NormalizeLocale(syscall.UTF16ToString(buffer[:written]))
			return
		}
		if langID, _, _ := procDefaultUILang.Call(); langID > 0 {
			systemLanguageTag = languageIDToTag(uint16(langID))
		}
	})
	return systemLanguageTag
}

// languageIDToTag 把 Windows LANGID 縮減成語言層級標籤。
func languageIDToTag(langID uint16) string {
	primary := langID & 0x3ff
	sublanguage := langID >> 10
	switch primary {
	case 0x04: // 中文
		if sublanguage == 0x01 { // 0x0404 繁體中文（台灣）
			return "zh-TW"
		}
		return "zh-CN"
	case 0x11: // 日文
		return "ja-JP"
	case 0x09: // 英文
		return "en-US"
	}
	return ""
}
