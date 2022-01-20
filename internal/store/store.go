// Package store 封裝 SQLite 檔案：開啟、格式識別與遷移、交易邊界、
// 暫存與發佈、跨行程寫入鎖、一致性備份與完整性檢查。
//
// 選型依據（modernc.org/sqlite）：
//   - 純 Go 實作，CGO_ENABLED=0 即可交叉編譯 Windows/macOS/Linux；
//   - 支援交易提交與回滾、context 取消（中斷查詢後連線仍可用）；
//   - 具備一致性備份途徑（VACUUM INTO 在同一個讀交易內產生快照，
//     不需要裸複製可能遺漏 WAL 內容的主檔）。
//
// 寫入邊界：正式資料庫的所有寫入都必須先取得同一目標的跨行程鎖（見 lock.go）
// 再經由 OpenTarget 開啟；Open 只作為底層原語，供唯讀與測試使用。
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

const (
	// ApplicationID 是寫入 PRAGMA application_id 的值，用來識別本程式建立的資料庫。
	// 0x45564E49 對應 ASCII 的 "EVNI"。
	ApplicationID int64 = 0x45564E49
	// SchemaVersion 是目前支援的格式版本，同時寫入 PRAGMA user_version。
	//
	// 版本 1 只有 store_meta；版本 2 加入綁定根目錄、執行、條目、暫存條目、
	// 變化事件與待匯出狀態。舊版只允許明確支援的版本升級，未知結構一律拒絕。
	SchemaVersion int64 = 2
)

// Options 控制開啟行為。
type Options struct {
	// Create 允許在檔案不存在時建立資料庫。
	Create bool
	// ReadOnly 以唯讀模式開啟：查詢類命令不得建立或修改資料庫。
	ReadOnly bool
	// BusyTimeout 是等待其他寫入者釋放 SQLite 鎖的最長時間，0 表示不等待。
	BusyTimeout time.Duration
}

// Store 是開啟後的資料庫句柄。
//
// 連線池固定為單一連線：CLI 每次只做一項任務，單連線讓寫入序列化、
// 交易邊界與 SQLite 的單寫者語義一致，也避免多連線同時持有讀交易。
type Store struct {
	db   *sql.DB
	path string
}

// Open 開啟或建立資料庫，並在必要時寫入識別、版本與結構。
//
// 流程：檔案是否存在 → 建立連線 → 識別 application_id 與 user_version →
// 新檔則初始化，既有檔則驗證或遷移。非本程式建立、版本過新、結構不明或
// 損壞的檔案一律拒絕，不覆蓋。
//
// 正式寫入請改用 OpenTarget，讓所有寫入口都經過同一把跨行程鎖。
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errDatabaseIO(errors.New("empty database path"))
	}
	if opts.ReadOnly && opts.Create {
		return nil, errDatabaseIO(errors.New("read-only open cannot create a database"))
	}
	exists := false
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		if info.IsDir() {
			return nil, errDatabaseIO(fmt.Errorf("database path is a directory: %s", path))
		}
		exists = true
	case errors.Is(statErr, os.ErrNotExist):
		exists = false
	default:
		return nil, errDatabaseIO(statErr)
	}
	if !exists && !opts.Create {
		return nil, model.New(model.CodeDatabaseMissing, "err.databaseMissing", model.ExitFatal, map[string]string{"path": path})
	}

	db, err := sql.Open("sqlite", dataSourceName(path, opts))
	if err != nil {
		return nil, databaseError(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, databaseError(err)
	}
	s := &Store{db: db, path: path}
	// 空檔案不算資料庫：只有明確允許建立時才初始化，否則回報缺少資料庫，
	// 避免在要求唯讀或只查詢時把別人的空檔案變成新資料庫。
	if !exists || (info != nil && info.Size() == 0) {
		if !opts.Create {
			_ = db.Close()
			return nil, model.New(model.CodeDatabaseMissing, "err.databaseMissing", model.ExitFatal, map[string]string{"path": path})
		}
		if err := s.initialize(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
		return s, nil
	}
	if err := s.verify(ctx, opts); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// OpenTarget 是正式資料庫的唯一寫入開啟入口。
//
// 它要求呼叫端已持有同一目標的跨行程寫入鎖，避免出現繞過鎖的寫入路徑；
// 鎖與路徑不一致時直接拒絕，避免把工作副本鎖當成正式目標鎖使用。
func OpenTarget(ctx context.Context, path string, opts Options, lock *Lock) (*Store, error) {
	if opts.ReadOnly {
		return nil, errDatabaseIO(errors.New("OpenTarget is for writable access; use Open with ReadOnly"))
	}
	if lock == nil {
		return nil, model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "writable open requires the formal database lock",
		})
	}
	if !lock.matches(path) {
		return nil, model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "lock target " + lock.dbPath + " does not match " + path,
		})
	}
	return Open(ctx, path, opts)
}

