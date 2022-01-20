package model

import (
	"strings"
	"time"
)

// 本檔定義索引任務共用的領域型別：條目、根目錄、執行、變化事件與待匯出狀態。
//
// 這些型別只描述「資料代表什麼」，不含 SQL、平台呼叫或輸出格式；
// 儲存層負責與 SQLite 欄位互轉，平台層負責採集，呈現層負責文字。

// Timestamp 是以固定寬度 UTC 文字表示的時間戳：2006-01-02T15:04:05.000000000Z。
//
// 選用文字而非整數奈秒的理由：
//   - 固定寬度（補零到奈秒）讓同一欄位的字串排序等於時間排序，
//     範圍查詢可直接用索引掃描，不必額外轉換函式；
//   - UTC 加上明確的 Z 後綴，避免跨系統時區歧義；
//   - 補零只是表示寬度，不宣稱來源系統真有奈秒精度。
//
// 空值（nil 指標或空字串）代表「不可得」，與時間零值不同，不得互換。
type Timestamp string

// timestampLayout 是資料庫內的固定時間格式。
const timestampLayout = "2006-01-02T15:04:05.000000000Z"

// NewTimestamp 把時間轉成資料庫表示（UTC、固定寬度）。
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp(t.UTC().Format(timestampLayout))
}

// NowTimestamp 回傳目前時間的資料庫表示。
func NowTimestamp() Timestamp { return NewTimestamp(time.Now()) }

