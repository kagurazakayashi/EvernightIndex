package model

import "encoding/json"

// 本檔定義條目的穩定 JSON 快照，供變化事件的 before/after 欄位與後續 CSV 匯出共用。
//
// 設計理由：
//   - 欄位名直接取自資料庫欄位（穩定英語），不隨語言改變，也不與畫面文字耦合；
//   - 可為 NULL 的欄位用指標，序列化成 null 而不是 0 或空字串：
//     「沒有這個值」與「值是空字串」在匯出後仍必須可以區分；
//   - 快照是某一瞬間的複本，事件寫入後就與 live 條目脫鉤，
//     因此後續更新不會改寫歷史事件的內容。

// EntrySnapshot 是一筆條目的 JSON 快照。
//
// 欄位順序即序列化順序，刻意固定下來讓同一份變化在兩次執行間可以逐位元組比對。
type EntrySnapshot struct {
	RelPath    string      `json:"rel_path"`
	AbsPath    string      `json:"abs_path"`
	Name       string      `json:"name"`
	Ext        *string     `json:"ext"`
	ParentRel  string      `json:"parent_rel"`
	Type       EntryType   `json:"entry_type"`
	Size       *int64      `json:"size_bytes"`
	Atime      *Timestamp  `json:"atime_utc"`
	Mtime      *Timestamp  `json:"mtime_utc"`
	Btime      *Timestamp  `json:"btime_utc"`
	Ctime      *Timestamp  `json:"ctime_utc"`
	Mode       *int64      `json:"mode"`
	LinkTarget *string     `json:"link_target"`
	UID        *int64      `json:"uid"`
	GID        *int64      `json:"gid"`
	DeviceID   *int64      `json:"device_id"`
	Inode      *int64      `json:"inode"`
	Nlink      *int64      `json:"nlink"`
	WinAttrs   *int64      `json:"win_attrs"`
	WinVolume  *string     `json:"win_volume_id"`
	WinFileID  *string     `json:"win_file_id"`
	WinReparse *string     `json:"win_reparse_tag"`
	BSDFlags   *int64      `json:"bsd_flags"`
	HashSHA256 *string     `json:"hash_sha256"`
	HashState  HashState   `json:"hash_state"`
	HashAt     *Timestamp  `json:"hash_computed_at_utc"`
	HashSize   *int64      `json:"hash_size_bytes"`
	HashMTime  *Timestamp  `json:"hash_mtime_utc"`
	Status     EntryStatus `json:"status"`
	FirstSeen  string      `json:"first_seen_run"`
	LastSeen   string      `json:"last_seen_run"`
	LastSeenAt Timestamp   `json:"last_seen_at_utc"`
	ErrorCode  *string     `json:"error_code"`
	ErrorStage *string     `json:"error_stage"`
}

// SnapshotOf 建立一筆條目的快照複本。
func SnapshotOf(entry Entry) EntrySnapshot {
	return EntrySnapshot{
		RelPath:    entry.RelPath,
		AbsPath:    entry.AbsPath,
		Name:       entry.Name,
		Ext:        entry.Ext,
		ParentRel:  entry.ParentRel,
		Type:       entry.Type,
		Size:       entry.Size,
		Atime:      entry.Atime,
		Mtime:      entry.Mtime,
		Btime:      entry.Btime,
		Ctime:      entry.Ctime,
		Mode:       entry.Mode,
		LinkTarget: entry.LinkTarget,
		UID:        entry.UID,
		GID:        entry.GID,
		DeviceID:   entry.DeviceID,
		Inode:      entry.Inode,
		Nlink:      entry.Nlink,
		WinAttrs:   entry.WinAttrs,
		WinVolume:  entry.WinVolumeID,
		WinFileID:  entry.WinFileID,
		WinReparse: entry.WinReparseTag,
		BSDFlags:   entry.BSDFlags,
		HashSHA256: entry.HashSHA256,
		HashState:  entry.HashState,
		HashAt:     entry.HashComputedAt,
		HashSize:   entry.HashSize,
		HashMTime:  entry.HashMTime,
		Status:     entry.Status,
		FirstSeen:  entry.FirstSeenRun,
		LastSeen:   entry.LastSeenRun,
		LastSeenAt: entry.LastSeenAt,
		ErrorCode:  entry.ErrorCode,
		ErrorStage: entry.ErrorStage,
	}
}

// JSON 把快照序列化成穩定鍵序的單行 JSON。
func (s EntrySnapshot) JSON() (string, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
