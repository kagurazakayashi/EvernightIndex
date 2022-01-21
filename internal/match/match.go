// Package match 把名稱與路徑的通配符模式編譯成可重複使用的比對器，
// 供掃描排除規則與 find 查詢共用同一套語意，避免兩處各自實作而出現
// 不一致的命中結果。
//
// 語意重點：
//
//   - 模式與被比較的路徑都以 "/" 分隔；模式中的 "\\" 一律視為分隔符，
//     本模組沒有轉義字元，反斜線不是跳脫。
//   - "*" 匹配 0 個或多個非分隔符字元；"?" 匹配恰好 1 個非分隔符字元；
//     "[abc]"、"[a-z]" 是字元類別，"[!abc]" 與 "[^abc]" 表示取反，
//     類別首位的 "]" 視為字面字元，字元類別一律不匹配分隔符。
//   - "**" 只有在整個 segment 時才有跨 segment 語意，代表 0 個或多個
//     segment；出現在 segment 內部（例如 "a**b"）是錯誤。
//   - 開頭的 "/" 表示錨定到根目錄，"/a/b" 與 "a/b" 的比對結果相同，
//     但 String() 仍回傳原始輸入。
//   - 不含分隔符的模式匹配任意深度的單一名稱；含分隔符的模式從根目錄錨定。
//   - 結尾的 "/" 只表示「只匹配目錄」，不影響路徑比對。
//   - 被比較的路徑一律是 "/" 分隔、相對根目錄、不以 "/" 開頭的字串，
//     比對前做與 model.PathKey 相同的收斂（"\\" 轉 "/"、重複的 "//"
//     收斂成 "/"）；根目錄自身是 "."。
//   - 非 UTF-8 位元組可以參與比對：字面值與 "*" 逐位元組處理，"?"
//     對無效 UTF-8 位元組前進 1 個位元組，字元類別則不匹配無效位元組。
package match

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// 錯誤原因使用固定英語短句，由 i18n 層以 reason 佔位符呈現，不隨語言改變。
const (
	reasonEmptyPattern  = "empty pattern"
	reasonUnclosedClass = "unclosed character class"
	reasonEmptyClass    = "empty character class"
	reasonDoubleStar    = "** must be a whole path segment"
	reasonInvalidRange  = "invalid character range"
)

// Options 控制匹配時的比較方式。
type Options struct {
	// Insensitive 為 true 時，模式與被比較的路徑都先轉成小寫再比較。
	//
	// 折疊只是 unicode.ToLower 等級的簡單小寫化，不是完整的 Unicode case
	// folding（例如 "ß" 不會展開成 "ss"）。無效 UTF-8 位元組保持原樣，
	// 以免位元組被替換成 U+FFFD 而改變比對結果。
	Insensitive bool
}

// itemKind 是單一 segment 內模式的節點種類。
type itemKind uint8

const (
	itemLiteral itemKind = iota // 字面值，lit 存已折疊的原始位元組
	itemStar                    // "*"，0 個或多個位元組
	itemAny                     // "?"，恰好 1 個 rune，無效位元組算 1 個位元組
	itemClass                   // "[...]"，字元類別
)

// runeRange 是字元類別中的一段閉區間。
type runeRange struct{ lo, hi rune }

// item 是 segment 模式的單一節點。
type item struct {
	kind itemKind
	lit  string      // itemLiteral 的字面位元組
	rngs []runeRange // itemClass 的成員；已依大小寫策略折疊
	neg  bool        // itemClass 是否取反
}

// segment 是一個路徑段的模式；globstar 表示整個 segment 是 "**"。
type segment struct {
	globstar bool
	items    []item
}

// Pattern 是編譯完成的模式，可安全地被多處重複使用（比對不會修改狀態）。
type Pattern struct {
	raw         string // 原始輸入，供 String() 回傳
	insensitive bool
	dirOnly     bool
	anchored    bool // 從根目錄錨定（含有效分隔符或以 "/" 開頭）
	segs        []segment
}

// Set 是一組依輸入順序編譯的模式；任一模式命中即視為命中。
type Set struct {
	patterns []string
	compiled []*Pattern
}

