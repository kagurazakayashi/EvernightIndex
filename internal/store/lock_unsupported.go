//go:build !unix && !windows

package store

import "os"

// tryLockExclusive 在沒有跨行程檔案鎖實作的平台上直接回報不支援，
// 而不是以「看起來成功」的假鎖讓多個寫入者同時進行。
func tryLockExclusive(*os.File) (bool, error) { return false, errLockUnsupported }

// unlockFile 在不支援的平台上沒有可釋放的鎖。
func unlockFile(*os.File) error { return nil }
