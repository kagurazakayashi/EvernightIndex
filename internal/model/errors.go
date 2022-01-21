// Package model 定義 EvernightIndex 的領域模型與穩定錯誤碼。
//
// 本套件不得依賴 CLI、儲存或平台層：錯誤碼與退出碼是對外承諾的機器介面，
// 人類可讀說明由 i18n 資源提供，兩者必須分開維護。
package model

import (
	"fmt"
	"sort"
	"strings"
)

// ExitCode 是程序對外承諾的退出碼。
type ExitCode int

const (
	// ExitOK 表示成功，包括沒有查詢結果或沒有重複檔案。
	ExitOK ExitCode = 0
	// ExitFatal 表示致命執行錯誤。
	ExitFatal ExitCode = 1
	// ExitUsage 表示參數或格式不合法。
	ExitUsage ExitCode = 2
	// ExitPartial 表示部分失敗或仍有日誌待匯出。
	ExitPartial ExitCode = 3
	// ExitInterrupted 表示使用者中斷。
	ExitInterrupted ExitCode = 130
)

// ErrorCode 是穩定的機器標識，寫入日誌、JSONL 與錯誤輸出，不隨語言改變。
type ErrorCode string

const (
	// CodeMissingCommand 表示沒有給出子命令。
	CodeMissingCommand ErrorCode = "E_MISSING_COMMAND"
	// CodeUnknownCommand 表示子命令不存在。
	CodeUnknownCommand ErrorCode = "E_UNKNOWN_COMMAND"
	// CodeUnknownFlag 表示選項不存在。
	CodeUnknownFlag ErrorCode = "E_UNKNOWN_FLAG"
	// CodeFlagNotApplicable 表示選項存在但不適用於目前子命令。
	CodeFlagNotApplicable ErrorCode = "E_FLAG_NOT_APPLICABLE"
	// CodeFlagNeedsValue 表示需要值的選項沒有給值。
	CodeFlagNeedsValue ErrorCode = "E_FLAG_NEEDS_VALUE"
	// CodeInvalidValue 表示選項值不合法。
	CodeInvalidValue ErrorCode = "E_INVALID_VALUE"
	// CodeUnexpectedArgument 表示多餘的位置參數。
	CodeUnexpectedArgument ErrorCode = "E_UNEXPECTED_ARGUMENT"
	// CodeMissingArgument 表示缺少必要的位置參數。
	CodeMissingArgument ErrorCode = "E_MISSING_ARGUMENT"
	// CodeInvalidLanguage 表示顯式或環境指定的語言不合法。
	CodeInvalidLanguage ErrorCode = "E_INVALID_LANGUAGE"
	// CodeFlagConflict 表示互斥選項同時使用。
	CodeFlagConflict ErrorCode = "E_FLAG_CONFLICT"
	// CodeNotImplemented 表示命令已定義但本次建置尚未實作。
	CodeNotImplemented ErrorCode = "E_NOT_IMPLEMENTED"
	// CodeDatabaseForeign 表示目標檔案不是本程式建立的資料庫。
	CodeDatabaseForeign ErrorCode = "E_DATABASE_FOREIGN"
	// CodeDatabaseTooNew 表示資料庫格式版本高於本程式支援的版本。
	CodeDatabaseTooNew ErrorCode = "E_DATABASE_VERSION_TOO_NEW"
	// CodeDatabaseVersion 表示資料庫格式版本較舊或不受支援。
	CodeDatabaseVersion ErrorCode = "E_DATABASE_VERSION"
	// CodeDatabaseMissing 表示指定的資料庫檔案不存在。
	CodeDatabaseMissing ErrorCode = "E_DATABASE_MISSING"
	// CodeDatabaseCorrupt 表示資料庫完整性檢查失敗。
	CodeDatabaseCorrupt ErrorCode = "E_DATABASE_CORRUPT"
	// CodeDatabaseIO 表示資料庫讀寫失敗。
	CodeDatabaseIO ErrorCode = "E_DATABASE_IO"
	// CodeDatabaseLocked 表示在期限內無法取得正式資料庫的跨行程寫入鎖。
	CodeDatabaseLocked ErrorCode = "E_DATABASE_LOCKED"
	// CodeDatabaseUnknownSchema 表示資料庫標示的結構缺漏或無法辨識。
	CodeDatabaseUnknownSchema ErrorCode = "E_DATABASE_UNKNOWN_SCHEMA"
	// CodeRootMismatch 表示指定的根目錄與資料庫綁定的根目錄不一致。
	CodeRootMismatch ErrorCode = "E_ROOT_MISMATCH"
	// CodeIndexIncomplete 表示資料庫尚未完成任何一次發佈，不能當成完整索引查詢。
	CodeIndexIncomplete ErrorCode = "E_INDEX_INCOMPLETE"
	// CodePublishConflict 表示發佈前的代次或執行狀態檢查失敗，拒絕覆蓋他人結果。
	CodePublishConflict ErrorCode = "E_PUBLISH_CONFLICT"
	// CodePartial 表示部分完成：結果已發佈但有明確覆蓋缺口。
	CodePartial ErrorCode = "E_PARTIAL"
	// CodeExportPending 表示索引已發佈但變化日誌仍待匯出。
	CodeExportPending ErrorCode = "E_EXPORT_PENDING"
	// CodeInvalidPattern 表示排除或查詢用的名稱/路徑模式不合法。
	CodeInvalidPattern ErrorCode = "E_INVALID_PATTERN"
	// CodeRootUnavailable 表示掃描根目錄不存在、無法存取或身份明顯不符。
	CodeRootUnavailable ErrorCode = "E_ROOT_UNAVAILABLE"
	// CodeScanIO 表示讀取檔案系統資訊失敗；這不是「檔案已刪除」。
	CodeScanIO ErrorCode = "E_SCAN_IO"
	// CodeInternal 表示不應發生的內部錯誤。
	CodeInternal ErrorCode = "E_INTERNAL"
	// CodeInterrupted 表示使用者中斷。
	CodeInterrupted ErrorCode = "E_INTERRUPTED"
)

