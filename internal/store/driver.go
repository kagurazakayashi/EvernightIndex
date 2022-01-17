package store

// 這裡單獨匯入 SQLite 驅動，讓 database/sql 完成註冊，
// 而不把驅動別名散布到業務檔案；驅動名稱固定為 "sqlite"。
import _ "modernc.org/sqlite"