// Path 回傳開啟時的資料庫路徑。
func (s *Store) Path() string { return s.path }

// Close 關閉資料庫連線。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// WithTx 在單一交易內執行 fn：fn 回傳錯誤時回滾，否則提交。
//
// 取消、崩潰或錯誤都不得留下半次更新；因此提交失敗時同樣嘗試回滾，
// 讓呼叫端只看到「已提交」或「未提交」兩種狀態。
func (s *Store) WithTx(ctx context.Context, fn func(context.Context, *sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return databaseError(err)
	}
	succeeded := false
	defer func() {
		if succeeded {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) && err == nil {
			err = databaseError(rollbackErr)
		}
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return databaseError(err)
	}
	succeeded = true
	return nil
}

// Backup 以一致性快照把目前資料庫寫成 dst。
//
// 使用 VACUUM INTO：SQLite 在單一讀交易內產生目標檔，因此不含未提交的變更，
// 也不會像裸複製主檔那樣遺漏仍留在 WAL 的資料。dst 不得已存在；
// 先寫入同目錄暫存檔再更名，避免中途失敗留下半成品。
func (s *Store) Backup(ctx context.Context, dst string) error {
	if strings.TrimSpace(dst) == "" {
		return errDatabaseIO(errors.New("empty backup path"))
	}
	if _, err := os.Stat(dst); err == nil {
		return errDatabaseIO(fmt.Errorf("backup target already exists: %s", dst))
	} else if !errors.Is(err, os.ErrNotExist) {
		return errDatabaseIO(err)
	}
	suffix, err := randomHex(8)
	if err != nil {
		return errDatabaseIO(err)
	}
	tmp := dst + ".tmp-" + suffix
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", filepath.ToSlash(tmp)); err != nil {
		_ = os.Remove(tmp)
		return databaseError(err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return errDatabaseIO(err)
	}
	return nil
}

// IntegrityCheck 對目前資料庫執行完整性檢查。
//
// 完整檢查會讀過整個檔案，成本與資料庫大小成正比，因此不在每次 Open 執行，
// 而是留給寫回正式庫前的驗證與 recovery 使用。
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return databaseError(err)
	}
	if result != "ok" {
		return model.New(model.CodeDatabaseCorrupt, "err.databaseCorrupt", model.ExitFatal, map[string]string{"path": s.path})
	}
	return nil
}

// Meta 讀取 store_meta 中的單一值。
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key = ?", key).Scan(&value)
	if err != nil {
		return "", databaseError(err)
	}
	return value, nil
}

// NewRunID 產生一次執行的識別碼（RFC4122 第 4 版 UUID）。
//
// 執行 ID 由呼叫端在建鎖之前產生，讓鎖資訊能記錄持有者。
func NewRunID() (string, error) {
	id, err := randomUUID()
	if err != nil {
		return "", errDatabaseIO(err)
	}
	return id, nil
}

