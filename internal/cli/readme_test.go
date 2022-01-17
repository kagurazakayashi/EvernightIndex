package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 四份 README 的參數表是人工維護的公開文件。此測試把表格與集中宣告對照，
// 讓「文件漏掉參數」「短名寫錯」在測試階段就失敗，而不是等使用者發現。

var readmeFiles = []string{"README.md", "README.zh-CN.md", "README.zh-TW.md", "README.ja-JP.md"}

var readmeFlagRow = regexp.MustCompile("^\\| \u0060--([a-z0-9-]+)\u0060 \\| \u0060-([a-zA-Z])\u0060 \\|")

func readReadme(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

// TestReadmesDocumentEveryFlag 確認四份 README 的參數表與集中宣告完全一致。
func TestReadmesDocumentEveryFlag(t *testing.T) {
	want := map[string]string{}
	for _, spec := range flagRegistry {
		want[spec.name] = string(spec.short)
	}
	for _, name := range readmeFiles {
		content := readReadme(t, name)
		got := map[string]string{}
		for _, line := range strings.Split(content, "\n") {
			match := readmeFlagRow.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			if previous, ok := got[match[1]]; ok && previous != match[2] {
				t.Errorf("%s: --%s documented with two short names", name, match[1])
			}
			got[match[1]] = match[2]
		}
		var missing, extra, wrong []string
		for flag, short := range want {
			gotShort, ok := got[flag]
			if !ok {
				missing = append(missing, flag)
				continue
			}
			if gotShort != short {
				wrong = append(wrong, flag+"=-"+gotShort+" (want -"+short+")")
			}
		}
		for flag := range got {
			if _, ok := want[flag]; !ok {
				extra = append(extra, flag)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s: option table is missing %v", name, missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s: option table documents undeclared options %v", name, extra)
		}
		if len(wrong) > 0 {
			t.Errorf("%s: wrong short names %v", name, wrong)
		}
	}
}

// TestReadmesCrossLinkAndAttribute 確認四份文件互鏈、標明授權與署名，
// 且都提到每個已定義的命令與目前版本。
func TestReadmesCrossLinkAndAttribute(t *testing.T) {
	for _, name := range readmeFiles {
		content := readReadme(t, name)
		for _, link := range readmeFiles {
			if !strings.Contains(content, "("+link+")") {
				t.Errorf("%s: missing link to %s", name, link)
			}
		}
		for _, want := range []string{"MulanPSL-2.0", "KagurazakaMiyabi(KagurazakaYashi)", "LICENSE", "0.1.0-dev"} {
			if !strings.Contains(content, want) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
		for _, command := range commandRegistry {
			if !strings.Contains(content, "\u0060"+command.name+"\u0060") {
				t.Errorf("%s: does not document command %s", name, command.name)
			}
		}
	}
}
