//go:build windows

package hash

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// fileIdentity 由已開啟的控制代碼取得「卷 + 檔案標識」。
//
// GetFileInformationByHandle 問的是控制代碼本身，不是路徑，
// 因此「檢查的物件」與「讀取的物件」是同一個控制代碼；
// 格式（win:<8 位大寫十六進位>|<16 位大寫十六進位>）與 fsmeta 相同。
func fileIdentity(file *os.File) (string, bool) {
	if file == nil {
		return "", false
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", false
	}
	fileID := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return fmt.Sprintf("win:%08X|%016X", info.VolumeSerialNumber, fileID), true
}

// pathIdentity 由路徑目前的狀態取得身分，用來發現路徑在讀取期間被替換。
//
// 以可讀屬性開啟後立刻詢問檔案資訊，只讀屬性而不讀內容。
func pathIdentity(path string) (string, bool) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", false
	}
	fileID := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return fmt.Sprintf("win:%08X|%016X", info.VolumeSerialNumber, fileID), true
}
