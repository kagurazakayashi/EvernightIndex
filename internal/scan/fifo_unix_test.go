//go:build !windows

package scan

import "syscall"

// mkfifo 建立一個具名管道，用於特殊檔測試。
func mkfifo(path string) error {
	return syscall.Mkfifo(path, 0o644)
}