// Compile 編譯一個模式字串。
//
// 模式中的 "\\" 會先轉成 "/"（本模組沒有轉義字元），重複的分隔符會收斂成
// 單一個；開頭的 "/" 表示錨定到根目錄，結尾的 "/" 表示只匹配目錄。空模式、
// 未閉合或空白的字元類別、出現在 segment 內部的 "**"，以及顛倒的字元範圍
// 都會回傳 model.CodeInvalidPattern 錯誤；錯誤參數含原始模式與固定英語
// 短句 reason。被比較的路徑本身不會被編譯，只有 relPath 會在比對時收斂。
func Compile(pattern string, opts Options) (*Pattern, error) {
	raw := pattern
	if pattern == "" {
		return nil, invalidPattern(raw, reasonEmptyPattern)
	}

	body := strings.ReplaceAll(pattern, "\\", "/")
	for strings.Contains(body, "//") {
		body = strings.ReplaceAll(body, "//", "/")
	}
	if opts.Insensitive {
		body = foldCase(body)
	}

	anchored := false
	for strings.HasPrefix(body, "/") {
		body = body[1:]
		anchored = true
	}
	dirOnly := false
	for strings.HasSuffix(body, "/") {
		body = body[:len(body)-1]
		dirOnly = true
	}
	if body == "" {
		// 例如 "/" 或 "///"：只有分隔符，沒有可匹配的內容。
		return nil, invalidPattern(raw, reasonEmptyPattern)
	}
	if strings.Contains(body, "/") {
		anchored = true
	}

	parts := strings.Split(body, "/")
	segs := make([]segment, 0, len(parts))
	for _, part := range parts {
		if part == "**" {
			segs = append(segs, segment{globstar: true})
			continue
		}
		if strings.Contains(part, "**") {
			return nil, invalidPattern(raw, reasonDoubleStar)
		}
		items, err := compileSegment(part)
		if err != nil {
			return nil, invalidPattern(raw, err.Error())
		}
		segs = append(segs, segment{items: items})
	}

	return &Pattern{
		raw:         raw,
		insensitive: opts.Insensitive,
		dirOnly:     dirOnly,
		anchored:    anchored,
		segs:        segs,
	}, nil
}

// String 回傳編譯時使用的原始模式（未經大小寫折疊、仍保留原始分隔符寫法）。
func (p *Pattern) String() string {
	if p == nil {
		return ""
	}
	return p.raw
}

// DirOnly 表示模式結尾帶 "/"，只匹配目錄。
func (p *Pattern) DirOnly() bool {
	if p == nil {
		return false
	}
	return p.dirOnly
}

// Anchored 表示模式從根目錄錨定：含有效分隔符（例如 "a/b"）或以 "/" 開頭。
//
// 結尾的 "/" 只表示 DirOnly，不使模式錨定，因此 "build/" 的 Anchored() 為
// false，仍以「任意深度的單一名稱」比對。
func (p *Pattern) Anchored() bool {
	if p == nil {
		return false
	}
	return p.anchored
}

// MatchPath 比對相對根目錄的 "/" 分隔路徑。
//
// relPath 不以 "/" 開頭；根目錄自身相對於根是 "."。比對前會做與
// model.PathKey 相同的收斂（"\\" 轉 "/"、重複的 "//" 收斂成 "/"），
// 因此 "a//b" 與 "a\\b" 都等同 "a/b"。
//
// 不含分隔符的模式匹配路徑中任一層的單一名稱；含分隔符的模式（以及結尾
// "/" 以外的錨定寫法）從根目錄整段比對。"**" 代表 0 個或多個 segment，
// 所以 "a/**" 也匹配 "a" 本身，"a/**/b" 也匹配 "a/b"。
//
// relPath 含分隔符時，"*"、"?" 與字元類別都不會跨越 segment，因為比對是
// 逐 segment 進行的。
func (p *Pattern) MatchPath(relPath string, isDir bool) bool {
	if p == nil {
		return false
	}
	if p.dirOnly && !isDir {
		return false
	}
	path := normalizePath(relPath)
	if p.insensitive {
		path = foldCase(path)
	}
	segs := splitPath(path)

	if !p.anchored {
		// 名稱模式：命中任一層的單一名稱即命中。
		if p.segs[0].globstar {
			// 單一段的 "**" 匹配任意路徑，包含根目錄自身。
			return true
		}
		for _, name := range segs {
			if matchItems(p.segs[0].items, name) {
				return true
			}
		}
		return false
	}
	return matchSegments(p.segs, segs)
}

