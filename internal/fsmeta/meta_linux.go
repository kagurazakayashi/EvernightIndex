//go:build linux

package fsmeta

import (
	"os"
	"syscall"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"golang.org/x/sys/unix"
)

// Collect 採集一個路徑的中繼資料，不跟隨符號連結。
//
// 主要欄位來自一次 lstat；建立時間另外用 statx 取得（Linux 4.11 起），
// 核心或檔案系統不提供時留 nil，不退回 ctime 冒充建立時間。
// FIFO、socket 與裝置檔只做 lstat，不會被開啟，因此不會阻塞。
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
		info.Atime = timestampFromTimespec(stat.Atim.Sec, stat.Atim.Nsec)
		info.Mtime = timestampFromTimespec(stat.Mtim.Sec, stat.Mtim.Nsec)
		info.Ctime = timestampFromTimespec(stat.Ctim.Sec, stat.Ctim.Nsec)
	}
	if birth, ok := statxBirthTime(path); ok {
		info.Btime = &birth
	}
	if info.Type == model.EntryTypeSymlink {
		setLinkTarget(path, &info)
	}
	return info, nil
}

// statxBirthTime 用 statx 讀取建立時間。
//
// 舊核心（無 statx）、被 seccomp 擋下，或檔案系統不記錄 btime 時一律視為不可得：
// 這不是掃描失敗，只是能力受限，因此不回傳錯誤。
func statxBirthTime(path string) (model.Timestamp, bool) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BTIME, &stx); err != nil {
		return "", false
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return "", false
	}
	if stx.Btime.Sec == 0 && stx.Btime.Nsec == 0 {
		return "", false
	}
	return model.NewTimestamp(time.Unix(stx.Btime.Sec, int64(stx.Btime.Nsec))), true
}

// timestampFromTimespec 把秒與奈秒轉成時間戳指標。
func timestampFromTimespec(sec, nsec int64) *model.Timestamp {
	return model.TimestampOrNil(time.Unix(sec, nsec))
}

// defaultCasePolicy 是無法探測時的平台預設值（未探測，不是測量結果）。
func defaultCasePolicy() model.CasePolicy { return model.CaseSensitive }
