//go:build darwin

package fsmeta

import (
	"os"
	"syscall"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// Collect 採集一個路徑的中繼資料，不跟隨符號連結。
//
// macOS 的 lstat 直接提供建立時間（Birthtimespec）與 BSD 檔案旗標（Flags），
// 因此不需要額外系統呼叫；Flags 保留原始位元，不用 Go 模式推測。
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
	if stat, ok := fileInfo.Sys().(*syscall.Stat_t); ok {
		info.UID = ptrInt64(int64(stat.Uid))
		info.GID = ptrInt64(int64(stat.Gid))
		info.DeviceID = ptrInt64(int64(stat.Dev))
		info.Inode = ptrInt64(int64(stat.Ino))
		info.Nlink = ptrInt64(int64(stat.Nlink))
		info.Atime = timestampFromTimespec(stat.Atimespec.Sec, stat.Atimespec.Nsec)
		info.Mtime = timestampFromTimespec(stat.Mtimespec.Sec, stat.Mtimespec.Nsec)
		info.Ctime = timestampFromTimespec(stat.Ctimespec.Sec, stat.Ctimespec.Nsec)
		info.Btime = timestampFromTimespec(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
		info.BSDFlags = ptrInt64(int64(stat.Flags))
	}
	if info.Type == model.EntryTypeSymlink {
		setLinkTarget(path, &info)
	}
	return info, nil
}

// timestampFromTimespec 把秒與奈秒轉成時間戳指標；全零代表平台沒有提供。
func timestampFromTimespec(sec, nsec int64) *model.Timestamp {
	if sec == 0 && nsec == 0 {
		return nil
	}
	return model.TimestampOrNil(time.Unix(sec, nsec))
}

// defaultCasePolicy 是無法探測時的平台預設值（未探測，不是測量結果）。
func defaultCasePolicy() model.CasePolicy { return model.CaseInsensitive }
