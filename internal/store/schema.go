package store

// 本檔集中宣告 SQLite 結構：識別表、綁定根目錄、執行、條目、暫存條目、
// 變化事件與待匯出狀態，以及常用查詢索引。
//
// 結構演進規則：
//   - SchemaVersion 只在新增明確遷移時遞增，遷移寫在 store.go 的 migrate；
//   - 不自動重建未知結構：缺表或表集不符時回報 E_DATABASE_UNKNOWN_SCHEMA；
//   - 時間欄位一律使用 model.Timestamp 的固定寬度 UTC 文字；
//   - 可為 NULL 的欄位代表「不可得或不適用」，與 0、空字串語義不同。

// storeMetaDDL 是識別表：記錄格式版本與資料庫 UUID。
//
// 以 key/value 形式保存，讓識別資訊不必隨結構變動而搬家。
const storeMetaDDL = `CREATE TABLE IF NOT EXISTS store_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID`

// runDDL 是一次執行。
//
// 狀態機：running → published（提交成功）／failed（發佈前致命錯誤）／
// interrupted（發佈前取消）／aborted（發佈交易失敗並回滾）／abandoned
// （行程消失後由下一個持有寫入鎖的執行補標）。
// generation 只在 published 時有值，讓「執行完成」與「已發佈」分開表達。
const runDDL = `CREATE TABLE IF NOT EXISTS run (
	run_id              TEXT    PRIMARY KEY,
	root_id             INTEGER NOT NULL,
	mode                TEXT    NOT NULL CHECK (mode IN ('create', 'update')),
	status              TEXT    NOT NULL CHECK (status IN ('running', 'published', 'failed', 'interrupted', 'aborted', 'abandoned')),
	started_at_utc      TEXT    NOT NULL,
	finished_at_utc     TEXT,
	baseline_generation INTEGER CHECK (baseline_generation IS NULL OR baseline_generation >= 1),
	generation          INTEGER CHECK (generation IS NULL OR generation >= 1),
	coverage            TEXT    NOT NULL CHECK (coverage IN ('unknown', 'complete', 'partial')),
	coverage_json       TEXT,
	params_json         TEXT    NOT NULL,
	discovered          INTEGER NOT NULL DEFAULT 0 CHECK (discovered >= 0),
	processed           INTEGER NOT NULL DEFAULT 0 CHECK (processed >= 0),
	succeeded           INTEGER NOT NULL DEFAULT 0 CHECK (succeeded >= 0),
	failed              INTEGER NOT NULL DEFAULT 0 CHECK (failed >= 0),
	error_events        INTEGER NOT NULL DEFAULT 0 CHECK (error_events >= 0),
	published_entries   INTEGER CHECK (published_entries IS NULL OR published_entries >= 0),
	message_key         TEXT,
	error_code          TEXT,
	CHECK (status <> 'published' OR generation IS NOT NULL),
	CHECK (status = 'published' OR generation IS NULL),
	CHECK (status <> 'published' OR finished_at_utc IS NOT NULL),
	CHECK (status = 'published' OR published_entries IS NULL)
)`

// rootDirDDL 是綁定根目錄。CHECK (id = 1) 在結構層面保證「一庫一根目錄」。
//
// published_generation 為 NULL 表示尚未完成任何一次發佈：
// 首次建立被取消或發佈失敗時就是這個狀態，查詢端必須把它視為不完整索引。
const rootDirDDL = `CREATE TABLE IF NOT EXISTS root_dir (
	id                   INTEGER PRIMARY KEY CHECK (id = 1),
	abs_path             TEXT    NOT NULL,
	path_key             TEXT    NOT NULL,
	source_platform      TEXT    NOT NULL,
	case_policy          TEXT    NOT NULL CHECK (case_policy IN ('sensitive', 'insensitive')),
	volume_identity      TEXT,
	root_file_id         TEXT,
	bound_at_utc         TEXT    NOT NULL,
	published_generation INTEGER CHECK (published_generation IS NULL OR published_generation >= 1),
	published_run_id     TEXT REFERENCES run(run_id),
	published_at_utc     TEXT,
	CHECK ((published_generation IS NULL) = (published_run_id IS NULL)),
	CHECK ((published_generation IS NULL) = (published_at_utc IS NULL))
)`

