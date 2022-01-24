//go:build linux || darwin

package hash

import (
	"os"
	"strconv"
	"syscall"
)

// fileIdentity 由已開啟的控制代碼取得「卷 + 檔案標識」。
//
// 使用 fstat 而不是再看一次路徑，讓「檢查的物件」與「讀取的物件」是同一個控制代碼；
// 格式必須與 fsmeta.IdentityOf 一致，才能與掃描時記錄的身分直接比較。
func fileIdentity(file *os.File) (string, bool) {
	if file == nil {
		return "", false
	}
	info, err := file.Stat()
	if err != nil {
		return "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return "dev:" + strconv.FormatInt(int64(stat.Dev), 10) + "|" + strconv.FormatInt(int64(stat.Ino), 10), true
}

// pathIdentity 由路徑目前的狀態取得身分，用來發現路徑在讀取期間被替換。
func pathIdentity(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return "dev:" + strconv.FormatInt(int64(stat.Dev), 10) + "|" + strconv.FormatInt(int64(stat.Ino), 10), true
}
