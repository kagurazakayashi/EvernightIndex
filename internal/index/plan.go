package index

import (
	"github.com/kagurazakayashi/EvernightIndex/internal/hash"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/scan"
)

// 本檔是增量更新的決策核心：對每一個掃描到的條目回答三個問題。
//
//  1. 這是新增、還是已記錄過的路徑？
//  2. 快速判據是否有變化，摘要該重算、複用、保留還是清除？
//  3. 這次的變化屬於哪一類（新增、型別變化、內容變化、元資料變化、基線更新）？
//
// 決策表（功能清單 3.2）在此以程式碼表達，四種組合的行為刻意寫成三條分支：
//
//	保存 SHA256 | 細緻模式 | 行為
//	開啟         | 關閉     | 新增、快速判據變化、沒有可用舊摘要時計算並保存；其餘複用
//	關閉         | 關閉     | 不讀內容；新增無摘要；判據變化清除舊摘要；其餘保留舊摘要
//	開啟         | 開啟     | 所有可讀取普通檔案重算；有舊摘要才比較內容是否變化
//	關閉         | 開啟     | 非法組合，Build 與命令列層都會提前拒絕
//
// 不變的取捨：ctime 變化只是保守觸發，不代表內容改變；mtime 變化但摘要相同
// 屬於元資料變化；只有「舊摘要與新摘要都存在且不同」才宣告內容變化。

// plan 是一個條目的比對結論。
type plan struct {
	// entry 是本次要暫存的條目；摘要結果稍後由 applyHash 填入。
	entry model.Entry
	// old 是已發佈的同一路徑條目；nil 表示新增。
	old *model.Entry
	// computeHash 表示需要讀取內容計算摘要。
	computeHash bool
	// cleared 表示舊摘要因為快速判據變化而被清除（關閉保存時）。
	cleared bool
}

// planEntry 依已發佈的舊條目與本次掃描結果產生決策。
func (b *builder) planEntry(entry model.Entry, old model.Entry, exists bool) plan {
	result := plan{entry: entry}
	if exists {
		previous := old
		result.old = &previous
		// 首次發現的執行不能因為這次更新而被改寫：它記錄的是「這條路徑最初何時出現」。
		result.entry.FirstSeenRun = old.FirstSeenRun
	}
	result.entry.Status = model.StatusPresent
	result.entry.LastSeenRun = b.target.RunID()
	result.entry.LastSeenAt = model.NewTimestamp(b.now())

	// 目錄、連結與特殊檔案沒有主資料內容：摘要一律不適用，舊摘要也一併清掉。
	if entry.Type != model.EntryTypeFile {
		result.entry.HashSHA256 = nil
		result.entry.HashState = model.HashStateNotApplicable
		result.entry.HashComputedAt = nil
		result.entry.HashSize = nil
		result.entry.HashMTime = nil
		return result
	}
	if !b.options.SHA256 {
		b.planWithoutStoring(&result, exists)
		return result
	}
	b.planWithStoring(&result, exists)
	return result
}

// planWithoutStoring 處理 --sha256=false：本次執行完全不讀取檔案內容。
func (b *builder) planWithoutStoring(result *plan, exists bool) {
	result.entry.HashSHA256 = nil
	result.entry.HashState = model.HashStateDisabled
	result.entry.HashComputedAt = nil
	result.entry.HashSize = nil
	result.entry.HashMTime = nil
	if !exists || !hasRecordedHash(*result.old) {
		return
	}
	if b.quickChanged(*result.old, result.entry) {
		// 快速判據變了：舊摘要不再可信。關閉保存時既不能重算，就只能清除；
		// 留著會讓後續查詢與去重把過期摘要當成當前內容。
		result.cleared = true
		return
	}
	// 判據沒變：保留舊摘要與它對應的大小/時間，但這次沒有驗證過內容。
	// 因此狀態照抄舊值（computed 或 reused），不改成任何「本次已確認」的說法。
	result.entry.HashSHA256 = result.old.HashSHA256
	result.entry.HashState = result.old.HashState
	result.entry.HashComputedAt = result.old.HashComputedAt
	result.entry.HashSize = result.old.HashSize
	result.entry.HashMTime = result.old.HashMTime
}

// planWithStoring 處理 --sha256=true：只在必要時讀取內容。
func (b *builder) planWithStoring(result *plan, exists bool) {
	if exists && !b.options.Deep {
		if previous := *result.old; hasRecordedHash(previous) && !b.quickChanged(previous, result.entry) {
			// 快速複用：判據沒變，沿用舊摘要，並明白標成 reused（不是本次重新驗證）。
			result.entry.HashSHA256 = previous.HashSHA256
			result.entry.HashState = model.HashStateReused
			result.entry.HashComputedAt = previous.HashComputedAt
			result.entry.HashSize = previous.HashSize
			result.entry.HashMTime = previous.HashMTime
			result.entry.ErrorCode = nil
			result.entry.ErrorStage = nil
			return
		}
	}
	result.entry.HashState = model.HashStateNotComputed
	result.computeHash = true
}

