package cli

import (
	"strconv"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/i18n"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
)

// helpRenderer 依目前語言產生說明；說明內容全部來自集中宣告，
// 因此參數名、類型、預設值與互斥關係不會與解析行為脫節。
type helpRenderer struct {
	cat  *i18n.Catalog
	lang i18n.Lang
}

func (h *helpRenderer) t(key string, args map[string]string) string {
	return h.cat.T(h.lang, key, args)
}

// programHelp 產生程式層級說明。
func (h *helpRenderer) programHelp() string {
	var b strings.Builder
	b.WriteString(h.t("help.title", nil) + "\n\n")
	b.WriteString(h.t("help.usage", nil) + ":\n")
	b.WriteString("  " + h.t("help.usageGlobal", nil) + "\n")
	for _, command := range commandRegistry {
		b.WriteString("  " + h.usageLine(&command) + "\n")
	}
	b.WriteString("\n" + h.t("help.commands", nil) + ":\n")
	for _, command := range commandRegistry {
		label := command.name
		if command.argSpec != "" {
			label += " " + command.argSpec
		}
		b.WriteString("  " + padRight(label, 26) + h.t(command.summaryKey, nil) + "\n")
		if !command.implemented {
			b.WriteString("  " + padRight("", 26) + h.t("help.status.notImplemented", nil) + "\n")
		}
	}
	b.WriteString("\n" + h.t("help.globalOptions", nil) + ":\n")
	h.renderFlags(&b, globalFlags())
	b.WriteString("\n" + h.exitCodes() + "\n")
	b.WriteString("\n" + h.t("help.notImplementedNote", nil) + "\n")
	b.WriteString(h.t("help.more", nil))
	return b.String()
}

// commandHelp 產生單一子命令的說明。
func (h *helpRenderer) commandHelp(command *commandSpec) string {
	var b strings.Builder
	b.WriteString(h.t(command.summaryKey, nil) + "\n\n")
	b.WriteString(h.t("help.usage", nil) + ":\n")
	b.WriteString("  " + h.usageLine(command) + "\n")
	if command.argSpec != "" {
		b.WriteString("\n" + h.t("help.arguments", nil) + ":\n")
		b.WriteString("  " + command.argSpec + "\n")
	}
	b.WriteString("\n" + h.t("help.globalOptions", nil) + ":\n")
	h.renderFlags(&b, globalFlags())
	if specific := commandOnlyFlags(command.name); len(specific) > 0 {
		b.WriteString("\n" + h.t("help.commandOptions", map[string]string{"command": command.name}) + ":\n")
		h.renderFlags(&b, specific)
	}
	b.WriteString("\n" + h.exitCodes() + "\n")
	if !command.implemented {
		b.WriteString("\n" + h.t("help.notImplementedNote", nil) + "\n")
	}
	b.WriteString(h.t("help.more", nil))
	return b.String()
}

// usageLine 產生單一命令的用法行。
func (h *helpRenderer) usageLine(command *commandSpec) string {
	parts := []string{"evernightindex", command.name}
	if command.argSpec != "" {
		parts = append(parts, command.argSpec)
	}
	parts = append(parts, "[options]")
	return strings.Join(parts, " ")
}

// renderFlags 輸出選項的型別、預設值、可選值、重複規則、互斥關係與適用命令。
func (h *helpRenderer) renderFlags(b *strings.Builder, specs []flagSpec) {
	for _, spec := range specs {
		header := "  -" + string(spec.short) + ", --" + spec.name
		if spec.value != "" {
			header += " " + spec.value
		}
		b.WriteString(header + "\n")
		meta := "      " + h.t("help.label.type", nil) + ": " + spec.typ.String() +
			", " + h.t("help.label.default", nil) + ": " + h.defaultValue(spec)
		if len(spec.enum) > 0 {
			meta += ", " + h.t("help.label.allowed", nil) + ": " + strings.Join(spec.enum, ", ")
		}
		b.WriteString(meta + "\n")
		b.WriteString("      " + h.t(spec.descKey, nil) + "\n")
		var notes []string
		if spec.repeat {
			notes = append(notes, h.t("help.label.repeat", nil)+": "+h.t("help.value.accumulate", nil))
		} else {
			notes = append(notes, h.t("help.label.repeat", nil)+": "+h.t("help.value.once", nil))
		}
		notes = append(notes, h.t("help.label.conflicts", nil)+": "+h.conflicts(spec))
		if len(spec.commands) > 0 {
			notes = append(notes, h.t("help.label.commands", nil)+": "+strings.Join(spec.commands, ", "))
		}
		b.WriteString("      " + strings.Join(notes, "; ") + "\n")
	}
}

// defaultValue 顯示預設值；布林以 true/false 表示，空字串顯示為「無」。
func (h *helpRenderer) defaultValue(spec flagSpec) string {
	if spec.def == "" {
		return h.t("help.label.none", nil)
	}
	if spec.typ == typeBool {
		if value, ok := parseBool(spec.def); ok {
			return strconv.FormatBool(value)
		}
	}
	return spec.def
}

// conflicts 顯示互斥選項的長名稱。
func (h *helpRenderer) conflicts(spec flagSpec) string {
	if len(spec.conflict) == 0 {
		return h.t("help.label.none", nil)
	}
	names := make([]string, 0, len(spec.conflict))
	for _, name := range spec.conflict {
		names = append(names, "--"+name)
	}
	return strings.Join(names, ", ")
}

// exitCodes 輸出穩定的退出碼說明。
func (h *helpRenderer) exitCodes() string {
	var b strings.Builder
	b.WriteString(h.t("help.exitCodes", nil) + ":\n")
	for _, code := range []model.ExitCode{model.ExitOK, model.ExitFatal, model.ExitUsage, model.ExitPartial, model.ExitInterrupted} {
		b.WriteString("  " + h.t("help.exit."+strconv.Itoa(int(code)), nil) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// printVersion 輸出程式版本、建置資訊、授權條款與署名。
//
// 標籤固定使用英語機器欄位，不隨介面語言翻譯，方便腳本解析。
func (h *helpRenderer) printVersion(console *output.Console, build BuildInfo) {
	console.Result("EvernightIndex %s", build.Version)
	console.Result("commit: %s", build.Commit)
	console.Result("built: %s", build.BuildDate)
	console.Result("go: %s %s/%s", build.GoVersion, build.GOOS, build.GOARCH)
	console.Result("license: MulanPSL-2.0")
	console.Result("author: KagurazakaMiyabi(KagurazakaYashi)")
}

// padRight 以空白補齊欄位寬度；只處理 ASCII 命令名稱。
func padRight(text string, width int) string {
	if len(text) >= width {
		return text + " "
	}
	return text + strings.Repeat(" ", width-len(text))
}
