package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

//go:embed catalogs/*.json
var catalogFS embed.FS

// Catalog 是載入後的訊息資源集合，按語言索引鍵值。
type Catalog struct {
	messages map[Lang]map[string]string
}

var (
	defaultOnce sync.Once
	defaultCat  *Catalog
	defaultErr  error
)

// Default 回傳內嵌的四語言資源。內嵌資源損壞屬於建置缺陷，會回傳錯誤。
func Default() (*Catalog, error) {
	defaultOnce.Do(func() { defaultCat, defaultErr = loadFS(catalogFS) })
	return defaultCat, defaultErr
}

// MustDefault 供程式啟動與測試使用；資源缺損時直接 panic，避免靜默降級。
func MustDefault() *Catalog {
	c, err := Default()
	if err != nil {
		panic(err)
	}
	return c
}

func loadFS(fsys fs.FS) (*Catalog, error) {
	entries, err := fs.ReadDir(fsys, "catalogs")
	if err != nil {
		return nil, fmt.Errorf("read catalogs: %w", err)
	}
	cat := &Catalog{messages: make(map[Lang]map[string]string, len(Supported))}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		tag := strings.TrimSuffix(entry.Name(), ".json")
		lang, ok := Parse(tag)
		if !ok || lang == LangAuto {
			return nil, fmt.Errorf("catalog name is not a supported language: %s", entry.Name())
		}
		raw, err := fs.ReadFile(fsys, path.Join("catalogs", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		messages := map[string]string{}
		if err := json.Unmarshal(raw, &messages); err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		for key, value := range messages {
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%s: message %q is empty", entry.Name(), key)
			}
		}
		cat.messages[lang] = messages
	}
	for _, lang := range Supported {
		if _, ok := cat.messages[lang]; !ok {
			return nil, fmt.Errorf("missing catalog for %s", lang)
		}
	}
	return cat, nil
}

// T 依語言取得訊息並替換 {name} 佔位符。
//
// 目前語言的鍵缺失時回退英語；英語也缺失時回傳鍵名本身，
// 讓缺項在畫面上可見，而不是顯示空白。
func (c *Catalog) T(lang Lang, key string, args map[string]string) string {
	if c == nil {
		return key
	}
	text, ok := c.messages[lang][key]
	if !ok {
		text, ok = c.messages[Fallback][key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return text
	}
	return substitute(text, args)
}

// substitute 以手寫掃描取代 {name}，避免為單一用途引入正則成本。
func substitute(text string, args map[string]string) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			b.WriteByte(text[i])
			continue
		}
		end := strings.IndexByte(text[i:], '}')
		if end < 0 {
			b.WriteString(text[i:])
			break
		}
		name := text[i+1 : i+end]
		if value, ok := args[name]; ok {
			b.WriteString(value)
		} else {
			b.WriteString(text[i : i+end+1])
		}
		i += end
	}
	return b.String()
}

// Placeholders 回傳訊息中出現的佔位符名稱（已排序），供資源一致性檢查使用。
func Placeholders(text string) []string {
	var names []string
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		end := strings.IndexByte(text[i:], '}')
		if end < 0 {
			break
		}
		names = append(names, text[i+1:i+end])
		i += end
	}
	sort.Strings(names)
	return names
}

// Problem 描述一項資源一致性問題。
type Problem struct {
	Kind   string
	Lang   Lang
	Key    string
	Detail string
}

// Check 檢查各語言鍵集、佔位符與空值是否一致；空結果代表資源可用。
func (c *Catalog) Check() []Problem {
	if c == nil {
		return []Problem{{Kind: "catalog", Detail: "catalog is nil"}}
	}
	base := c.messages[Fallback]
	var problems []Problem
	for _, lang := range Supported {
		messages, ok := c.messages[lang]
		if !ok {
			problems = append(problems, Problem{Kind: "missing-catalog", Lang: lang})
			continue
		}
		for key, value := range base {
			text, ok := messages[key]
			if !ok {
				problems = append(problems, Problem{Kind: "missing-key", Lang: lang, Key: key})
				continue
			}
			if strings.TrimSpace(text) == "" {
				problems = append(problems, Problem{Kind: "empty-value", Lang: lang, Key: key})
				continue
			}
			want := strings.Join(Placeholders(value), ",")
			got := strings.Join(Placeholders(text), ",")
			if want != got {
				problems = append(problems, Problem{
					Kind:   "placeholder-mismatch",
					Lang:   lang,
					Key:    key,
					Detail: "want [" + want + "], got [" + got + "]",
				})
			}
		}
		for key := range messages {
			if _, ok := base[key]; !ok {
				problems = append(problems, Problem{Kind: "extra-key", Lang: lang, Key: key})
			}
		}
	}
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Lang != problems[j].Lang {
			return problems[i].Lang < problems[j].Lang
		}
		return problems[i].Key < problems[j].Key
	})
	return problems
}

// Keys 回傳英語資源的排序鍵清單，供測試與文件工具使用。
func (c *Catalog) Keys() []string {
	if c == nil {
		return nil
	}
	keys := make([]string, 0, len(c.messages[Fallback]))
	for key := range c.messages[Fallback] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
