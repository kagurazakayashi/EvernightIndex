//go:build unix

package store

import (
	"errors"
	"os"
	"syscall"
)

// tryLockExclusive 以非阻塞方式嘗試取得檔案排他鎖。
//
// 回傳 (false, nil) 表示鎖目前由其他行程持有，呼叫端應等待重試；
// 真正的錯誤（權限、檔案系統不支援等）才回傳 error。
func tryLockExclusive(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EINTR):
		return false, nil
	default:
		return false, err
	}
}

// unlockFile 釋放檔案排他鎖。
func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