// MatchName 只用單一名稱比對，不做路徑收斂。
//
// 這個方法只在模式不含分隔符時有意義：錨定模式（例如 "a/b"）一律回傳
// false，需要比對整條路徑時請用 MatchPath。結尾 "/" 的 DirOnly 不在此
// 判斷，呼叫端要自行檢查 DirOnly() 決定是否只看目錄。單一段的 "**"
// 模式對任何名稱都命中。
func (p *Pattern) MatchName(name string) bool {
	if p == nil || p.anchored || len(p.segs) == 0 {
		return false
	}
	if p.segs[0].globstar {
		return true
	}
	if p.insensitive {
		name = foldCase(name)
	}
	return matchItems(p.segs[0].items, name)
}

// CompileAll 依序編譯多個模式；任一失敗即回傳錯誤（錯誤中包含出錯的模式）。
func CompileAll(patterns []string, opts Options) (*Set, error) {
	set := &Set{patterns: make([]string, 0, len(patterns)), compiled: make([]*Pattern, 0, len(patterns))}
	for _, pattern := range patterns {
		p, err := Compile(pattern, opts)
		if err != nil {
			return nil, err
		}
		set.patterns = append(set.patterns, pattern)
		set.compiled = append(set.compiled, p)
	}
	return set, nil
}

// MatchPath 任一模式命中即回傳 true。
func (s *Set) MatchPath(relPath string, isDir bool) bool {
	if s == nil {
		return false
	}
	for _, p := range s.compiled {
		if p.MatchPath(relPath, isDir) {
			return true
		}
	}
	return false
}

// MatchName 任一模式命中即回傳 true。
func (s *Set) MatchName(name string) bool {
	if s == nil {
		return false
	}
	for _, p := range s.compiled {
		if p.MatchName(name) {
			return true
		}
	}
	return false
}

// Len 回傳模式數。
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.compiled)
}

// Patterns 回傳編譯時使用的原始模式字串（保持輸入順序）。
//
// 回傳的是副本，呼叫端修改不影響 Set。
func (s *Set) Patterns() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.patterns))
	copy(out, s.patterns)
	return out
}

// Empty 表示沒有任何模式。
func (s *Set) Empty() bool {
	return s == nil || len(s.compiled) == 0
}

// invalidPattern 建立本專案統一的模式錯誤，沿用既有的錯誤碼與訊息鍵。
func invalidPattern(rawPattern, reason string) *model.Error {
	return model.New(model.CodeInvalidPattern, "err.invalidPattern", model.ExitUsage,
		map[string]string{"pattern": rawPattern, "reason": reason})
}

// compileSegment 把單一 segment 的模式文字編譯成節點序列。
//
// 回傳的 error 只帶固定的英語原因短句，由 Compile 包成 model 錯誤。
func compileSegment(part string) ([]item, error) {
	items := make([]item, 0, len(part))
	for i := 0; i < len(part); {
		switch part[i] {
		case '*':
			items = append(items, item{kind: itemStar})
			i++
		case '?':
			items = append(items, item{kind: itemAny})
			i++
		case '[':
			it, next, err := compileClass(part, i)
			if err != nil {
				return nil, err
			}
			items = append(items, it)
			i = next
		default:
			_, size := utf8.DecodeRuneInString(part[i:])
			// 無效 UTF-8 位元組 size 為 1，仍以原始位元組當字面值。
			items = append(items, item{kind: itemLiteral, lit: part[i : i+size]})
			i += size
		}
	}
	return items, nil
}

// compileClass 從 part[start] 的 "[" 開始解析一個字元類別。
//
// 回傳類別節點與下一個待處理的位置。類別首位的 "]" 視為字面字元，但若整個
// 模式沒有第二個 "]" 可以收尾，則該 "]" 直接結束類別，形成空類別錯誤。
func compileClass(part string, start int) (item, int, error) {
	i := start + 1
	if i >= len(part) {
		return item{}, 0, errUnclosedClass
	}
	neg := false
	if part[i] == '!' || part[i] == '^' {
		neg = true
		i++
	}

	var rngs []runeRange
	if i < len(part) && part[i] == ']' {
		if strings.IndexByte(part[i+1:], ']') < 0 {
			return item{}, 0, errEmptyClass
		}
		rngs = append(rngs, runeRange{lo: ']', hi: ']'})
		i++
	}

	closed := false
	for i < len(part) {
		if part[i] == ']' {
			closed = true
			i++
			break
		}
		lo, size := decodeClassRune(part[i:])
		i += size
		if i < len(part) && part[i] == '-' && i+1 < len(part) && part[i+1] != ']' {
			hi, hiSize := decodeClassRune(part[i+1:])
			if hi < lo {
				return item{}, 0, errInvalidRange
			}
			rngs = append(rngs, runeRange{lo: lo, hi: hi})
			i += 1 + hiSize
			continue
		}
		rngs = append(rngs, runeRange{lo: lo, hi: lo})
	}
	if !closed {
		return item{}, 0, errUnclosedClass
	}
	if len(rngs) == 0 {
		return item{}, 0, errEmptyClass
	}
	return item{kind: itemClass, rngs: rngs, neg: neg}, i, nil
}

