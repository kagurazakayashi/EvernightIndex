// Package fsmeta 採集單一路徑的檔案系統中繼資料，並提供跨平台的識別比較。
//
// 契約（掃描層依賴這些保證）：
//   - 一律以不跟隨連結的方式讀取（lstat 語意）：連結與 Windows 重解析點本身是一筆條目，
//     目標既不會被存取，也不會被遞迴，因此不會出現環路或走出根目錄。
//   - 只讀取中繼資料，不讀取內容：FIFO、socket、裝置檔與重解析點都不會被開啟來讀資料，
//     所以在沒有寫入端的 FIFO 上也不會阻塞。
//   - 平台不提供的欄位留 nil，不用 0 或空字串假裝有效值；Unix 的 ctime 是「中繼資料變更時間」，
//     絕不拿來當建立時間（btime）。
//   - 名稱與路徑保留原始位元組，不做小寫化、Unicode 正規化或有損替換。
//
// 平台實作分檔：meta_windows.go、meta_linux.go、meta_darwin.go；其餘平台由
// meta_unsupported.go 明確拒絕，而不是回報一半正確的資料。
package fsmeta

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// LinkState 描述一個跳轉物件的解析狀態。
//
// 連結本身永遠是索引裡的一筆條目；「目標讀不到」或「目標目前不存在」都不等於
// 這個連結被刪除，因此狀態另外記錄，而不是把條目丟掉。
type LinkState string

const (
	// LinkStateNone 表示這個路徑不是連結或重解析點。
	LinkStateNone LinkState = ""
	// LinkStateResolved 表示已取得目標文字，且目標目前可解析。
	LinkStateResolved LinkState = "resolved"
	// LinkStateDangling 表示已取得目標文字，但目標目前不存在（斷鏈）。
	LinkStateDangling LinkState = "dangling"
	// LinkStateUnreadable 表示無法取得目標文字（例如不帶目標的其他重解析點）。
	LinkStateUnreadable LinkState = "unreadable"
)

// Info 是一個路徑的原始中繼資料。
//
// 這裡只放「檔案系統說了什麼」，不含相對路徑、狀態或執行 ID 等掃描語意；
// 呼叫端用 Fill 併入 model.Entry，讓採集與判定分離。
type Info struct {
	// Type 是條目類型；重解析點本身一律視為 symlink，不跟隨目標。
	Type model.EntryType
	// Size 只對普通檔案有值。
	Size *int64
	// Mode 是 Go fs.FileMode 的原始值，跨平台語意一致。
	Mode *int64
	// Atime、Mtime、Btime、Ctime 分別是存取、修改、建立與中繼資料變更時間。
	Atime *model.Timestamp
	Mtime *model.Timestamp
	Btime *model.Timestamp
	Ctime *model.Timestamp

	// POSIX 擴展欄位。
	UID      *int64
	GID      *int64
	DeviceID *int64
	Inode    *int64
	Nlink    *int64

	// Windows 擴展欄位。
	WinAttrs      *int64
	WinVolumeID   *string
	WinFileID     *string
	WinReparseTag *string

	// BSD 擴展欄位（macOS 的檔案旗標）。
	BSDFlags *int64

	// LinkTarget 是連結目標文字；不是跳轉物件或目標讀不到時為 nil。
	LinkTarget *string
	// LinkState 說明連結目標的解析結果。
	LinkState LinkState

	// Incomplete 表示部分擴展欄位沒能採集（例如無法開啟 Windows 屬性句柄）。
	// 已知條目仍然有效，只是能力受限；上層必須能區分「沒有值」與「讀不到」。
	Incomplete bool
	// IncompleteReason 是穩定的機器標識，供事件與除錯使用。
	IncompleteReason string
}

// IsDir 表示這是一般目錄。
func (i Info) IsDir() bool { return i.Type == model.EntryTypeDir }

// Fill 把採集結果併入條目；只覆寫中繼資料欄位，不動路徑、狀態與執行欄位。
func (i Info) Fill(entry *model.Entry) {
	if entry == nil {
		return
	}
	entry.Type = i.Type
	entry.Size = i.Size
	entry.Mode = i.Mode
	entry.Atime = i.Atime
	entry.Mtime = i.Mtime
	entry.Btime = i.Btime
	entry.Ctime = i.Ctime
	entry.UID = i.UID
	entry.GID = i.GID
	entry.DeviceID = i.DeviceID
	entry.Inode = i.Inode
	entry.Nlink = i.Nlink
	entry.WinAttrs = i.WinAttrs
	entry.WinVolumeID = i.WinVolumeID
	entry.WinFileID = i.WinFileID
	entry.WinReparseTag = i.WinReparseTag
	entry.BSDFlags = i.BSDFlags
	entry.LinkTarget = i.LinkTarget
}

// VolumeOf 回傳路徑所在卷/裝置的穩定文字標識。
//
// POSIX 用裝置號（st_dev），Windows 用卷序列號；兩套字串命名空間不互通，
// 但同一次掃描只會用到其中一套，因此可以安全地當字串比較。
func VolumeOf(info Info) (string, bool) {
	if info.WinVolumeID != nil && *info.WinVolumeID != "" {
		return "win:" + *info.WinVolumeID, true
	}
	if info.DeviceID != nil {
		return "dev:" + strconv.FormatInt(*info.DeviceID, 10), true
	}
	return "", false
}

// FileIDOf 回傳檔案系統內的檔案標識（Windows 檔案 ID 或 POSIX inode）。
func FileIDOf(info Info) (string, bool) {
	if info.WinFileID != nil && *info.WinFileID != "" {
		return *info.WinFileID, true
	}
	if info.Inode != nil {
		return strconv.FormatInt(*info.Inode, 10), true
	}
	return "", false
}