// entryDDL 是已發佈的索引條目，也是查詢唯一的來源。
//
// 寫入邊界：正式寫入路徑只允許在發佈交易內改寫此表（見 entry.go 的 Publish）；
// 掃描期間的變更一律先寫 entry_stage，讓查詢看不到半次更新。
const entryDDL = `CREATE TABLE IF NOT EXISTS entry (
	root_id              INTEGER NOT NULL REFERENCES root_dir(id),
	path_key             TEXT    NOT NULL,
	rel_path             TEXT    NOT NULL,
	abs_path             TEXT    NOT NULL,
	name                 TEXT    NOT NULL,
	ext                  TEXT,
	parent_rel           TEXT    NOT NULL,
	entry_type           TEXT    NOT NULL CHECK (entry_type IN ('file', 'dir', 'symlink', 'special')),
	size_bytes           INTEGER,
	atime_utc            TEXT,
	mtime_utc            TEXT,
	btime_utc            TEXT,
	ctime_utc            TEXT,
	mode                 INTEGER,
	link_target          TEXT,
	uid                  INTEGER,
	gid                  INTEGER,
	device_id            INTEGER,
	inode                INTEGER,
	nlink                INTEGER,
	win_attrs            INTEGER,
	win_volume_id        TEXT,
	win_file_id          TEXT,
	win_reparse_tag      TEXT,
	bsd_flags            INTEGER,
	hash_sha256          TEXT,
	hash_state           TEXT    NOT NULL CHECK (hash_state IN ('not_applicable', 'not_computed', 'computed', 'reused', 'failed', 'disabled')),
	hash_computed_at_utc TEXT,
	hash_size_bytes      INTEGER,
	hash_mtime_utc       TEXT,
	status               TEXT    NOT NULL CHECK (status IN ('present', 'excluded', 'unverified', 'missing')),
	first_seen_run       TEXT    NOT NULL REFERENCES run(run_id),
	last_seen_run        TEXT    NOT NULL REFERENCES run(run_id),
	last_seen_at_utc     TEXT    NOT NULL,
	error_code           TEXT,
	error_stage          TEXT,
	PRIMARY KEY (root_id, path_key),
	CHECK (entry_type = 'file' OR size_bytes IS NULL),
	CHECK (hash_sha256 IS NULL OR hash_state IN ('computed', 'reused')),
	CHECK (hash_sha256 IS NOT NULL OR hash_state NOT IN ('computed', 'reused'))
) WITHOUT ROWID`

// entryStageDDL 是本次執行暫存的條目變更，查詢不可見。
//
// 欄位名稱刻意與 entry 完全一致，讓發佈交易能以 INSERT ... SELECT 一次搬移；
// op = 'delete' 的列只使用 run_id 與 path_key，其餘欄位為 NULL。
const entryStageDDL = `CREATE TABLE IF NOT EXISTS entry_stage (
	run_id               TEXT    NOT NULL REFERENCES run(run_id) ON DELETE CASCADE,
	path_key             TEXT    NOT NULL,
	op                   TEXT    NOT NULL CHECK (op IN ('upsert', 'delete')),
	rel_path             TEXT,
	abs_path             TEXT,
	name                 TEXT,
	ext                  TEXT,
	parent_rel           TEXT,
	entry_type           TEXT    CHECK (entry_type IS NULL OR entry_type IN ('file', 'dir', 'symlink', 'special')),
	size_bytes           INTEGER,
	atime_utc            TEXT,
	mtime_utc            TEXT,
	btime_utc            TEXT,
	ctime_utc            TEXT,
	mode                 INTEGER,
	link_target          TEXT,
	uid                  INTEGER,
	gid                  INTEGER,
	device_id            INTEGER,
	inode                INTEGER,
	nlink                INTEGER,
	win_attrs            INTEGER,
	win_volume_id        TEXT,
	win_file_id          TEXT,
	win_reparse_tag      TEXT,
	bsd_flags            INTEGER,
	hash_sha256          TEXT,
	hash_state           TEXT    CHECK (hash_state IS NULL OR hash_state IN ('not_applicable', 'not_computed', 'computed', 'reused', 'failed', 'disabled')),
	hash_computed_at_utc TEXT,
	hash_size_bytes      INTEGER,
	hash_mtime_utc       TEXT,
	status               TEXT    CHECK (status IS NULL OR status IN ('present', 'excluded', 'unverified', 'missing')),
	first_seen_run       TEXT,
	last_seen_run        TEXT,
	last_seen_at_utc     TEXT,
	error_code           TEXT,
	error_stage          TEXT,
	PRIMARY KEY (run_id, path_key)
) WITHOUT ROWID`

