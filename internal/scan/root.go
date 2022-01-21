package scan

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/fsmeta"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔集中處理「路徑從哪裡來」與「根目錄是什麼」。
//
// 三個位置必須分清楚，混用會讓相對路徑在錯誤的目錄下解析：
//   - 呼叫時工作目錄（call working directory）：使用者下命令時所在的位置，
//     所有顯式相對路徑（資料庫、日誌、暫存、掃描根目錄）都以它為基準；
//   - 掃描根目錄：解析成真實絕對路徑後的走訪起點，所有條目路徑都以它為基準；
//   - 輸出位置：資料庫、日誌、暫存工作目錄各自獨立，不因前兩者改變。

// Root 是解析後的掃描根目錄。
type Root struct {
	// Requested 是呼叫端給的路徑，只做絕對化，不解析連結。
	Requested string
	// AbsPath 是解析符號連結後的絕對路徑；之後所有條目都相對於它。
	AbsPath string
	// Info 是根目錄自身的中繼資料。
	Info fsmeta.Info
}

// CasePolicy 探測根目錄所在環境的路徑大小寫策略。
//
// probed 為 false 表示路徑裡沒有可供反轉的 ASCII 字母，回傳的是平台預設值；
// 呼叫端必須把這種情況記成「未探測」，不能宣稱已測量。
func (r Root) CasePolicy() (model.CasePolicy, bool, error) {
	return fsmeta.ProbeCasePolicy(r.AbsPath)
}

// ResolvePath 把一個顯式路徑按呼叫時工作目錄解析成絕對路徑。
//
// 這是「顯式相對路徑一律相對於呼叫時工作目錄」的唯一實作點：
// 它不看行程目前的工作目錄（行程可能已被切換），也不受後續建立臨時目錄影響。
func ResolvePath(callCWD, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "path is empty",
		})
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed), nil
	}
	if !filepath.IsAbs(callCWD) {
		return "", model.New(model.CodeInternal, "err.internal", model.ExitFatal, map[string]string{
			"detail": "call working directory is not absolute: " + callCWD,
		})
	}
	return filepath.Clean(filepath.Join(callCWD, trimmed)), nil
}

// errRootNotDirectory 表示給定的根路徑存在但不是目錄。
var errRootNotDirectory = errors.New("root is not a directory")

// ResolveRoot 解析根目錄並採集它的中繼資料。
//
// 連結會被解析一次：同一個目錄透過連結與真實路徑掃描必須產生同一組相對路徑，
// 否則同一個資料庫會因為使用者換了寫法而看到兩套路徑。
// 根目錄不存在、不可存取或不是目錄時回報 E_ROOT_UNAVAILABLE，且不會清空既有索引。
func ResolveRoot(path string) (Root, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Root{}, rootUnavailable(path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Root{}, rootUnavailable(abs, err)
	}
	info, err := fsmeta.Collect(resolved)
	if err != nil {
		return Root{}, rootUnavailable(resolved, err)
	}
	if !info.IsDir() {
		return Root{}, rootUnavailable(resolved, errRootNotDirectory)
	}
	return Root{Requested: abs, AbsPath: resolved, Info: info}, nil
}

// rootUnavailable 建立根目錄不可用的穩定錯誤。
func rootUnavailable(path string, cause error) error {
	detail := "unknown error"
	if cause != nil {
		detail = cause.Error()
	}
	return model.Wrap(model.CodeRootUnavailable, "err.rootUnavailable", model.ExitFatal,
		map[string]string{"path": model.EscapeDisplay(path), "reason": detail}, cause)
}
