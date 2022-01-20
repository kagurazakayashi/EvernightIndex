package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔驗證結構本身：欄位定義一致、必要表與索引存在、唯一鍵與 CHECK 生效，
// 以及版本遷移與拒絕規則。全部使用測試專屬目錄，不接觸使用者資料。

// applicationIDLiteral 是 PRAGMA application_id 的十進位值，由常數推導。
var applicationIDLiteral = strconv.FormatInt(ApplicationID, 10)

// tableColumns 讀回資料表的欄位名（依建立順序）。
func tableColumns(t *testing.T, store *Store, table string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("read columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	return names
}

// TestSchemaColumnsMatchDeclarations 確認條目欄位定義與 entry、entry_stage 一致。
//
// 發佈交易以 INSERT ... SELECT 在兩表之間搬移資料，欄位名一旦漂移就會
// 安靜地搬錯值，因此這裡把順序也一起鎖住。
func TestSchemaColumnsMatchDeclarations(t *testing.T) {
	store, _ := newStore(t)
	want := make([]string, len(entryColumns))
	for index, column := range entryColumns {
		want[index] = column.name
	}

	entry := tableColumns(t, store, "entry")
	if len(entry) == 0 || entry[0] != "root_id" {
		t.Fatalf("entry columns = %v, want root_id first", entry)
	}
	if got := strings.Join(entry[1:], ","); got != strings.Join(want, ",") {
		t.Errorf("entry columns do not match declarations: got %s, want %s", got, strings.Join(want, ","))
	}

	stage := tableColumns(t, store, "entry_stage")
	if len(stage) < 3 || stage[0] != "run_id" || stage[1] != "path_key" || stage[2] != "op" {
		t.Fatalf("entry_stage columns = %v, want run_id, path_key, op first", stage)
	}
	valueNames := entryValueNames()
	if got := strings.Join(stage[3:], ","); got != strings.Join(valueNames, ",") {
		t.Errorf("entry_stage columns do not match declarations: got %s, want %s", got, strings.Join(valueNames, ","))
	}
}

// TestRequiredTablesAndIndexesExist 確認必要表與查詢索引都存在。
func TestRequiredTablesAndIndexesExist(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	tables := map[string]bool{}
	indexes := map[string]bool{}
	rows, err := store.db.QueryContext(ctx, "SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		switch kind {
		case "table":
			tables[name] = true
		case "index":
			indexes[name] = true
		}
	}
	for _, name := range requiredTables {
		if !tables[name] {
			t.Errorf("missing table %s", name)
		}
	}
	for _, name := range []string{"idx_entry_name", "idx_entry_size", "idx_entry_mtime", "idx_entry_sha256", "idx_run_started", "idx_export_state"} {
		if !indexes[name] {
			t.Errorf("missing index %s", name)
		}
	}
}