// initialize 為新檔寫入識別、版本與完整結構。
func (s *Store) initialize(ctx context.Context) error {
	uuid, err := randomUUID()
	if err != nil {
		return errDatabaseIO(err)
	}
	return s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range []string{
			"PRAGMA application_id = " + strconv.FormatInt(ApplicationID, 10),
			"PRAGMA user_version = " + strconv.FormatInt(SchemaVersion, 10),
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return databaseError(err)
			}
		}
		for _, stmt := range schemaDDL() {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return databaseError(err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO store_meta(key, value) VALUES (?, ?), (?, ?)",
			"schema_version", strconv.FormatInt(SchemaVersion, 10),
			"db_uuid", uuid,
		); err != nil {
			return databaseError(err)
		}
		return nil
	})
}

// verify 檢查既有資料庫是否為本程式支援的檔案，必要時執行明確遷移。
func (s *Store) verify(ctx context.Context, opts Options) error {
	var applicationID, userVersion int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return databaseError(err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return databaseError(err)
	}
	tables, err := s.userTables(ctx)
	if err != nil {
		return err
	}
	switch {
	case applicationID == ApplicationID:
	case applicationID == 0 && len(tables) == 0 && opts.Create:
		return s.initialize(ctx)
	default:
		return model.New(model.CodeDatabaseForeign, "err.databaseForeign", model.ExitFatal, map[string]string{"path": s.path})
	}
	switch {
	case userVersion == SchemaVersion:
		return s.requireSchema(ctx)
	case userVersion < SchemaVersion:
		if opts.ReadOnly {
			return model.New(model.CodeDatabaseVersion, "err.databaseVersion", model.ExitFatal, map[string]string{
				"path":      s.path,
				"found":     strconv.FormatInt(userVersion, 10),
				"supported": strconv.FormatInt(SchemaVersion, 10),
			})
		}
		return s.migrate(ctx, userVersion)
	default:
		return model.New(model.CodeDatabaseTooNew, "err.databaseTooNew", model.ExitFatal, map[string]string{
			"path":      s.path,
			"found":     strconv.FormatInt(userVersion, 10),
			"supported": strconv.FormatInt(SchemaVersion, 10),
		})
	}
}

// migrate 只遷移明確支援的舊版本；其他版本一律回報不受支援，不自動重建。
func (s *Store) migrate(ctx context.Context, from int64) error {
	switch from {
	case 1:
		return s.migrateFromV1(ctx)
	default:
		return model.New(model.CodeDatabaseVersion, "err.databaseVersion", model.ExitFatal, map[string]string{
			"path":      s.path,
			"found":     strconv.FormatInt(from, 10),
			"supported": strconv.FormatInt(SchemaVersion, 10),
		})
	}
}

// migrateFromV1 把只有識別表的版本 1 升級到版本 2。
//
// 版本 1 沒有任何業務資料，因此遷移只建立新結構並更新版本；
// 若檔案內有版本 1 不認識的表，代表結構被改寫或來自其他程式，
// 此時拒絕遷移，避免覆蓋未知結構。
func (s *Store) migrateFromV1(ctx context.Context) error {
	tables, err := s.userTables(ctx)
	if err != nil {
		return err
	}
	if !sameNameSet(tables, v1Tables) {
		return unknownSchema(s.path, missingNames(requiredTables, tables))
	}
	return s.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, stmt := range schemaDDL() {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return databaseError(err)
			}
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.FormatInt(SchemaVersion, 10)); err != nil {
			return databaseError(err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO store_meta(key, value) VALUES ('schema_version', ?) "+
				"ON CONFLICT(key) DO UPDATE SET value = excluded.value",
			strconv.FormatInt(SchemaVersion, 10),
		); err != nil {
			return databaseError(err)
		}
		return nil
	})
}

// requireSchema 確認目前版本標示的結構確實完整。
//
// 版本號正確但缺表或識別值不一致，代表檔案被截斷、複製不完整或結構被改寫，
// 一律視為無法辨識的結構，不嘗試自動修補。
func (s *Store) requireSchema(ctx context.Context) error {
	tables, err := s.userTables(ctx)
	if err != nil {
		return err
	}
	if missing := missingNames(requiredTables, tables); len(missing) > 0 {
		return unknownSchema(s.path, missing)
	}
	var recorded string
	err = s.db.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key = 'schema_version'").Scan(&recorded)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return unknownSchema(s.path, []string{"store_meta.schema_version"})
	case err != nil:
		return databaseError(err)
	case recorded != strconv.FormatInt(SchemaVersion, 10):
		return unknownSchema(s.path, []string{"store_meta.schema_version"})
	}
	return nil
}

