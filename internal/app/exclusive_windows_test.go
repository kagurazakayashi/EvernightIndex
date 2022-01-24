//go:build windows

package app

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// holdExclusiveFile 以「不共享」模式開啟檔案，讓其他行程無法讀取內容。
//
// 這是 Windows 上製造「摘要讀取失敗」最確定的方法：掃描仍可取得屬性，
// 但開啟內容會得到共享違規，因此能驗證摘要失敗的部分完成路徑。
func holdExclusiveFile(t *testing.T, path string) (func(), bool) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Logf("exclusive open failed: %v", err)
		return nil, false
	}
	return func() {
		_ = windows.CloseHandle(handle)
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			return
		}
	}, true
}