// entryEventDDL 是變化事件，與索引結果在同一個發佈交易內提交。
//
// before_exists / after_exists 區分「路徑不存在」與「欄位為 NULL」；
// 本步驟只寫入事件，CSV 匯出留給後續步驟。
const entryEventDDL = `CREATE TABLE IF NOT EXISTS entry_event (
	event_id       INTEGER PRIMARY KEY,
	run_id         TEXT    NOT NULL REFERENCES run(run_id),
	root_id        INTEGER NOT NULL,
	path_key       TEXT    NOT NULL,
	rel_path       TEXT    NOT NULL,
	event_type     TEXT    NOT NULL CHECK (event_type IN ('added', 'removed', 'metadata_changed', 'content_changed', 'type_changed', 'hash_updated', 'excluded', 'scan_error', 'restored')),
	changed_fields TEXT,
	before_exists  INTEGER NOT NULL CHECK (before_exists IN (0, 1)),
	after_exists   INTEGER NOT NULL CHECK (after_exists IN (0, 1)),
	before_json    TEXT,
	after_json     TEXT,
	created_at_utc TEXT    NOT NULL,
	CHECK (before_exists = 1 OR before_json IS NULL),
	CHECK (after_exists = 1 OR after_json IS NULL)
)`

// changeExportDDL 是待匯出狀態：即使索引已發佈，CSV 仍可能尚未輸出。
//
// state = 'pending' 表示需要補匯出；事件本身留在 entry_event，重試必須冪等。
const changeExportDDL = `CREATE TABLE IF NOT EXISTS change_export (
	run_id         TEXT    PRIMARY KEY REFERENCES run(run_id),
	state          TEXT    NOT NULL CHECK (state IN ('pending', 'exported', 'failed')),
	target_path    TEXT,
	checksum       TEXT,
	event_count    INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
	attempts       INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
	last_error     TEXT,
	updated_at_utc TEXT    NOT NULL
)`

// indexDDL 是常用查詢索引。
//
// 前綴查詢（依相對路徑）直接使用 entry 的主鍵順序，不另外建立索引；
// 這裡補上名稱、大小、時間、摘要、類型與狀態等篩選欄位。
var indexDDL = []string{
	`CREATE INDEX IF NOT EXISTS idx_run_started ON run(started_at_utc)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS uq_run_generation ON run(root_id, generation) WHERE generation IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_entry_name ON entry(root_id, name)`,
	`CREATE INDEX IF NOT EXISTS idx_entry_size ON entry(root_id, size_bytes) WHERE size_bytes IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_entry_mtime ON entry(root_id, mtime_utc) WHERE mtime_utc IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_entry_sha256 ON entry(hash_sha256) WHERE hash_sha256 IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_entry_type ON entry(root_id, entry_type)`,
	`CREATE INDEX IF NOT EXISTS idx_entry_status ON entry(root_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_event_run ON entry_event(run_id, event_id)`,
	`CREATE INDEX IF NOT EXISTS idx_event_path ON entry_event(root_id, path_key)`,
	`CREATE INDEX IF NOT EXISTS idx_export_state ON change_export(state)`,
}

// schemaDDL 是建立目前版本結構的完整語句序列（依依賴順序）。
func schemaDDL() []string {
	statements := []string{storeMetaDDL, runDDL, rootDirDDL, entryDDL, entryStageDDL, entryEventDDL, changeExportDDL}
	return append(statements, indexDDL...)
}

// requiredTables 是目前版本必須存在的表；缺任何一個都代表結構不完整。
var requiredTables = []string{"store_meta", "run", "root_dir", "entry", "entry_stage", "entry_event", "change_export"}

// v1Tables 是格式版本 1 允許存在的表。
//
// 版本 1 只有識別表；出現其他表表示結構被改寫或來自其他程式，
// 此時拒絕自動遷移，避免覆蓋未知結構。
var v1Tables = []string{"store_meta"}