// userTables 回傳資料庫中的使用者表名（已排序）。
func (s *Store) userTables(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, databaseError(err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, databaseError(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(err)
	}
	return names, nil
}

// unknownSchema 建立「結構不明」錯誤，列出缺少的表作為診斷依據。
func unknownSchema(path string, missing []string) error {
	sort.Strings(missing)
	return model.New(model.CodeDatabaseUnknownSchema, "err.databaseUnknownSchema", model.ExitFatal, map[string]string{
		"path":    path,
		"missing": strings.Join(missing, ", "),
	})
}

// missingNames 回傳 want 中不存在於 have 的名稱。
func missingNames(want, have []string) []string {
	present := make(map[string]bool, len(have))
	for _, name := range have {
		present[name] = true
	}
	var missing []string
	for _, name := range want {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// sameNameSet 比較兩組表名是否完全相同（忽略順序）。
func sameNameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// dataSourceName 組出 SQLite URI：保留磁碟機代號與分隔符，只轉義會破壞 URI 的字元。
func dataSourceName(path string, opts Options) string {
	values := url.Values{}
	values.Add("_pragma", "busy_timeout("+strconv.FormatInt(opts.BusyTimeout.Milliseconds(), 10)+")")
	values.Add("_pragma", "foreign_keys(1)")
	if opts.ReadOnly {
		// 唯讀連線不得設定日誌模式：切換 WAL 需要寫入資料庫標頭，
		// 在 mode=ro 下會直接失敗（也會改動別人的檔案）。
		values.Set("mode", "ro")
	} else {
		values.Add("_pragma", "journal_mode(WAL)")
		values.Add("_pragma", "synchronous(NORMAL)")
	}
	escaper := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	return "file:" + escaper.Replace(filepath.ToSlash(path)) + "?" + values.Encode()
}

// corruptionMarkers 是驅動回報「檔案不是資料庫」時的典型訊息片段。
var corruptionMarkers = []string{
	"file is not a database",
	"not a database",
	"database disk image is malformed",
	"malformed",
	"file is encrypted",
}

// databaseError 依驅動訊息把損壞與一般讀寫失敗分開，讓呼叫端能給出正確診斷。
func databaseError(cause error) error {
	if cause == nil {
		return nil
	}
	lowered := strings.ToLower(cause.Error())
	for _, marker := range corruptionMarkers {
		if strings.Contains(lowered, marker) {
			return model.Wrap(model.CodeDatabaseCorrupt, "err.databaseCorrupt", model.ExitFatal,
				map[string]string{"detail": cause.Error()}, cause)
		}
	}
	return model.Wrap(model.CodeDatabaseIO, "err.databaseIO", model.ExitFatal, map[string]string{"detail": cause.Error()}, cause)
}

// errDatabaseIO 建立資料庫讀寫錯誤；detail 保留原始系統訊息以便診斷。
func errDatabaseIO(cause error) error {
	return model.Wrap(model.CodeDatabaseIO, "err.databaseIO", model.ExitFatal, map[string]string{"detail": cause.Error()}, cause)
}

// randomHex 產生 n 個隨機位元組的十六進位字串，用於暫存檔名。
func randomHex(n int) (string, error) {
	buffer := make([]byte, n)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// randomUUID 產生 RFC4122 第 4 版格式的識別碼（純十六進位，無外部相依）。
func randomUUID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	canonical := hex.EncodeToString(buffer)
	return canonical[0:8] + "-" + canonical[8:12] + "-" + canonical[12:16] + "-" + canonical[16:20] + "-" + canonical[20:32], nil
}
