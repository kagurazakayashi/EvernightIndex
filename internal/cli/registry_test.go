package cli

import (
	"sort"
	"strings"
	"testing"

	"github.com/kagurazakayashi/EvernightIndex/internal/i18n"
)

// plannedFlags 是功能清單 8.3 節固定的公開參數長名稱集合。
// 少一個或多一個都代表介面漂移，因此在此凍結成檢查清單。
var plannedFlags = []string{
	"help", "version", "lang", "verbose", "quiet", "progress",
	"db", "log-dir", "temp-dir", "sha256", "deep", "change-log", "exclude",
	"one-file-system", "workers", "lock-timeout", "read-retries",
	"name", "path", "filter", "case", "details", "fields", "sort", "limit",
	"offset", "format", "null", "db-a", "db-b", "match", "verify", "run",
}

// TestPlannedFlagSetIsDeclared 確認功能清單中的參數全部集中宣告，沒有遺漏或臨時新增。
func TestPlannedFlagSetIsDeclared(t *testing.T) {
	declared := make([]string, 0, len(flagRegistry))
	for _, spec := range flagRegistry {
		declared = append(declared, spec.name)
	}
	sort.Strings(declared)
	planned := append([]string(nil), plannedFlags...)
	sort.Strings(planned)
	if strings.Join(declared, ",") != strings.Join(planned, ",") {
		t.Fatalf("declared flags = %v, planned flags = %v", declared, planned)
	}
}

// TestFlagNamesAreUnique 確認長名稱與短名稱（區分大小寫）都不衝突。
func TestFlagNamesAreUnique(t *testing.T) {
	longNames := map[string]bool{}
	shortNames := map[rune]string{}
	for _, spec := range flagRegistry {
		if longNames[spec.name] {
			t.Errorf("duplicate long name: --%s", spec.name)
		}
		longNames[spec.name] = true
		if spec.short == 0 {
			t.Errorf("--%s has no short name", spec.name)
			continue
		}
		if previous, ok := shortNames[spec.short]; ok {
			t.Errorf("short name -%c is shared by --%s and --%s", spec.short, previous, spec.name)
		}
		shortNames[spec.short] = spec.name
		if !isASCIILetter(spec.short) {
			t.Errorf("-%c of --%s is not an ASCII letter", spec.short, spec.name)
		}
	}
}

// TestFlagMetadataIsComplete 確認型別、預設值、可選值與說明鍵都有意義。
func TestFlagMetadataIsComplete(t *testing.T) {
	catalog := i18n.MustDefault()
	for _, spec := range flagRegistry {
		if spec.descKey == "" {
			t.Errorf("--%s has no description key", spec.name)
			continue
		}
		for _, lang := range i18n.Supported {
			if got := catalog.T(lang, spec.descKey, nil); got == spec.descKey {
				t.Errorf("%s: description key %q is missing", lang, spec.descKey)
			}
		}
		if spec.typ == typeEnum {
			if len(spec.enum) == 0 {
				t.Errorf("--%s is an enum without allowed values", spec.name)
			}
			if spec.def != "" && !contains(spec.enum, spec.def) {
				t.Errorf("--%s default %q is not among its allowed values", spec.name, spec.def)
			}
		}
		if spec.typ != typeBool && spec.value == "" {
			t.Errorf("--%s takes a value but declares no placeholder", spec.name)
		}
		if spec.typ == typeBool && spec.repeat {
			t.Errorf("--%s is a boolean and cannot accumulate", spec.name)
		}
		for _, conflict := range spec.conflict {
			other := lookupLong(conflict)
			if other == nil {
				t.Errorf("--%s conflicts with unknown --%s", spec.name, conflict)
				continue
			}
			if !contains(other.conflict, spec.name) {
				t.Errorf("conflict between --%s and --%s is not declared both ways", spec.name, conflict)
			}
		}
	}
}

// TestCommandScopesAreValid 確認每個命令的參數引用都存在、位置參數範圍合理。
func TestCommandScopesAreValid(t *testing.T) {
	names := map[string]bool{}
	for _, command := range commandRegistry {
		if names[command.name] {
			t.Errorf("duplicate command name: %s", command.name)
		}
		names[command.name] = true
		if command.argMin < 0 || command.argMax < command.argMin {
			t.Errorf("%s: invalid argument range %d..%d", command.name, command.argMin, command.argMax)
		}
		if command.summaryKey == "" {
			t.Errorf("%s: missing summary key", command.name)
		}
	}
	for _, spec := range flagRegistry {
		for _, command := range spec.commands {
			if !names[command] {
				t.Errorf("--%s is scoped to unknown command %q", spec.name, command)
			}
		}
	}
}

// TestEveryCommandSeesGlobalFlags 確認全域選項對所有命令都可見。
func TestEveryCommandSeesGlobalFlags(t *testing.T) {
	for _, command := range commandRegistry {
		seen := map[string]bool{}
		for _, spec := range flagsForCommand(command.name) {
			seen[spec.name] = true
		}
		for _, spec := range globalFlags() {
			if !seen[spec.name] {
				t.Errorf("%s: global option --%s is missing", command.name, spec.name)
			}
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
