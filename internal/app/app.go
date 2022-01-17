// Package app 編排任務：命令列層只負責解析與呈現，平台採集、索引與儲存細節在此組合。
package app

import (
	"context"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// Request 是一次命令請求。
//
// 後續步驟會在此加入根目錄、資料庫路徑、排除規則與輸出選項；
// 本步驟先固定「命令 + 位置參數」這個最小邊界，避免提前定型業務參數。
type Request struct {
	Command string
	Args    []string
}

// Service 是應用服務入口。
type Service struct{}

// New 建立應用服務。
func New() *Service { return &Service{} }

// Run 執行請求。
//
// 本步驟只建立邊界：所有業務命令都明確回報尚未實作，不回傳空結果假裝成功，
// 也不建立資料庫或掃描目錄。後續步驟在此接入掃描、索引更新、查詢與去重。
func (s *Service) Run(ctx context.Context, request Request) error {
	if err := ctx.Err(); err != nil {
		return model.Wrap(model.CodeInterrupted, "err.interrupted", model.ExitInterrupted, nil, err)
	}
	return model.New(model.CodeNotImplemented, "err.notImplemented", model.ExitFatal, map[string]string{
		"command": request.Command,
	})
}