// Error 攜帶穩定錯誤碼、i18n 訊息鍵、佔位符參數與退出碼。
//
// Error() 只回傳機器可讀的摘要（錯誤碼、訊息鍵與排序後的參數），
// 使用者可見文字由呈現層依當前語言以 Key 與 Args 渲染。
type Error struct {
	Code ErrorCode
	Key  string
	Args map[string]string
	Exit ExitCode
	Err  error
}

// New 建立一個帶錯誤碼與訊息鍵的錯誤。
func New(code ErrorCode, key string, exit ExitCode, args map[string]string) *Error {
	return &Error{Code: code, Key: key, Args: args, Exit: exit}
}

// Wrap 建立一個保留底層原因的錯誤。
func Wrap(code ErrorCode, key string, exit ExitCode, args map[string]string, err error) *Error {
	return &Error{Code: code, Key: key, Args: args, Exit: exit, Err: err}
}

// Error 實作 error 介面，輸出穩定的機器可讀摘要。
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString(string(e.Code))
	if e.Key != "" {
		b.WriteString(" ")
		b.WriteString(e.Key)
	}
	if len(e.Args) > 0 {
		keys := make([]string, 0, len(e.Args))
		for k := range e.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%q", k, e.Args[k])
		}
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap 讓 errors.Is/errors.As 能取得底層原因。
func (e *Error) Unwrap() error { return e.Err }

// ExitCode 回傳此錯誤對應的退出碼。
func (e *Error) ExitCode() ExitCode {
	if e == nil || e.Exit == 0 {
		return ExitFatal
	}
	return e.Exit
}

// ExitCodeOf 取得任意錯誤對應的退出碼；非 model.Error 一律視為致命錯誤。
func ExitCodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	if e, ok := err.(*Error); ok {
		return e.ExitCode()
	}
	return ExitFatal
}
