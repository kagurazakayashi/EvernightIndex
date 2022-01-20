//go:build windows

package store

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// kernel32 的檔案鎖 API：LockFileEx / UnlockFileEx 由作業系統在行程結束時釋放，
// 因此崩潰不會留下永久鎖。
var (
	procLockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	procUnlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	errorLockViolation      = syscall.Errno(33)
)

// tryLockExclusive 以非阻塞方式嘗試取得檔案排他鎖。
//
// 鎖定範圍由 lockRangeOffset/lockRangeLength 決定，刻意避開存放中繼資料的
// 檔案開頭，讓等待中的行程仍能讀出持有者資訊。
func tryLockExclusive(file *os.File) (bool, error) {
	var overlapped syscall.Overlapped
	overlapped.Offset = uint32(lockRangeOffset)
	overlapped.OffsetHigh = uint32(lockRangeOffset >> 32)
	result, _, callErr := procLockFileEx.Call(
		file.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		uintptr(lockRangeLength),
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result != 0 {
		return true, nil
	}
	if callErr == nil || errors.Is(callErr, syscall.Errno(0)) {
		return false, errors.New("LockFileEx failed without an error code")
	}
	if errors.Is(callErr, errorLockViolation) || errors.Is(callErr, syscall.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, callErr
}

// unlockFile 釋放檔案排他鎖。
func unlockFile(file *os.File) error {
	var overlapped syscall.Overlapped
	overlapped.Offset = uint32(lockRangeOffset)
	overlapped.OffsetHigh = uint32(lockRangeOffset >> 32)
	result, _, callErr := procUnlockFileEx.Call(
		file.Fd(),
		0,
		uintptr(lockRangeLength),
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result == 0 {
		if callErr == nil || errors.Is(callErr, syscall.Errno(0)) {
			return errors.New("UnlockFileEx failed without an error code")
		}
		return callErr
	}
	return nil
}
