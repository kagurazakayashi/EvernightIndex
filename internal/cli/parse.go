package cli

import (
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kagurazakayashi/EvernightIndex/internal/i18n"
	"github.com/kagurazakayashi/EvernightIndex/internal/model"
)

// options 是一次命令列解析的結果，保留每個選項的原始字串值。
//
// 同一標量選項重複指定時取最後一次；可重複列表選項依出現順序累積。
// 長短名指向同一筆記錄，因此 --db 與 -d 視為同一選項。
type options struct {
	command *commandSpec
	args    []string
	values  map[string][]string
	seen    map[string]bool
}

// MaxWorkers 回傳內容讀取並行數的上限：max(1, floor(邏輯 CPU 數/2))。
func MaxWorkers() int {
	ncpu := runtime.NumCPU()
	if ncpu < 2 {
		return 1
	}
	return ncpu / 2
}

func (o *options) record(name, value string) {
	o.values[name] = append(o.values[name], value)
	o.seen[name] = true
}

// isSet 表示使用者是否明確指定過此選項。
func (o *options) isSet(name string) bool { return o.seen[name] }

// str 回傳標量選項值：未指定時使用集中宣告的預設值。
func (o *options) str(name string) string {
	if values := o.values[name]; len(values) > 0 {
		return values[len(values)-1]
	}
	if spec := lookupLong(name); spec != nil {
		return spec.def
	}
	return ""
}

// list 回傳可重複選項的全部值，依出現順序。
func (o *options) list(name string) []string {
	values := o.values[name]
	if len(values) == 0 {
		return nil
	}
	result := make([]string, len(values))
	copy(result, values)
	return result
}

// boolValue 回傳布林選項值。
func (o *options) boolValue(name string) bool {
	value, _ := strconv.ParseBool(o.str(name))
	return value
}

// intValue 回傳整數選項值。
func (o *options) intValue(name string) int {
	value, _ := strconv.Atoi(o.str(name))
	return value
}

// durationValue 回傳時長選項值。
func (o *options) durationValue(name string) time.Duration {
	value, _ := time.ParseDuration(o.str(name))
	return value
}

