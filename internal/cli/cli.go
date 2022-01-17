package cli

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/kagurazakayashi/EvernightIndex/internal/app"
	"github.com/kagurazakayashi/EvernightIndex/internal/i18n"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
	"github.com/kagurazakayashi/EvernightIndex/internal/output"
	"github.com/kagurazakayashi/EvernightIndex/internal/platform"
)

// BuildInfo 是建置時注入的版本資訊。
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
	GoVersion string
	GOOS      string
	GOARCH    string
}

// languageEnvKey 是語言覆寫的環境變數。
const languageEnvKey = "EVERNIGHTINDEX_LANG"

// systemLanguage 取得系統語言；宣告成變數以便測試替換。
var systemLanguage = platform.SystemLanguage

// Main 是 CLI 的唯一入口：語言、解析、說明、分派與退出碼都在此完成。
//
// 它不呼叫 os.Exit、不寫入全域狀態，因此測試可以直接呼叫並檢查輸出與退出碼。
// 語言在說明與參數錯誤之前決定，確保錯誤訊息本身已在地化。
func Main(ctx context.Context, build BuildInfo, argv []string, stdout, stderr io.Writer, getenv func(string) string) int {
	catalog, err := i18n.Default()
	if err != nil {
		// 內嵌資源損壞屬於建置缺陷，此時沒有可用的在地化文字。
		_, _ = io.WriteString(stderr, "evernightindex: E_INTERNAL: "+err.Error()+"\n")
		return int(model.ExitFatal)
	}
	console := output.New(stdout, stderr)

	lang, langErr := resolveLanguage(argv, getenv)
	if langErr != nil {
		printError(catalog, resolveFallbackLanguage(getenv), console, langErr, true)
		return int(model.ExitUsage)
	}

	opts, parseErr := parse(argv)
	if parseErr != nil {
		printError(catalog, lang, console, parseErr, true)
		return int(model.ExitCodeOf(parseErr))
	}
	console.Configure(opts.boolValue("quiet"), opts.boolValue("verbose"), opts.boolValue("progress"))

	renderer := &helpRenderer{cat: catalog, lang: lang}
	switch {
	case opts.boolValue("version"):
		renderer.printVersion(console, build)
		return int(model.ExitOK)
	case opts.boolValue("help"):
		if opts.command != nil {
			console.Result("%s", renderer.commandHelp(opts.command))
			return int(model.ExitOK)
		}
		console.Result("%s", renderer.programHelp())
		return int(model.ExitOK)
	}

	service := app.New()
	if err := service.Run(ctx, app.Request{Command: opts.command.name, Args: opts.args}); err != nil {
		printError(catalog, lang, console, err, false)
		return int(model.ExitCodeOf(err))
	}
	return int(model.ExitOK)
}

// printError 以目前語言輸出錯誤碼與說明。
//
// 錯誤碼固定為英語機器標識，說明才隨語言改變；用法類錯誤額外提示如何取得說明。
func printError(catalog *i18n.Catalog, lang i18n.Lang, console *output.Console, err error, withHint bool) {
	code := model.CodeInternal
	text := err.Error()
	var appErr *model.Error
	if errors.As(err, &appErr) {
		code = appErr.Code
		text = catalog.T(lang, appErr.Key, appErr.Args)
		if console.VerboseEnabled() && appErr.Err != nil {
			text += " (" + appErr.Err.Error() + ")"
		}
	}
	console.Error("evernightindex: %s: %s", code, text)
	if withHint {
		console.Error("%s", catalog.T(lang, "hint.help", nil))
	}
}

// resolveLanguage 依序採用 --lang、EVERNIGHTINDEX_LANG、系統語言與英語。
//
// 明確指定但不合法的語言會回報錯誤，不會靜默改成其他語言；
// auto 表示繼續往下判斷。
func resolveLanguage(argv []string, getenv func(string) string) (i18n.Lang, error) {
	if raw, found := prescanLanguage(argv); found {
		lang, ok := i18n.Parse(raw)
		if !ok {
			return resolveFallbackLanguage(getenv), model.New(model.CodeInvalidLanguage, "err.invalidLanguage", model.ExitUsage, map[string]string{
				"value":   raw,
				"source":  "--lang",
				"allowed": i18n.Allowed(),
			})
		}
		if lang != i18n.LangAuto {
			return lang, nil
		}
	}
	if raw := strings.TrimSpace(getenv(languageEnvKey)); raw != "" {
		lang, ok := i18n.Parse(raw)
		if !ok {
			return resolveFallbackLanguage(getenv), model.New(model.CodeInvalidLanguage, "err.invalidLanguage", model.ExitUsage, map[string]string{
				"value":   raw,
				"source":  languageEnvKey,
				"allowed": i18n.Allowed(),
			})
		}
		if lang != i18n.LangAuto {
			return lang, nil
		}
	}
	return resolveFallbackLanguage(getenv), nil
}

// resolveFallbackLanguage 在沒有合法顯式語言時，依環境變數、系統語言、英語的順序決定。
func resolveFallbackLanguage(getenv func(string) string) i18n.Lang {
	if tag := platform.FromEnv(getenv); tag != "" {
		if lang, ok := i18n.Parse(tag); ok && lang != i18n.LangAuto {
			return lang
		}
	}
	if tag := platform.NormalizeLocale(systemLanguage()); tag != "" {
		if lang, ok := i18n.Parse(tag); ok && lang != i18n.LangAuto {
			return lang
		}
	}
	return i18n.Fallback
}

// prescanLanguage 在完整解析之前找出 --lang/-l 的值，最後一次指定優先。
//
// 這裡刻意寬容：格式錯誤留給完整解析回報，只取能辨識的寫法。
func prescanLanguage(argv []string) (string, bool) {
	value, found := "", false
	for index := 0; index < len(argv); index++ {
		switch token := argv[index]; {
		case token == "--":
			return value, found
		case token == "--lang", token == "-l":
			if index+1 < len(argv) {
				value, found = argv[index+1], true
				index++
			}
		case strings.HasPrefix(token, "--lang="):
			value, found = strings.TrimPrefix(token, "--lang="), true
		case strings.HasPrefix(token, "-l="):
			value, found = strings.TrimPrefix(token, "-l="), true
		}
	}
	return value, found
}
