//go:build darwin

package platform

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	systemLanguageOnce sync.Once
	systemLanguageTag  string
)

// SystemLanguage 取得 macOS 系統語言，例如 zh-Hant-TW、ja-JP。
//
// 由 Finder 啟動的程式通常沒有 LANG，因此先讀系統偏好 `defaults read -g AppleLocale`，
// 只有命令失敗或逾時時才回退環境變數。外部命令有 2 秒上限，避免拖慢啟動。
// 此實作僅交叉編譯驗證，尚未在 macOS 實機執行。
func SystemLanguage() string {
	systemLanguageOnce.Do(func() {
		if tag := appleLocale(); tag != "" {
			systemLanguageTag = tag
			return
		}
		systemLanguageTag = FromEnv(os.Getenv)
	})
	return systemLanguageTag
}

func appleLocale() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleLocale").Output()
	if err != nil {
		return ""
	}
	return NormalizeLocale(strings.TrimSpace(string(output)))
}