// decodeClassRune 解出類別內的成員；無效 UTF-8 位元組以自身數值當 rune。
func decodeClassRune(s string) (rune, int) {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return utf8.RuneError, 1
	}
	return r, size
}

// matchItems 判斷單一 segment 的節點序列是否完整匹配 s。
//
// 用可達位置集合做前向動態規劃：每個節點處理一次，避免 "*" 造成回溯爆炸。
func matchItems(items []item, s string) bool {
	reach := make([]bool, len(s)+1)
	reach[0] = true
	next := make([]bool, len(s)+1)
	for _, it := range items {
		for i := range next {
			next[i] = false
		}
		for p := 0; p <= len(s); p++ {
			if !reach[p] {
				continue
			}
			switch it.kind {
			case itemStar:
				for q := p; q <= len(s); q++ {
					next[q] = true
				}
			case itemAny:
				if p < len(s) {
					_, size := utf8.DecodeRuneInString(s[p:])
					next[p+size] = true
				}
			case itemLiteral:
				if strings.HasPrefix(s[p:], it.lit) {
					next[p+len(it.lit)] = true
				}
			case itemClass:
				if p >= len(s) {
					continue
				}
				r, size := utf8.DecodeRuneInString(s[p:])
				if r == utf8.RuneError && size == 1 {
					// 無效 UTF-8 位元組：字元類別不匹配。
					continue
				}
				if matchClass(it, r) {
					next[p+size] = true
				}
			}
		}
		reach, next = next, reach
	}
	return reach[len(s)]
}

// matchClass 判斷 r 是否屬於類別；neg 時取反。
func matchClass(it item, r rune) bool {
	in := false
	for _, rg := range it.rngs {
		if r >= rg.lo && r <= rg.hi {
			in = true
			break
		}
	}
	return in != it.neg
}

// matchSegments 判斷模式段序列是否完整匹配路徑段序列。
//
// "**" 可跨 0 個或多個 segment，因此 "a/**/b" 匹配 "a/b"，"a/**" 匹配 "a"。
func matchSegments(pat []segment, path []string) bool {
	reach := make([]bool, len(path)+1)
	reach[0] = true
	next := make([]bool, len(path)+1)
	for _, sg := range pat {
		for i := range next {
			next[i] = false
		}
		for i := 0; i <= len(path); i++ {
			if !reach[i] {
				continue
			}
			if sg.globstar {
				for j := i; j <= len(path); j++ {
					next[j] = true
				}
				continue
			}
			if i < len(path) && matchItems(sg.items, path[i]) {
				next[i+1] = true
			}
		}
		reach, next = next, reach
	}
	return reach[len(path)]
}

// normalizePath 收斂被比較的路徑：統一成 "/"、收斂重複分隔符、去掉開頭
// 與結尾的分隔符。根目錄自身 "." 保持原樣。
func normalizePath(path string) string {
	s := strings.ReplaceAll(path, "\\", "/")
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	for strings.HasPrefix(s, "/") {
		s = s[1:]
	}
	for len(s) > 1 && strings.HasSuffix(s, "/") {
		s = s[:len(s)-1]
	}
	return s
}

// splitPath 把收斂後的路徑切成 segment；根目錄 "." 與空字串沒有 segment。
func splitPath(path string) []string {
	if path == "" || path == "." {
		return nil
	}
	return strings.Split(path, "/")
}

// foldCase 做大小寫折疊：逐 rune 使用 unicode.ToLower，並保留無效 UTF-8
// 位元組原樣（strings.ToLower 會把無效位元組換成 U+FFFD，等於丟棄位元組）。
func foldCase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteRune(unicode.ToLower(r))
		i += size
	}
	return b.String()
}

var (
	errUnclosedClass = errorString(reasonUnclosedClass)
	errEmptyClass    = errorString(reasonEmptyClass)
	errInvalidRange  = errorString(reasonInvalidRange)
)

// errorString 是只攜帶固定原因短句的內部錯誤型別。
type errorString string

// Error 實作 error 介面，回傳固定的英語原因短句。
func (e errorString) Error() string { return string(e) }