// hasRecordedHash 判斷舊條目是否有可比較的摘要。
//
// 只有真有摘要（且狀態為 computed/reused，結構層保證）才算「有舊摘要」；
// 失敗或缺摘要的條目只能建立基線，不能推斷歷史內容是否改變。
func hasRecordedHash(entry model.Entry) bool {
	if entry.HashSHA256 == nil || *entry.HashSHA256 == "" {
		return false
	}
	return entry.HashState == model.HashStateComputed || entry.HashState == model.HashStateReused
}

// quickChanged 判斷快速判據是否有變化。
//
// 基礎判據是型別、邏輯大小與修改時間；輔助判據是可用且可比較的檔案身份
// 與中繼資料變更時間。輔助欄位「從無到有」或「從有到無」都算變化：
// 那代表能力改變，不能聲稱這次驗證通過。訪問時間不參與判斷（讀取自己會改它），
// 建立時間與權限值本身也不是內容一致性的證據。
func (b *builder) quickChanged(old, entry model.Entry) bool {
	if old.Type != entry.Type {
		return true
	}
	if !equalInt64(old.Size, entry.Size) {
		return true
	}
	if !equalTimestamp(old.Mtime, entry.Mtime) {
		return true
	}
	oldIdentity, hasOld := hash.IdentityOfEntry(old)
	newIdentity, hasNew := hash.IdentityOfEntry(entry)
	if hasOld != hasNew {
		return true
	}
	if hasOld && oldIdentity != newIdentity {
		return true
	}
	if (old.Ctime == nil) != (entry.Ctime == nil) {
		return true
	}
	if old.Ctime != nil && entry.Ctime != nil && *old.Ctime != *entry.Ctime {
		// ctime 只代表「中繼資料動過」，例如改權限；這是保守觸發，不是內容變化結論。
		return true
	}
	return false
}

// finish 依決策與摘要結果分類這個條目，並累加統計與變化事件。
func (b *builder) finish(current *plan) error {
	kind, fields := b.classify(*current)
	switch {
	case current.entry.HashState == model.HashStateReused:
		b.result.HashReused++
		if b.options.Stats != nil {
			b.options.Stats.AddHashReused(1)
		}
	case current.entry.HashState == model.HashStateComputed && current.computeHash:
		observe(b.options.Observe, model.OpHashed, current.entry.RelPath)
	case current.entry.HashState == model.HashStateFailed:
		observe(b.options.Observe, model.OpFailed, current.entry.RelPath)
	}
	if current.cleared {
		b.result.HashInvalidated++
	}

	if current.old == nil {
		b.result.Added++
		if b.options.Stats != nil {
			b.options.Stats.AddAdded(1)
		}
		observe(b.options.Observe, model.OpAdded, current.entry.RelPath)
		return b.recordEvent(current, model.EventAdded, fields)
	}
	if kind == "" {
		b.result.Unchanged++
		if b.options.Stats != nil {
			b.options.Stats.AddUnchanged(1)
		}
		return nil
	}
	b.result.Updated++
	if b.options.Stats != nil {
		b.options.Stats.AddUpdated(1)
	}
	switch kind {
	case model.EventContentChanged:
		b.result.ContentChange++
	case model.EventMetadataChanged:
		b.result.MetaChange++
	case model.EventTypeChanged:
		b.result.TypeChanged++
	case model.EventRestored:
		b.result.Restored++
	}
	observe(b.options.Observe, model.OpUpdated, current.entry.RelPath)
	return b.recordEvent(current, kind, fields)
}

// classify 決定變化的分類與變化欄位清單。
//
// 一個路徑在一次執行裡只留一筆事件，多項變化合併在同一筆的 changed_fields；
// 這就是 CSV 匯出「一行一個變化」的依據。優先順序是刻意安排的：
// 型別變化最顯眼，其次是已確認的內容變化，再來是狀態復原，
// 最後才是純元資料變化與摘要基線更新。
func (b *builder) classify(current plan) (model.EventType, []string) {
	if current.old == nil {
		return model.EventAdded, nil
	}
	previous := *current.old
	fields := changedFields(previous, current.entry)
	digestChanged, digestGained, digestLost := digestTransition(previous.HashSHA256, current.entry.HashSHA256)
	if digestChanged || digestGained || digestLost {
		fields = append(fields, "hash_sha256", "hash_state")
	}
	switch {
	case previous.Type != current.entry.Type:
		return model.EventTypeChanged, fields
	case digestChanged:
		return model.EventContentChanged, fields
	case previous.Status == model.StatusMissing || previous.Status == model.StatusExcluded:
		return model.EventRestored, fields
	case len(fields) > 0:
		return model.EventMetadataChanged, fields
	case digestGained:
		return model.EventHashUpdated, fields
	default:
		return "", fields
	}
}

