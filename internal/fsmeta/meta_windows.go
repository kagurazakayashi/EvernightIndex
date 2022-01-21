//go:build windows

package fsmeta

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"golang.org/x/sys/windows"
)

// Windows 檔案時間以 1601-01-01 為起點、單位為 100 ns；
// 這個常數是該起點到 1970-01-01 的差值，用來換算成 Unix 奈秒。
const windowsFiletimeEpoch = 116444736000000000

// fileBasicInfo 對應 FILE_BASIC_INFO：四個 100 ns 時間欄位之後才是屬性位。
type fileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

// fileAttributeTagInfo 對應 FILE_ATTRIBUTE_TAG_INFO。
type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

const (
	longPathPrefix = "\\\\?\\"
	devicePrefix   = "\\\\.\\"
	uncPathPrefix  = "\\\\?\\UNC\\"
)

// Collect 採集一個路徑的中繼資料，不跟隨重解析點（junction、symlink）。
//
// 先用 os.Lstat 取得屬性位與時間（一次呼叫），再用只要求 FILE_READ_ATTRIBUTES 的句柄
// 取得卷序列號、檔案 ID、硬連結數與真正的變更時間（FILE_BASIC_INFO.ChangeTime）。
// 這個句柄沒有資料讀取權，所以不會讀到內容，也不會因為檔案被獨佔而阻塞；
// 無法取得句柄時仍保留 Lstat 的結果，並把 Incomplete 標為 true。
func Collect(path string) (Info, error) {
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		Type: classifyGoMode(fileInfo.Mode()),
		Mode: ptrInt64(int64(fileInfo.Mode())),
	}
	if fileInfo.Mode().IsRegular() {
		size := fileInfo.Size()
		info.Size = &size
	}
	if data, ok := fileInfo.Sys().(*syscall.Win32FileAttributeData); ok {
		info.WinAttrs = ptrInt64(int64(data.FileAttributes))
		info.Btime = timestampPointer(filetimeUnixNano(data.CreationTime.LowDateTime, data.CreationTime.HighDateTime))
		info.Atime = timestampPointer(filetimeUnixNano(data.LastAccessTime.LowDateTime, data.LastAccessTime.HighDateTime))
		info.Mtime = timestampPointer(filetimeUnixNano(data.LastWriteTime.LowDateTime, data.LastWriteTime.HighDateTime))
	}

	handle, openErr := openAttributesHandle(path)
	if openErr != nil {
		info.Incomplete = true
		info.IncompleteReason = "attributes_unavailable"
		if info.Type == model.EntryTypeSymlink {
			setLinkTarget(path, &info)
		}
		return info, nil
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var byHandle windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &byHandle); err == nil {
		info.WinVolumeID = ptrString(fmt.Sprintf("%08X", byHandle.VolumeSerialNumber))
		fileID := uint64(byHandle.FileIndexHigh)<<32 | uint64(byHandle.FileIndexLow)
		info.WinFileID = ptrString(fmt.Sprintf("%016X", fileID))
		info.Nlink = ptrInt64(int64(byHandle.NumberOfLinks))
		info.Atime = timestampPointer(filetimeUnixNano(byHandle.LastAccessTime.LowDateTime, byHandle.LastAccessTime.HighDateTime))
		info.Mtime = timestampPointer(filetimeUnixNano(byHandle.LastWriteTime.LowDateTime, byHandle.LastWriteTime.HighDateTime))
		info.Btime = timestampPointer(filetimeUnixNano(byHandle.CreationTime.LowDateTime, byHandle.CreationTime.HighDateTime))
	}

	// FILE_BASIC_INFO 的 ChangeTime 才是中繼資料變更時間；建立時間不得當成 ctime。
	//
	// 必須用結構型別而不是 [N]byte：這些輸出結構含 8 位元組欄位，
	// 核心會以 8 位元組對齊探測目標緩衝區，對齊不足的位元組陣列會回報
	// ERROR_NOACCESS（998）。Go 保證結構欄位依型別對齊，因此用結構指標。
	var basic fileBasicInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err == nil {
		info.Ctime = timestampPointer(win32TicksUnixNano(basic.ChangeTime))
	}

	// FILE_ATTRIBUTE_TAG_INFO 是兩個 uint32：屬性位與重解析點標籤。
	var tagInfo fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&tagInfo)), uint32(unsafe.Sizeof(tagInfo))); err == nil {
		if tagInfo.ReparseTag != 0 {
			info.WinReparseTag = ptrString(fmt.Sprintf("0x%08X", tagInfo.ReparseTag))
			// 任何帶標籤的重解析點都是「會跳轉」的物件：記錄本體，不跟隨。
			info.Type = model.EntryTypeSymlink
		}
	}

	if info.Type == model.EntryTypeSymlink {
		setLinkTarget(path, &info)
	}
	return info, nil
}

// openAttributesHandle 開啟只含屬性存取權的句柄。
//
// FILE_FLAG_OPEN_REPARSE_POINT 讓句柄指向重解析點本身而不是它的目標；
// FILE_FLAG_BACKUP_SEMANTICS 讓目錄也能被開啟。
func openAttributesHandle(path string) (windows.Handle, error) {
	pointer, err := windows.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

// extendedPath 在路徑過長時加上 Win32 長路徑前綴。
//
// Go 的 os 套件內部也做同樣處理；這裡只覆蓋掃描實際會遇到的絕對路徑形式，
// 已帶前綴或相對路徑原樣回傳。
func extendedPath(path string) string {
	if len(path) < 248 {
		return path
	}
	if strings.HasPrefix(path, longPathPrefix) || strings.HasPrefix(path, devicePrefix) {
		return path
	}
	if len(path) >= 2 && path[1] == ':' {
		return longPathPrefix + path
	}
	if len(path) >= 2 && path[0] == '\\' && path[1] == '\\' {
		return uncPathPrefix + path[2:]
	}
	return path
}

// filetimeUnixNano 把 Windows 的兩個 32 位時間欄位組成 Unix 奈秒。
//
// syscall.Filetime 與 windows.Filetime 是兩種型別但版面相同，
// 因此兩邊都拆成高低欄位傳入，避免為了型別轉換而依賴 unsafe。
func filetimeUnixNano(low, high uint32) int64 {
	ticks := int64(uint64(high)<<32 | uint64(low))
	return win32TicksUnixNano(ticks)
}

// win32TicksUnixNano 把 1601 紀元的 100 ns 刻轉成 Unix 奈秒。
func win32TicksUnixNano(ticks int64) int64 { return (ticks - windowsFiletimeEpoch) * 100 }

// timestampPointer 把 Unix 奈秒轉成時間戳指標；零值代表不可得。
func timestampPointer(unixNano int64) *model.Timestamp {
	if unixNano == 0 {
		return nil
	}
	return model.TimestampOrNil(time.Unix(0, unixNano))
}

// defaultCasePolicy 是無法探測時的平台預設值（未探測，不是測量結果）。
func defaultCasePolicy() model.CasePolicy { return model.CaseInsensitive }
