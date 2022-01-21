package scan

import (
	"path/filepath"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔處理「程式自己產生的檔案」的排除清單。
//
// 原則：只排除明確屬於本程式的檔案與本次執行專屬的目錄。
// 不能因為「它在臨時父目錄裡」就整個父目錄排除，那會吞掉使用者放在旁邊的檔案；
// 也不能把所有 *.db 一概排除，其他使用者的資料庫仍應被索引。

// SelfArtifacts 由正式資料庫路徑推導本程式自產的檔案。
//
// 包含資料庫主檔、SQLite 的三個輔助檔（回滾日誌、WAL、共享記憶體索引）與跨行程鎖檔。
// 這些都是「檔案本身」，不是整個目錄，因此同目錄下的其他檔案照常被掃描。
func SelfArtifacts(dbPath string) []string {
	clean := dbPath
	if abs, err := filepath.Abs(dbPath); err == nil {
		clean = abs
	} else {
		clean = filepath.Clean(dbPath)
	}
	return []string{
		clean,
		clean + "-journal",
		clean + "-wal",
		clean + "-shm",
		clean + model.LockFileSuffix,
	}
}