// digestTransition 比較兩邊的摘要，回報「內容變化」「補上基線」「失去摘要」。
func digestTransition(old, current *string) (changed, gained, lost bool) {
	switch {
	case old != nil && current != nil:
		return *old != *current, false, false
	case old == nil && current != nil:
		return false, true, false
	case old != nil && current == nil:
		return false, false, true
	}
	return false, false, false
}

// changedFields 列出兩筆條目之間變化的欄位（穩定英語欄位名）。
//
// 刻意不比對 last_seen_*、hash_computed_at 等「本次執行寫入」或「重算時間」欄位：
// 它們每次都會變，卻不代表檔案有任何改變。訪問時間也不比對：
// 讀取內容本身就可能更新它，把它算成變化只會製造噪音。
func changedFields(old, entry model.Entry) []string {
	fields := make([]string, 0, 8)
	if old.Type != entry.Type {
		fields = append(fields, "entry_type")
	}
	if !equalInt64(old.Size, entry.Size) {
		fields = append(fields, "size_bytes")
	}
	if !equalTimestamp(old.Mtime, entry.Mtime) {
		fields = append(fields, "mtime_utc")
	}
	if !equalTimestamp(old.Ctime, entry.Ctime) {
		fields = append(fields, "ctime_utc")
	}
	if !equalTimestamp(old.Btime, entry.Btime) {
		fields = append(fields, "btime_utc")
	}
	if !equalInt64(old.Mode, entry.Mode) {
		fields = append(fields, "mode")
	}
	if !equalString(old.LinkTarget, entry.LinkTarget) {
		fields = append(fields, "link_target")
	}
	if !equalInt64(old.UID, entry.UID) {
		fields = append(fields, "uid")
	}
	if !equalInt64(old.GID, entry.GID) {
		fields = append(fields, "gid")
	}
	if !equalInt64(old.DeviceID, entry.DeviceID) {
		fields = append(fields, "device_id")
	}
	if !equalInt64(old.Inode, entry.Inode) {
		fields = append(fields, "inode")
	}
	if !equalInt64(old.Nlink, entry.Nlink) {
		fields = append(fields, "nlink")
	}
	if !equalInt64(old.WinAttrs, entry.WinAttrs) {
		fields = append(fields, "win_attrs")
	}
	if !equalString(old.WinVolumeID, entry.WinVolumeID) {
		fields = append(fields, "win_volume_id")
	}
	if !equalString(old.WinFileID, entry.WinFileID) {
		fields = append(fields, "win_file_id")
	}
	if !equalString(old.WinReparseTag, entry.WinReparseTag) {
		fields = append(fields, "win_reparse_tag")
	}
	if !equalInt64(old.BSDFlags, entry.BSDFlags) {
		fields = append(fields, "bsd_flags")
	}
	return fields
}

// equalInt64 比較兩個可為 NULL 的整數欄位。
func equalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// equalString 比較兩個可為 NULL 的字串欄位。
func equalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// equalTimestamp 比較兩個可為 NULL 的時間欄位。
func equalTimestamp(left, right *model.Timestamp) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// applyHash 把摘要結果寫進條目。
//
// 成功時記下摘要、時間、對應的大小與修改時間；失敗時摘要一律清空：
// 重算失敗或無法確認穩定時，舊摘要不能繼續被當成本次的當前摘要。
func (b *builder) applyHash(current *plan, outcome hash.Result) {
	if outcome.Computed() {
		digest := outcome.Digest
		current.entry.HashSHA256 = &digest
		current.entry.HashState = model.HashStateComputed
		computedAt := model.NewTimestamp(b.now())
		current.entry.HashComputedAt = &computedAt
		size := outcome.Size
		current.entry.HashSize = &size
		current.entry.HashMTime = nil
		if outcome.Mtime != "" {
			mtime := outcome.Mtime
			current.entry.HashMTime = &mtime
		}
		current.entry.ErrorCode = nil
		current.entry.ErrorStage = nil
		b.result.Hashed++
		if current.old == nil || !hasRecordedHash(*current.old) {
			b.result.HashBaseline++
		}
		return
	}
	code := outcome.Code
	if code == "" {
		code = model.CodeHashRead
	}
	codeText := string(code)
	stage := errorStageHash
	current.entry.HashSHA256 = nil
	current.entry.HashState = model.HashStateFailed
	current.entry.HashComputedAt = nil
	current.entry.HashSize = nil
	current.entry.HashMTime = nil
	current.entry.ErrorCode = &codeText
	current.entry.ErrorStage = &stage
	b.entriesFailed = append(b.entriesFailed, scan.Gap{
		RelPath: current.entry.RelPath,
		Code:    code,
		Detail:  outcome.Detail,
	})
	b.result.HashFailed++
	if b.options.Stats != nil {
		b.options.Stats.AddErrorEvent(1)
	}
}