// Time 解析時間戳；空值或格式錯誤時回傳 false。
func (t Timestamp) Time() (time.Time, bool) {
	if t == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(timestampLayout, string(t))
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// String 回傳穩定表示；空值為空字串，方便輸出與比對。
func (t Timestamp) String() string { return string(t) }

// TimestampOrNil 把選填時間轉成指標；零時間視為不可得。
func TimestampOrNil(t time.Time) *Timestamp {
	if t.IsZero() {
		return nil
	}
	stamp := NewTimestamp(t)
	return &stamp
}

// EntryType 是條目類型。目錄、符號連結與特殊檔案的 SHA256 一律為空。
type EntryType string

const (
	// EntryTypeFile 是普通檔案。
	EntryTypeFile EntryType = "file"
	// EntryTypeDir 是目錄（包含空目錄）。
	EntryTypeDir EntryType = "dir"
	// EntryTypeSymlink 是符號連結或 Windows 重解析點本身（不跟隨目標）。
	EntryTypeSymlink EntryType = "symlink"
	// EntryTypeSpecial 是 FIFO、socket、裝置檔等其他特殊檔案。
	EntryTypeSpecial EntryType = "special"
)

// Valid 表示類型是否屬於已知列舉。
func (t EntryType) Valid() bool {
	switch t {
	case EntryTypeFile, EntryTypeDir, EntryTypeSymlink, EntryTypeSpecial:
		return true
	}
	return false
}

// HashState 是摘要欄位的狀態，用來區分「有摘要」「明確沒有」與「失敗保留」。
type HashState string

const (
	// HashStateNotApplicable 表示此類型不適用（目錄、連結、特殊檔案）。
	HashStateNotApplicable HashState = "not_applicable"
	// HashStateNotComputed 表示普通檔案但本次未計算（例如尚未處理）。
	HashStateNotComputed HashState = "not_computed"
	// HashStateComputed 表示本次實際讀取內容並計算。
	HashStateComputed HashState = "computed"
	// HashStateReused 表示沿用舊摘要（快速判據未變），不代表本次重新驗證。
	HashStateReused HashState = "reused"
	// HashStateFailed 表示嘗試計算但失敗；不得留下偽有效摘要。
	HashStateFailed HashState = "failed"
	// HashStateDisabled 表示本次執行以 --sha256=false 關閉內容读取。
	HashStateDisabled HashState = "disabled"
)

// Valid 表示摘要狀態是否屬於已知列舉。
func (s HashState) Valid() bool {
	switch s {
	case HashStateNotApplicable, HashStateNotComputed, HashStateComputed,
		HashStateReused, HashStateFailed, HashStateDisabled:
		return true
	}
	return false
}

// EntryStatus 是條目的可見狀態。
//
// excluded 表示因排除規則移出索引、unverified 表示本次未能成功覆蓋，
// 兩者都不是磁碟上的刪除。
type EntryStatus string

const (
	// StatusPresent 表示條目目前被視為存在於索引中。
	StatusPresent EntryStatus = "present"
	// StatusExcluded 表示被排除規則移出索引，不是刪除。
	StatusExcluded EntryStatus = "excluded"
	// StatusUnverified 表示所在區域本次未被成功覆蓋，保留舊值。
	StatusUnverified EntryStatus = "unverified"
	// StatusMissing 表示已保守確認不存在。
	StatusMissing EntryStatus = "missing"
)

// Valid 表示狀態是否屬於已知列舉。
func (s EntryStatus) Valid() bool {
	switch s {
	case StatusPresent, StatusExcluded, StatusUnverified, StatusMissing:
		return true
	}
	return false
}

// CasePolicy 是路徑比較策略；由綁定根目錄時的環境決定並存入資料庫，
// 不能只憑作業系統名稱推定。
type CasePolicy string

const (
	// CaseSensitive 是區分大小寫的比較策略。
	CaseSensitive CasePolicy = "sensitive"
	// CaseInsensitive 是不區分大小寫的比較策略。
	CaseInsensitive CasePolicy = "insensitive"
)

// Valid 表示策略是否屬於已知列舉。
func (p CasePolicy) Valid() bool {
	return p == CaseSensitive || p == CaseInsensitive
}

// PathKey 把路徑轉成比較鍵：統一分隔符，必要時折疊大小寫。
//
// 原始路徑（含原始大小寫與 Unicode 形式）另存於其他欄位；此鍵只用於
// 唯一鍵、鎖目標比對與前綴查詢，不做顯示。
func PathKey(path string, policy CasePolicy) string {
	normalized := strings.ReplaceAll(path, "\\", "/")
	// 收斂重複分隔符，讓 a//b 與 a/b 視為同一路徑。
	for strings.Contains(normalized, "//") {
		normalized = strings.ReplaceAll(normalized, "//", "/")
	}
	if policy == CaseInsensitive {
		normalized = strings.ToLower(normalized)
	}
	return normalized
}

// Entry 是一筆索引條目，對應綁定根目錄下的一個路徑。
//
// 可為 nil 的欄位代表「不可得或不適用」，與 0、空字串意義不同：
// 大小對目錄不適用、擴展名對無副檔名檔案不存在、摘要對未計算檔案為空。
type Entry struct {
	// PathKey 是正規化比較鍵，也是條目唯一鍵的一部分。
	PathKey string
	// RelPath 是相對綁定根目錄的原始路徑（保留原始大小寫）。
	RelPath string
	// AbsPath 是掃描時的絕對路徑。
	AbsPath string
	// Name 是最後一段名稱。
	Name string
	// Ext 是含點的小寫擴展名；無副檔名為 nil。
	Ext *string
	// ParentRel 是父目錄的相對路徑；根目錄自身為空字串。
	ParentRel string
	// Type 是條目類型。
	Type EntryType
	// Size 是普通檔案邏輯大小；其他類型為 nil。
	Size *int64

	// Atime、Mtime、Btime、Ctime 分別是存取、修改、建立與元資料變更時間。
	// Btime 不可得時為 nil，不以 Ctime 代替。
	Atime *Timestamp
	Mtime *Timestamp
	Btime *Timestamp
	Ctime *Timestamp

	// Mode 是 Go fs.FileMode 的原始值。
	Mode *int64
	// LinkTarget 是連結目標；非連結為 nil。
	LinkTarget *string

	// POSIX 擴展欄位，取得時才有值。
	UID      *int64
	GID      *int64
	DeviceID *int64
	Inode    *int64
	Nlink    *int64

	// Windows 擴展欄位，取得時才有值。
	WinAttrs      *int64
	WinVolumeID   *string
	WinFileID     *string
	WinReparseTag *string

	// BSD 擴展欄位（macOS flags），取得時才有值。
	BSDFlags *int64

	// HashSHA256 是主資料內容摘要；空值表示沒有可用摘要。
	HashSHA256 *string
	// HashState 說明摘要的來源與有效性。
	HashState HashState
	// HashComputedAt 是摘要計算時間。
	HashComputedAt *Timestamp
	// HashSize 與 HashMTime 是摘要對應的大小與修改時間，用於判斷快速複用。
	HashSize  *int64
	HashMTime *Timestamp

	// Status 是條目在索引中的狀態。
	Status EntryStatus
	// FirstSeenRun 是首次發現這筆路徑的執行 ID。
	FirstSeenRun string
	// LastSeenRun 是最近一次成功觀察的執行 ID。
	LastSeenRun string
	// LastSeenAt 是最近一次成功觀察的時間。
	LastSeenAt Timestamp

	// ErrorCode 與 ErrorStage 記錄最近一次失敗；沒有失敗時為 nil。
	ErrorCode  *string
	ErrorStage *string
}

// RootSpec 是綁定根目錄時提供的資訊。
type RootSpec struct {
	// AbsPath 是掃描根目錄的絕對路徑，保留原始大小寫。
	AbsPath string
	// SourcePlatform 是採集此根目錄的作業系統（runtime.GOOS）。
	SourcePlatform string
	// CasePolicy 是此根目錄所在環境的路徑比較策略。
	CasePolicy CasePolicy
	// VolumeIdentity 是卷/裝置身份；不可得為 nil。
	VolumeIdentity *string
	// RootFileID 是根目錄自身的檔案子身份；不可得為 nil。
	RootFileID *string
}

// Root 是已綁定的根目錄。
//
// 一個資料庫只綁定一個根目錄；PublishedGeneration 為 nil 代表尚未完成
// 任何一次發佈，因此不能當成完整索引使用。
type Root struct {
	ID                  int64
	AbsPath             string
	PathKey             string
	SourcePlatform      string
	CasePolicy          CasePolicy
	VolumeIdentity      *string
	RootFileID          *string
	BoundAt             Timestamp
	PublishedGeneration *int64
	PublishedRunID      *string
	PublishedAt         *Timestamp
}

// RunMode 是索引執行的種類。
type RunMode string

const (
	// RunModeCreate 表示資料庫首次建立索引。
	RunModeCreate RunMode = "create"
	// RunModeUpdate 表示更新既有索引。
	RunModeUpdate RunMode = "update"
)

// Valid 表示模式是否屬於已知列舉。
func (m RunMode) Valid() bool { return m == RunModeCreate || m == RunModeUpdate }

// RunStatus 是執行狀態機的狀態。
//
// 只有 published 代表正式資料庫已包含本次結果；其餘狀態都必須讓舊代次
// 保持可用，且不得回報已回滾尚未提交的內容。
type RunStatus string

const (
	// RunStatusRunning 表示執行進行中。
	RunStatusRunning RunStatus = "running"
	// RunStatusPublished 表示發佈交易已提交。
	RunStatusPublished RunStatus = "published"
	// RunStatusFailed 表示發佈前發生致命錯誤。
	RunStatusFailed RunStatus = "failed"
	// RunStatusInterrupted 表示發佈前被取消。
	RunStatusInterrupted RunStatus = "interrupted"
	// RunStatusAborted 表示發佈交易失敗並回滾，舊代次仍可用。
	RunStatusAborted RunStatus = "aborted"
	// RunStatusAbandoned 表示程序中斷且未完成收尾，由後續取得寫入鎖的執行標記。
	RunStatusAbandoned RunStatus = "abandoned"
)

// Valid 表示狀態是否屬於已知列舉。
func (s RunStatus) Valid() bool {
	switch s {
	case RunStatusRunning, RunStatusPublished, RunStatusFailed,
		RunStatusInterrupted, RunStatusAborted, RunStatusAbandoned:
		return true
	}
	return false
}

// Finished 表示狀態是否為終態。
func (s RunStatus) Finished() bool { return s != RunStatusRunning }

// Coverage 是掃描覆蓋狀態。
type Coverage string

const (
	// CoverageUnknown 表示尚未判定覆蓋範圍（例如發佈前失敗）。
	CoverageUnknown Coverage = "unknown"
	// CoverageComplete 表示所有目標區域都被成功覆蓋。
	CoverageComplete Coverage = "complete"
	// CoveragePartial 表示有成功區域，但有明確覆蓋缺口。
	CoveragePartial Coverage = "partial"
)

// Valid 表示覆蓋狀態是否屬於已知列舉。
func (c Coverage) Valid() bool {
	return c == CoverageUnknown || c == CoverageComplete || c == CoveragePartial
}

// RunCounts 是一次執行的統計計數。
//
// Processed 是「處理完成」，不等於成功；Errors 是錯誤事件數，
// 與失敗條目數分開計算。
type RunCounts struct {
	Discovered  int64
	Processed   int64
	Succeeded   int64
	Failed      int64
	ErrorEvents int64
}

// Run 是一次索引執行。
type Run struct {
	ID                 string
	RootID             int64
	Mode               RunMode
	Status             RunStatus
	StartedAt          Timestamp
	FinishedAt         *Timestamp
	BaselineGeneration *int64
	Generation         *int64
	Coverage           Coverage
	CoverageJSON       *string
	ParamsJSON         string
	Counts             RunCounts
	PublishedEntries   *int64
	MessageKey         *string
	ErrorCode          *string
}

// Published 表示這次執行是否已發佈。
func (r Run) Published() bool { return r.Status == RunStatusPublished }

// EventType 是變化事件類型。
type EventType string

const (
	// EventAdded 表示新增。
	EventAdded EventType = "added"
	// EventRemoved 表示保守確認的移除。
	EventRemoved EventType = "removed"
	// EventMetadataChanged 表示只有元資料變化。
	EventMetadataChanged EventType = "metadata_changed"
	// EventContentChanged 表示已確認內容變化。
	EventContentChanged EventType = "content_changed"
	// EventTypeChanged 表示類型變化。
	EventTypeChanged EventType = "type_changed"
	// EventHashUpdated 表示補齊或更新摘要基線，不是內容變化。
	EventHashUpdated EventType = "hash_updated"
	// EventExcluded 表示因排除規則移出索引，不是刪除。
	EventExcluded EventType = "excluded"
	// EventScanError 表示掃描錯誤，不是檔案變化。
	EventScanError EventType = "scan_error"
	// EventRestored 表示先前缺失的路徑再次出現。
	EventRestored EventType = "restored"
)

// Valid 表示事件類型是否屬於已知列舉。
func (t EventType) Valid() bool {
	switch t {
	case EventAdded, EventRemoved, EventMetadataChanged, EventContentChanged,
		EventTypeChanged, EventHashUpdated, EventExcluded, EventScanError, EventRestored:
		return true
	}
	return false
}

// Event 是一筆變化事件，與索引結果在同一個發佈交易內提交。
//
// BeforeExists 與 AfterExists 區分「路徑不存在」與「欄位為 NULL」：
// before_json 為 nil 且 BeforeExists 為 false 表示原本沒有這個路徑。
type Event struct {
	RunID         string
	RootID        int64
	PathKey       string
	RelPath       string
	Type          EventType
	ChangedFields []string
	BeforeExists  bool
	AfterExists   bool
	BeforeJSON    *string
	AfterJSON     *string
	CreatedAt     Timestamp
}

// ExportState 是變化日誌待匯出狀態。
type ExportState string

const (
	// ExportPending 表示事件已提交但 CSV 尚未成功輸出。
	ExportPending ExportState = "pending"
	// ExportExported 表示 CSV 已成功輸出。
	ExportExported ExportState = "exported"
	// ExportFailed 表示匯出嘗試失敗，事件仍保留以支援重試。
	ExportFailed ExportState = "failed"
)

// Valid 表示匯出狀態是否屬於已知列舉。
func (s ExportState) Valid() bool {
	return s == ExportPending || s == ExportExported || s == ExportFailed
}

// PendingExport 是一筆待匯出的變化日誌狀態。
type PendingExport struct {
	RunID      string
	State      ExportState
	TargetPath *string
	Checksum   *string
	EventCount int64
	Attempts   int64
	LastError  *string
	UpdatedAt  Timestamp
}