// parse 解析完整命令列：長短選項、位置參數、-- 終止、型別與互斥檢查。
func parse(argv []string) (*options, error) {
	opts := &options{values: map[string][]string{}, seen: map[string]bool{}}
	terminated := false
	for index := 0; index < len(argv); index++ {
		token := argv[index]
		if !terminated && token == "--" {
			terminated = true
			continue
		}
		if !terminated && len(token) > 1 && token[0] == '-' {
			consumed, err := opts.parseFlag(argv, index)
			if err != nil {
				return nil, err
			}
			index += consumed
			continue
		}
		if opts.command == nil {
			command := lookupCommand(token)
			if command == nil {
				return nil, model.New(model.CodeUnknownCommand, "err.unknownCommand", model.ExitUsage, map[string]string{"command": token})
			}
			opts.command = command
			continue
		}
		opts.args = append(opts.args, token)
	}
	if opts.command == nil {
		if opts.boolValue("help") || opts.boolValue("version") {
			return opts, nil
		}
		return nil, model.New(model.CodeMissingCommand, "err.missingCommand", model.ExitUsage, nil)
	}
	// 只要使用者要說明或版本，就不檢查位置參數與互斥關係：
	// 說明本身必須在缺少參數時也能取得。
	if opts.boolValue("help") || opts.boolValue("version") {
		return opts, nil
	}
	if len(opts.args) < opts.command.argMin {
		return nil, model.New(model.CodeMissingArgument, "err.missingArgument", model.ExitUsage, map[string]string{
			"command":  opts.command.name,
			"argument": opts.command.argSpec,
		})
	}
	if len(opts.args) > opts.command.argMax {
		return nil, model.New(model.CodeUnexpectedArgument, "err.unexpectedArg", model.ExitUsage, map[string]string{
			"arg": opts.args[opts.command.argMax],
		})
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	return opts, nil
}

// parseFlag 解析單一選項，回傳額外消耗的 token 數。
func (o *options) parseFlag(argv []string, index int) (int, error) {
	if strings.HasPrefix(argv[index], "--") {
		return o.parseLong(argv, index)
	}
	return o.parseShort(argv, index)
}

func (o *options) parseLong(argv []string, index int) (int, error) {
	body := argv[index][2:]
	name, inlineValue, hasInline := cutInline(body)
	if name == "" {
		return 0, unknownFlag("--")
	}
	spec := lookupLong(name)
	if spec == nil {
		return 0, unknownFlag("--" + name)
	}
	if err := o.checkScope(spec, "--"+name); err != nil {
		return 0, err
	}
	return o.assign(argv, index, spec, "--"+name, inlineValue, hasInline)
}

func (o *options) parseShort(argv []string, index int) (int, error) {
	body := argv[index][1:]
	short := rune(body[0])
	rest := body[1:]
	if rest != "" && rest[0] != '=' {
		return 0, model.New(model.CodeInvalidValue, "err.shortFlagForm", model.ExitUsage, map[string]string{"flag": argv[index]})
	}
	spec := lookupShort(short)
	if spec == nil {
		return 0, unknownFlag("-" + string(short))
	}
	if err := o.checkScope(spec, "-"+string(short)); err != nil {
		return 0, err
	}
	inlineValue, hasInline := "", false
	if rest != "" {
		inlineValue, hasInline = rest[1:], true
	}
	return o.assign(argv, index, spec, "-"+string(short), inlineValue, hasInline)
}

// checkScope 檢查選項是否適用於目前的命令位置。
func (o *options) checkScope(spec *flagSpec, label string) error {
	if len(spec.commands) == 0 {
		return nil
	}
	if o.command == nil {
		return model.New(model.CodeFlagNotApplicable, "err.flagBeforeCommand", model.ExitUsage, map[string]string{"flag": label})
	}
	if !spec.appliesTo(o.command.name) {
		return model.New(model.CodeFlagNotApplicable, "err.flagNotApplicable", model.ExitUsage, map[string]string{
			"flag":    label,
			"command": o.command.name,
		})
	}
	return nil
}

// assign 依型別取得並驗證值，然後記錄。
func (o *options) assign(argv []string, index int, spec *flagSpec, label, inlineValue string, hasInline bool) (int, error) {
	if spec.typ == typeBool {
		if !hasInline {
			o.record(spec.name, "true")
			return 0, nil
		}
		value, ok := parseBool(inlineValue)
		if !ok {
			return 0, model.New(model.CodeInvalidValue, "err.invalidBool", model.ExitUsage, map[string]string{
				"flag":  label,
				"value": inlineValue,
			})
		}
		o.record(spec.name, strconv.FormatBool(value))
		return 0, nil
	}

	consumed := 0
	value := inlineValue
	if !hasInline {
		if index+1 >= len(argv) {
			return 0, needsValue(label)
		}
		next := argv[index+1]
		if strings.HasPrefix(next, "-") && next != "-" {
			return 0, needsValue(label)
		}
		value = next
		consumed = 1
	}
	if value == "" {
		return 0, needsValue(label)
	}
	if err := checkValue(spec, label, value); err != nil {
		return 0, err
	}
	o.record(spec.name, value)
	return consumed, nil
}

// checkValue 依型別驗證值；值域上限與互斥關係留給 validate，因為它們跨選項。
func checkValue(spec *flagSpec, label, value string) error {
	switch spec.typ {
	case typeEnum:
		if spec.name == "lang" {
			if _, ok := i18n.Parse(value); !ok {
				return model.New(model.CodeInvalidLanguage, "err.invalidLanguage", model.ExitUsage, map[string]string{
					"value":   value,
					"source":  label,
					"allowed": i18n.Allowed(),
				})
			}
			return nil
		}
		for _, allowed := range spec.enum {
			if value == allowed {
				return nil
			}
		}
		return model.New(model.CodeInvalidValue, "err.invalidEnum", model.ExitUsage, map[string]string{
			"flag":    label,
			"value":   value,
			"allowed": strings.Join(spec.enum, ", "),
		})
	case typeInt:
		number, err := strconv.Atoi(value)
		if err != nil || number < 0 {
			return model.New(model.CodeInvalidValue, "err.invalidNumber", model.ExitUsage, map[string]string{
				"flag":  label,
				"value": value,
			})
		}
	case typeDuration:
		duration, err := time.ParseDuration(value)
		if err != nil || duration < 0 {
			return model.New(model.CodeInvalidValue, "err.invalidDuration", model.ExitUsage, map[string]string{
				"flag":  label,
				"value": value,
			})
		}
	}
	return nil
}

// validate 檢查跨選項的互斥與值域。
//
// 全部檢查都在任何掃描、資料庫變更或檔案讀取之前完成，
// 讓昂貴工作與破壞性操作不會先發生再報錯。
func (o *options) validate() error {
	if o.boolValue("quiet") && o.boolValue("verbose") {
		return conflict("err.conflictQuietVerbose")
	}
	if o.command == nil {
		return nil
	}
	switch o.command.name {
	case "index":
		if o.boolValue("deep") && !o.boolValue("sha256") {
			return conflict("err.conflictDeepNoSha256")
		}
		if o.isSet("log-dir") && !o.boolValue("change-log") {
			return conflict("err.conflictLogDirNeedsChangeLog")
		}
	case "dupes":
		if o.str("verify") == "sha256" && o.str("match") != "sha256" {
			return conflict("err.conflictVerifyNeedsMatch")
		}
	}
	if o.boolValue("null") {
		if o.boolValue("details") {
			return conflict("err.conflictNullDetails")
		}
		switch format := o.str("format"); format {
		case "jsonl", "csv":
			return model.New(model.CodeFlagConflict, "err.conflictNullFormat", model.ExitUsage, map[string]string{"format": format})
		}
	}
	if o.isSet("workers") {
		max := MaxWorkers()
		if workers := o.intValue("workers"); workers > max {
			return model.New(model.CodeInvalidValue, "err.workersRange", model.ExitUsage, map[string]string{
				"max":   strconv.Itoa(max),
				"value": strconv.Itoa(workers),
			})
		}
	}
	return nil
}

// cutInline 拆分 --name=value 形式。
func cutInline(body string) (name, value string, hasValue bool) {
	if index := strings.IndexByte(body, '='); index >= 0 {
		return body[:index], body[index+1:], true
	}
	return body, "", false
}

// parseBool 只接受 true/false，避免把 "1" 之類的值當成布林。
func parseBool(value string) (bool, bool) {
	switch value {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

func unknownFlag(label string) error {
	return model.New(model.CodeUnknownFlag, "err.unknownFlag", model.ExitUsage, map[string]string{"flag": label})
}

func needsValue(label string) error {
	return model.New(model.CodeFlagNeedsValue, "err.flagNeedsValue", model.ExitUsage, map[string]string{"flag": label})
}

func conflict(key string) error {
	return model.New(model.CodeFlagConflict, key, model.ExitUsage, nil)
}
