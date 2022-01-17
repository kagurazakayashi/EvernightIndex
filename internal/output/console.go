// Package output 管理結果與診斷的分流、詳細度，以及未來每秒統計的呈現位置。
package output

import (
	"fmt"
	"io"
)

// Console 把結果寫入標準輸出、把進度與診斷寫入標準錯誤。
//
// 契約：標準輸出只放查詢結果、重複檔案結果或使用者要求的結構化結果；
// 進度、警告、錯誤、逐路徑記錄與最終統計一律寫入標準錯誤，
// 以免污染以管線讀取路徑的呼叫端。
type Console struct {
	out      io.Writer
	errOut   io.Writer
	quiet    bool
	verbose  bool
	progress bool
}

// New 建立主控台；進度預設開啟。
func New(out, errOut io.Writer) *Console {
	return &Console{out: out, errOut: errOut, progress: true}
}

// Configure 套用 --quiet、--verbose 與 --progress 的解析結果。
//
// --quiet 優先關閉一般進度，即使同時給了 --progress=true；錯誤訊息不受影響。
func (c *Console) Configure(quiet, verbose, progress bool) {
	c.quiet = quiet
	c.verbose = verbose
	c.progress = progress && !quiet
}

// VerboseEnabled 表示是否需要輸出逐路徑記錄。
func (c *Console) VerboseEnabled() bool { return c.verbose }

// ProgressEnabled 表示是否輸出一般進度。
func (c *Console) ProgressEnabled() bool { return c.progress }

// Result 寫入標準輸出的結果行。
func (c *Console) Result(format string, args ...any) {
	fmt.Fprintf(c.out, format+"\n", args...)
}

// Note 寫入標準錯誤的一般訊息；--quiet 時不輸出。
func (c *Console) Note(format string, args ...any) {
	if c.quiet {
		return
	}
	fmt.Fprintf(c.errOut, format+"\n", args...)
}

// Verbosef 寫入逐路徑記錄；只有 --verbose 時輸出。
func (c *Console) Verbosef(format string, args ...any) {
	if !c.verbose {
		return
	}
	fmt.Fprintf(c.errOut, format+"\n", args...)
}

// Error 寫入錯誤訊息；--quiet 也不抑制。
func (c *Console) Error(format string, args ...any) {
	fmt.Fprintf(c.errOut, format+"\n", args...)
}
