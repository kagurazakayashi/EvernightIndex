//go:build !linux && !darwin && !windows

package hash

import "os"

// fileIdentity 在沒有檔案標識介面的平台上明確回報「不可得」。
//
// 寧可少一個穩定性檢查，也不要用 inode 之外的猜測冒充身分。
func fileIdentity(_ *os.File) (string, bool) { return "", false }

// pathIdentity 同上：平台沒有可靠標識時不做路徑替換判定。
func pathIdentity(_ string) (string, bool) { return "", false }
