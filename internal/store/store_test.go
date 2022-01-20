package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// verifySQLiteDriver 是本步驟的技術驗證：建庫、提交、回滾、一致性備份、
// 取消與平台編譯都必須在真正實作業務前確認可行。
//
// 測試資料全部位於 t.TempDir()，不接觸使用者資料。

// newStore 在測試專屬目錄建立資料庫；路徑刻意包含空白與中日文字元，
// 用來驗證 SQLite URI 轉義沒有破壞路徑。
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "索引 測試")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "index.db")
	store, err := Open(context.Background(), path, Options{Create: true, BusyTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func createEntryTable(t *testing.T, store *Store) {
	t.Helper()
	err := store.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE test_entry(id INTEGER PRIMARY KEY, path TEXT NOT NULL)")
		return err
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func insertEntry(t *testing.T, store *Store, path string) {
	t.Helper()
	err := store.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO test_entry(path) VALUES (?)", path)
		return err
	})
	if err != nil {
		t.Fatalf("insert %q: %v", path, err)
	}
}

func countEntries(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), "SELECT count(*) FROM test_entry").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

func requireCode(t *testing.T, err error, want model.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var appErr *model.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *model.Error, got %T: %v", err, err)
	}
	if appErr.Code != want {
		t.Fatalf("code = %s, want %s (%v)", appErr.Code, want, err)
	}
}

// TestOpenCreateWritesIdentity 驗證新建資料庫會寫入識別、版本與 UUID，並使用 WAL。
func TestOpenCreateWritesIdentity(t *testing.T) {
	store, path := newStore(t)
	ctx := context.Background()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file missing: %v", err)
	}
	uuid, err := store.Meta(ctx, "db_uuid")
	if err != nil || uuid == "" {
		t.Fatalf("db_uuid = %q, err = %v", uuid, err)
	}
	version, err := store.Meta(ctx, "schema_version")
	if err != nil || version != strconv.FormatInt(SchemaVersion, 10) {
		t.Fatalf("schema_version = %q, err = %v, want %d", version, err, SchemaVersion)
	}

	var applicationID, userVersion int64
	if err := store.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		t.Fatalf("read application_id: %v", err)
	}
	if applicationID != ApplicationID {
		t.Fatalf("application_id = %d, want %d", applicationID, ApplicationID)
	}
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if userVersion != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", userVersion, SchemaVersion)
	}
	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
}

// TestCommitAndRollback 驗證交易提交與回滾的可見性。
func TestCommitAndRollback(t *testing.T) {
	store, _ := newStore(t)
	createEntryTable(t, store)
	insertEntry(t, store, "committed.txt")
	if got := countEntries(t, store); got != 1 {
		t.Fatalf("after commit: count = %d, want 1", got)
	}

	sentinel := errors.New("rollback on purpose")
	err := store.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO test_entry(path) VALUES ('phantom.txt')"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx must return the callback error, got %v", err)
	}
	if got := countEntries(t, store); got != 1 {
		t.Fatalf("after rollback: count = %d, want 1 (the rolled-back row must be invisible)", got)
	}
}

// TestBackupIsConsistentSnapshot 驗證一致性備份：未提交的交易不得出現在快照中。
func TestBackupIsConsistentSnapshot(t *testing.T) {
	store, path := newStore(t)
	createEntryTable(t, store)
	insertEntry(t, store, "kept.txt")

	backupPath := filepath.Join(filepath.Dir(path), "copy.db")
	if err := store.Backup(context.Background(), backupPath); err != nil {
		t.Fatalf("backup: %v", err)
	}

	backupStore, err := Open(context.Background(), backupPath, Options{BusyTimeout: time.Second})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = backupStore.Close() }()
	if got := countEntries(t, backupStore); got != 1 {
		t.Fatalf("backup rows = %d, want 1", got)
	}
	if err := backupStore.IntegrityCheck(context.Background()); err != nil {
		t.Fatalf("backup integrity: %v", err)
	}
	uuid, err := backupStore.Meta(context.Background(), "db_uuid")
	if err != nil || uuid == "" {
		t.Fatalf("backup lost its identity: uuid = %q, err = %v", uuid, err)
	}

	// 備份目標已存在時必須拒絕，避免覆蓋既有檔案。
	if err := store.Backup(context.Background(), backupPath); err == nil {
		t.Fatal("backup must refuse to overwrite an existing target")
	}
}

// TestCancellationInterruptsQuery 驗證 context 取消能中斷長時間查詢，且連線之後仍可用。
func TestCancellationInterruptsQuery(t *testing.T) {
	store, _ := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	var count int
	err := store.db.QueryRowContext(ctx,
		"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 200000000) SELECT count(*) FROM c",
	).Scan(&count)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}

	var unusable int
	if err := store.db.QueryRowContext(context.Background(), "SELECT 1").Scan(&unusable); err != nil {
		t.Fatalf("connection unusable after cancellation: %v", err)
	}
}

// TestIntegrityCheck 驗證完整性檢查對正常資料庫回報成功。
func TestIntegrityCheck(t *testing.T) {
	store, _ := newStore(t)
	createEntryTable(t, store)
	if err := store.IntegrityCheck(context.Background()); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
}

// TestRejectsForeignDatabase 驗證非本程式建立的資料庫不被接受。
func TestRejectsForeignDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE other(value TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	_ = raw.Close()

	_, err = Open(context.Background(), path, Options{Create: true})
	requireCode(t, err, model.CodeDatabaseForeign)
}

// TestRejectsNewerVersion 驗證格式版本較新的資料庫不被降級處理。
func TestRejectsNewerVersion(t *testing.T) {
	store, path := newStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	_ = store.Close()

	_, err := Open(ctx, path, Options{Create: true})
	requireCode(t, err, model.CodeDatabaseTooNew)
}

// TestMissingDatabase 驗證缺少資料庫時回報明確錯誤碼。
func TestMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "index.db")
	_, err := Open(context.Background(), path, Options{})
	requireCode(t, err, model.CodeDatabaseMissing)
}

// TestEmptyFileIsNotADatabase 驗證空檔案不會在未要求建立時被初始化。
func TestEmptyFileIsNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	_, err := Open(context.Background(), path, Options{})
	requireCode(t, err, model.CodeDatabaseMissing)

	created, err := Open(context.Background(), path, Options{Create: true})
	if err != nil {
		t.Fatalf("create over an empty file: %v", err)
	}
	if err := created.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestReadOnlyRejectsWrites 驗證唯讀開啟不會建立或修改資料庫。
func TestReadOnlyRejectsWrites(t *testing.T) {
	store, path := newStore(t)
	createEntryTable(t, store)
	_ = store.Close()

	readOnly, err := Open(context.Background(), path, Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer func() { _ = readOnly.Close() }()

	if _, err := readOnly.Meta(context.Background(), "db_uuid"); err != nil {
		t.Fatalf("read-only query failed: %v", err)
	}
	err = readOnly.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO test_entry(path) VALUES ('nope.txt')")
		return err
	})
	if err == nil {
		t.Fatal("read-only store must reject writes")
	}
}

// TestOpenRejectsDirectory 驗證把目錄當資料庫路徑時立即失敗。
func TestOpenRejectsDirectory(t *testing.T) {
	_, err := Open(context.Background(), t.TempDir(), Options{Create: true})
	if err == nil {
		t.Fatal("expected an error when the database path is a directory")
	}
}
