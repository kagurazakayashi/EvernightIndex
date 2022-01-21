//go:build !windows && !darwin && !linux

package fsmeta

import (
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// Collect 在其他平台明確拒絕，而不是回報一半正確的中繼資料。
//
// 本專案的目標平台是 Windows、macOS 與 Linux；其他平台的 Stat_t 版面與可用欄位
// 都不同，與其猜測，不如讓掃描直接失敗並說清楚原因。
func Collect(path string) (Info, error) {
	return Info{}, model.New(model.CodeScanIO, "err.scanIO", model.ExitFatal,
		map[string]string{"path": path, "reason": "platform not supported"})
}

// defaultCasePolicy 是無法探測時的平台預設值（未探測，不是測量結果）。
func defaultCasePolicy() model.CasePolicy { return model.CaseSensitive }
