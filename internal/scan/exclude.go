package scan

import (
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/match"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 本檔把「哪些路徑不進索引」集中成一個可重複使用的判定器。
//
// 掃描層用它決定不進入哪些目錄；索引層用它判斷先前已發佈的條目
// 是否因排除規則改變而移出索引（excluded，不是刪除）。
// 兩處必須用同一份規則，否則會出現「掃描不進來、卻把舊記錄當成刪除」的矛盾，
// 而那正好是保守刪除最不能犯的錯。

// Excluder 是排除規則的唯一實作。
//
// 判定順序固定：先比對本程式自產的檔案與目錄（精確路徑與前綴），
// 再用顯式模式比對相對路徑。順序本身是語意的一部分：
// 自產檔案的排除不能靠使用者的模式來表達，否則使用者改模式就會把資料庫索引進來。
type Excluder struct {
	patterns  *match.Set
	policy    model.CasePolicy
	selfFiles map[string]struct{}
	selfDirs  []string
}

// NewExcluder 建立判定器；patterns 可為 nil，表示沒有顯式模式。
func NewExcluder(patterns *match.Set, policy model.CasePolicy, selfFiles, selfDirs []string) *Excluder {
	files, dirs := buildSelfSets(selfFiles, selfDirs, policy)
	return &Excluder{patterns: patterns, policy: policy, selfFiles: files, selfDirs: dirs}
}

// Match 判斷一個路徑是否被排除，並回報原因。
//
// absPath 用於自產檔案比對（依綁定的大小寫策略折疊），
// relPath 用於模式比對（保留原始大小寫，是否符合由 match.Set 的設定決定）。
func (e *Excluder) Match(absPath, relPath string, isDir bool) (ExcludeReason, bool) {
	if e == nil {
		return "", false
	}
	folded := foldPath(absPath, e.policy)
	if _, ok := e.selfFiles[folded]; ok {
		return ExcludeReasonSelfProduced, true
	}
	for _, dir := range e.selfDirs {
		if folded == dir || strings.HasPrefix(folded, dir+"/") {
			return ExcludeReasonSelfProduced, true
		}
	}
	if e.patterns != nil && e.patterns.MatchPath(relPath, isDir) {
		return ExcludeReasonPattern, true
	}
	return "", false
}
