//go:build !windows && !darwin

package platform

import "os"

// SystemLanguage 依 LC_ALL、LC_MESSAGES、LANG 判斷系統語言。
func SystemLanguage() string {
	return FromEnv(os.Getenv)
}