// TestSingleRootConstraint 確認一個資料庫只能綁定一個根目錄。
func TestSingleRootConstraint(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	if _, err := store.BindRoot(ctx, model.RootSpec{AbsPath: "C:/root", SourcePlatform: "windows", CasePolicy: model.CaseInsensitive}); err != nil {
		t.Fatalf("bind root: %v", err)
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO root_dir(id, abs_path, path_key, source_platform, case_policy, bound_at_utc)
		VALUES (2, 'C:/other', 'c:/other', 'windows', 'insensitive', '2026-01-01T00:00:00.000000000Z')`)
	if err == nil {
		t.Fatal("a second bound root must be rejected")
	}
}

// TestCheckConstraintsRejectInvalidRows 確認結構層阻擋不一致的條目組合。
func TestCheckConstraintsRejectInvalidRows(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	runID := "run-check"
	if _, err := store.BindRoot(ctx, model.RootSpec{AbsPath: "C:/root", SourcePlatform: "windows", CasePolicy: model.CaseInsensitive}); err != nil {
		t.Fatalf("bind root: %v", err)
	}
	if _, err := store.StartRun(ctx, RunSpec{RunID: runID, RootID: 1, Mode: model.RunModeCreate, ParamsJSON: "{}"}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	base := `INSERT INTO entry(root_id, path_key, rel_path, abs_path, name, parent_rel, entry_type, size_bytes,
		hash_sha256, hash_state, status, first_seen_run, last_seen_run, last_seen_at_utc)
		VALUES (1, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)`
	const stamp = "2026-01-01T00:00:00.000000000Z"
	cases := []struct {
		name  string
		args  []any
		label string
	}{
		{"unknown type", []any{"a", "a", "C:/root/a", "a", "folder", nil, nil, "not_computed", "present", runID, runID, stamp}, "entry_type"},
		{"size on directory", []any{"b", "b", "C:/root/b", "b", "dir", int64(5), nil, "not_applicable", "present", runID, runID, stamp}, "size_bytes"},
		{"digest without computed state", []any{"c", "c", "C:/root/c", "c", "file", int64(1), "abc", "failed", "present", runID, runID, stamp}, "hash_state"},
		{"unknown status", []any{"d", "d", "C:/root/d", "d", "file", int64(1), nil, "not_computed", "deleted", runID, runID, stamp}, "status"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := store.db.ExecContext(ctx, base, testCase.args...); err == nil {
				t.Fatalf("%s must be rejected by a CHECK constraint", testCase.label)
			}
		})
	}
}

// writeV1Database 建立一個只含識別表的格式版本 1 資料庫。
func writeV1Database(t *testing.T, path string, extraStatements ...string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	statements := []string{
		"PRAGMA application_id = " + applicationIDLiteral,
		"PRAGMA user_version = 1",
		"CREATE TABLE store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID",
		"INSERT INTO store_meta(key, value) VALUES ('schema_version', '1'), ('db_uuid', 'v1-uuid')",
	}
	statements = append(statements, extraStatements...)
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("exec %q: %v", statement, err)
		}
	}
}

// TestMigrationFromVersion1 確認支援的舊版本可升級，且識別資訊保留。
func TestMigrationFromVersion1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	writeV1Database(t, path)

	store, err := Open(context.Background(), path, Options{Create: true})
	if err != nil {
		t.Fatalf("open v1 database: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	var userVersion int64
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if userVersion != SchemaVersion {
		t.Errorf("user_version = %d, want %d", userVersion, SchemaVersion)
	}
	uuid, err := store.Meta(ctx, "db_uuid")
	if err != nil || uuid != "v1-uuid" {
		t.Errorf("db_uuid = %q, err = %v; migration must preserve identity", uuid, err)
	}
	if _, err := store.BindRoot(ctx, model.RootSpec{AbsPath: "C:/root", SourcePlatform: "windows", CasePolicy: model.CaseInsensitive}); err != nil {
		t.Errorf("migrated database must accept a root binding: %v", err)
	}
}

// TestMigrationRefusesUnknownVersion1Schema 確認不自動遷移未知結構。
func TestMigrationRefusesUnknownVersion1Schema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1-extra.db")
	writeV1Database(t, path, "CREATE TABLE mystery(value TEXT)")

	_, err := Open(context.Background(), path, Options{Create: true})
	requireCode(t, err, model.CodeDatabaseUnknownSchema)
}

// TestReadOnlyRefusesMigration 確認唯讀開啟不會遷移舊版資料庫。
func TestReadOnlyRefusesMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1-ro.db")
	writeV1Database(t, path)

	_, err := Open(context.Background(), path, Options{ReadOnly: true})
	requireCode(t, err, model.CodeDatabaseVersion)
}

// TestRejectsIncompleteCurrentSchema 確認版本正確但缺表時不自動修補。
func TestRejectsIncompleteCurrentSchema(t *testing.T) {
	store, path := newStore(t)
	if _, err := store.db.ExecContext(context.Background(), "DROP TABLE entry_event"); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err := Open(context.Background(), path, Options{Create: true})
	requireCode(t, err, model.CodeDatabaseUnknownSchema)
}

// TestRejectsCorruptFile 確認不是資料庫的檔案回報損壞，而不是一般讀寫錯誤。
func TestRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database at all, just bytes"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	_, err := Open(context.Background(), path, Options{Create: true})
	requireCode(t, err, model.CodeDatabaseCorrupt)
}