// IdentityOf 回傳「卷 + 檔案標識」，用來判斷兩個路徑是否指向同一個物件。
//
// 這是硬連結辨識與根目錄身份比對的依據：只有兩邊都取得標識時才成立，
// 缺少任一標識時回傳 false，絕不因為「看起來一樣」就宣稱相同。
func IdentityOf(info Info) (string, bool) {
	volume, okVolume := VolumeOf(info)
	fileID, okFileID := FileIDOf(info)
	if !okVolume || !okFileID {
		return "", false
	}
	return volume + "|" + fileID, true
}

// SameIdentity 判斷兩份中繼資料是否指向同一個檔案系統物件。
func SameIdentity(a, b Info) bool {
	left, okLeft := IdentityOf(a)
	right, okRight := IdentityOf(b)
	return okLeft && okRight && left == right
}

// RootIdentity 採集根目錄的卷標識與根目錄自身標識，供 model.RootSpec 使用。
//
// 兩者都可能不可得（例如平台不提供檔案 ID）：此時回傳 nil，而不是空字串。
func RootIdentity(path string) (volume *string, fileID *string, err error) {
	info, err := Collect(path)
	if err != nil {
		return nil, nil, err
	}
	if text, ok := VolumeOf(info); ok {
		volume = &text
	}
	if text, ok := FileIDOf(info); ok {
		fileID = &text
	}
	return volume, fileID, nil
}

// ProbeCasePolicy 以真實探測判定一個目錄所在環境的路徑大小寫比較策略。
//
// 做法是把路徑中某一段的 ASCII 字母大小寫反轉後重新採集：
//   - 解析到同一個檔案標識 → 不區分大小寫；
//   - 解析失敗，或解析到不同標識 → 區分大小寫。
//
// 這比「依作業系統名稱猜測」可靠：同一個系統上可以有大小寫策略不同的卷或目錄。
// probed 為 false 表示找不到任何含 ASCII 字母的段落可以反轉（例如全 CJK 路徑），
// 此時回傳平台預設值；呼叫端必須記成「未探測」，而不是實際測量結果。
func ProbeCasePolicy(path string) (policy model.CasePolicy, probed bool, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	target, err := Collect(abs)
	if err != nil {
		return "", false, err
	}
	candidates := caseProbeCandidates(abs)
	if len(candidates) == 0 {
		return defaultCasePolicy(), false, nil
	}
	for _, candidate := range candidates {
		got, collectErr := Collect(candidate)
		if collectErr != nil {
			// 該大小寫變體不存在，這本身就是區分大小寫的證據。
			continue
		}
		if SameIdentity(target, got) {
			return model.CaseInsensitive, true, nil
		}
	}
	return model.CaseSensitive, true, nil
}

// caseProbeCandidates 產生大小寫反轉的候選路徑（最多一個）。
//
// 由最貼近探測目標的段落開始往上层找，只用第一個含 ASCII 字母的段落：
// 反轉多個段落會讓「解析失敗」的歸因變得模糊。
func caseProbeCandidates(abs string) []string {
	current := filepath.Clean(abs)
	for depth := 0; depth < 3; depth++ {
		dir, name := filepath.Split(current)
		if name == "" {
			// 已經走到根目錄：對「C:」這種磁碟機根目錄嘗試反轉代號。
			trimmed := strings.TrimRight(current, string(filepath.Separator)+"/")
			if toggled, ok := toggleAsciiCase(trimmed); ok {
				return []string{toggled + string(filepath.Separator)}
			}
			return nil
		}
		if toggled, ok := toggleAsciiCase(name); ok {
			return []string{filepath.Join(dir, toggled)}
		}
		next := strings.TrimRight(dir, string(filepath.Separator)+"/")
		if next == "" || next == current {
			return nil
		}
		current = next
	}
	return nil
}

// toggleAsciiCase 反轉字串中所有 ASCII 字母的大小寫；沒有 ASCII 字母時回傳 false。
func toggleAsciiCase(text string) (string, bool) {
	changed := false
	out := []byte(text)
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case c >= 'a' && c <= 'z':
			out[i] = c - ('a' - 'A')
			changed = true
		case c >= 'A' && c <= 'Z':
			out[i] = c + ('a' - 'A')
			changed = true
		}
	}
	if !changed {
		return "", false
	}
	return string(out), true
}

// setLinkTarget 讀取連結目標並記錄解析狀態。
//
// os.Readlink 只讀連結本身的目標文字，不跟隨也不存取目標；
// 再用 os.Stat 判斷目標目前是否存在，以區分「可解析」與「斷鏈」。
// os.Stat 只取中繼資料，就算目標是 FIFO 也不會阻塞。
func setLinkTarget(path string, info *Info) {
	if info == nil {
		return
	}
	target, err := os.Readlink(path)
	if err != nil {
		info.LinkState = LinkStateUnreadable
		return
	}
	info.LinkTarget = &target
	if _, statErr := os.Stat(path); statErr != nil {
		info.LinkState = LinkStateDangling
		return
	}
	info.LinkState = LinkStateResolved
}

// classifyGoMode 把 Go 的檔案模式分類成條目類型；各平台共用同一語意。
func classifyGoMode(mode os.FileMode) model.EntryType {
	switch {
	case mode&os.ModeSymlink != 0:
		return model.EntryTypeSymlink
	case mode.IsDir():
		return model.EntryTypeDir
	case mode.IsRegular():
		return model.EntryTypeFile
	default:
		return model.EntryTypeSpecial
	}
}

// ptrInt64 回傳值的指標；用於建立「有值」欄位。
func ptrInt64(value int64) *int64 { return &value }

// ptrString 回傳字串的指標。
func ptrString(value string) *string { return &value }
