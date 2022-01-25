package index

import (
	"context"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/scan"
)

// 本檔處理「這次沒有再出現」的已發佈條目，也就是刪除判定。
//
// 唯一依據是掃描層的覆蓋證據（Coverage）：只有整棵樹走完、而且某條路徑
// 不落在任何缺口或排除區域裡，才能確認它真的不見了。判定順序固定：
//
//	1. 目前仍命中排除規則（含本程式自產檔案）→ excluded（不是刪除）；
//	2. 落在被排除的目錄或跨檔案系統的掛載點之下 → excluded 或 unverified；
//	3. 落在列舉失敗或列舉不全的目錄之下 → unverified（保留舊值）；
//	4. 其餘 → missing（暫存刪除，發佈時才真的移除）。
//
// 「保留」與「刪除」的差別是資料安全的核心：卷離線、目錄無權限、身分不明
// 都只會走到第 3 條，絕不會清空舊記錄。

// retireVerdict 是對「這次沒再出現」的已發佈條目的處置。
type retireVerdict int

const (
	// retireMissing 表示覆蓋證據充分，可以確認路徑已消失。
	retireMissing retireVerdict = iota
	// retireExcluded 表示因排除規則移出索引（不是刪除）。
	retireExcluded
	// retireUnverified 表示所在區域這次沒有被成功覆蓋，保留舊值並標明未驗證。
	retireUnverified
)

// region 是一段「沒有成功覆蓋」的相對路徑前綴。
type region struct {
	prefix  string
	verdict retireVerdict
}

// buildRegions 由覆蓋證據建立未覆蓋區域清單。
func (b *builder) buildRegions(coverage scan.Coverage) []region {
	regions := make([]region, 0, len(coverage.ExcludedDirs)+len(coverage.FailedDirs)+len(coverage.PartialDirs))
	// 被排除的區域先放：同一段路徑若同時落在排除與缺口描述裡，排除語意優先，
	// 因為那是使用者明確選擇的結果，而缺口只是「這次沒看到」。
	for _, excluded := range coverage.ExcludedDirs {
		verdict := retireExcluded
		if excluded.Reason == scan.ExcludeReasonCrossDevice {
			// 跨檔案系統的掛載點只是沒有被進入：保留舊記錄並標明未驗證，
			// 否則一次 --one-file-system 就會讓整個子樹看起來像被刪除。
			verdict = retireUnverified
		}
		regions = append(regions, region{prefix: b.foldRel(excluded.RelPath), verdict: verdict})
	}
	for _, gap := range coverage.FailedDirs {
		regions = append(regions, region{prefix: b.foldRel(gap.RelPath), verdict: retireUnverified})
	}
	for _, gap := range coverage.PartialDirs {
		regions = append(regions, region{prefix: b.foldRel(gap.RelPath), verdict: retireUnverified})
	}
	return regions
}

// foldRel 把相對路徑折成可比較的形式（統一分隔符，必要時折疊大小寫）。
func (b *builder) foldRel(relPath string) string {
	if relPath == "" {
		return "."
	}
	return model.PathKey(relPath, b.options.CasePolicy)
}

// matchRegion 判斷相對路徑是否落在某個未成功覆蓋的區域內。
func (b *builder) matchRegion(relPath string) (retireVerdict, bool) {
	folded := b.foldRel(relPath)
	for _, region := range b.regions {
		if region.prefix == "." || folded == region.prefix || strings.HasPrefix(folded, region.prefix+"/") {
			return region.verdict, true
		}
	}
	return retireMissing, false
}

// retire 逐條處置「本次不再出現」的已發佈條目。
//
// 它必須在整棵樹走完之後執行：走訪中途中止時沒有覆蓋證據，
// 呼叫端根本不會走到這裡。
func (b *builder) retire(ctx context.Context) error {
	err := b.target.Unseen(ctx, func(old model.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return b.retireOne(ctx, old)
	})
	if err != nil {
		return err
	}
	return b.flushPending(ctx)
}

// retireOne 對單一消失條目做處置。
func (b *builder) retireOne(ctx context.Context, old model.Entry) error {
	if old.RelPath == "." {
		// 根目錄自身的條目一律保留：沒有它索引就失去錨點，而且它只會在
		// 整棵樹根本沒走完時缺席，那時呼叫端已經在寫入暫存前結束。
		return b.markRetired(ctx, old, retireUnverified)
	}
	isDir := old.Type == model.EntryTypeDir
	if reason, excluded := b.excluder.Match(old.AbsPath, old.RelPath, isDir); excluded {
		return b.markRetired(ctx, old, verdictOf(reason))
	}
	if verdict, inside := b.matchRegion(old.RelPath); inside {
		return b.markRetired(ctx, old, verdict)
	}
	return b.markMissing(ctx, old)
}

// verdictOf 把排除原因轉成處置：跨檔案系統只是沒進入，其餘是使用者要的排除。
func verdictOf(reason scan.ExcludeReason) retireVerdict {
	if reason == scan.ExcludeReasonCrossDevice {
		return retireUnverified
	}
	return retireExcluded
}

// markRetired 保留舊記錄並標明 excluded 或 unverified。
func (b *builder) markRetired(ctx context.Context, old model.Entry, verdict retireVerdict) error {
	status := model.StatusExcluded
	if verdict == retireUnverified {
		status = model.StatusUnverified
		b.result.Unverified++
	} else {
		b.result.Excluded++
		observe(b.options.Observe, model.OpExcluded, old.RelPath)
	}
	if old.Status == status {
		// 已經是這個狀態：不重複暫存，也不重複產生事件。
		return nil
	}
	updated := old
	updated.Status = status
	b.pendingUpserts = append(b.pendingUpserts, updated)
	if status == model.StatusExcluded {
		if err := b.recordRetiredEvent(old, updated); err != nil {
			return err
		}
	}
	return b.maybeFlush(ctx)
}

// markMissing 確認路徑消失：暫存刪除並記錄事件，發佈時才真的移除。
func (b *builder) markMissing(ctx context.Context, old model.Entry) error {
	b.result.Removed++
	if b.options.Stats != nil {
		b.options.Stats.AddRemoved(1)
	}
	observe(b.options.Observe, model.OpRemoved, old.RelPath)
	b.pendingDeletions = append(b.pendingDeletions, old.PathKey)
	if err := b.recordRemovedEvent(old); err != nil {
		return err
	}
	return b.maybeFlush(ctx)
}

// maybeFlush 在緩衝達到批次大小時寫入暫存，讓記憶體維持在批次級別。
func (b *builder) maybeFlush(ctx context.Context) error {
	if len(b.pendingUpserts) < b.batchSize && len(b.pendingDeletions) < b.batchSize {
		return nil
	}
	return b.flushPending(ctx)
}

// flushPending 把緩衝中的處置寫進暫存區。
func (b *builder) flushPending(ctx context.Context) error {
	if len(b.pendingUpserts) > 0 {
		if err := b.target.Stage(ctx, b.pendingUpserts); err != nil {
			return err
		}
		b.result.Entries += int64(len(b.pendingUpserts))
		b.result.Batches++
		b.pendingUpserts = nil
	}
	if len(b.pendingDeletions) > 0 {
		if err := b.target.StageDeletions(ctx, b.pendingDeletions); err != nil {
			return err
		}
		b.pendingDeletions = nil
	}
	return nil
}
