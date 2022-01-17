package cli

// flagType 是選項值的型別，決定解析方式與說明中的型別名稱（機器值，不翻譯）。
type flagType int

const (
	typeBool flagType = iota
	typeString
	typeEnum
	typeInt
	typeDuration
	typeStringList
)

// String 回傳穩定的型別名稱。
func (t flagType) String() string {
	switch t {
	case typeBool:
		return "bool"
	case typeString:
		return "string"
	case typeEnum:
		return "enum"
	case typeInt:
		return "int"
	case typeDuration:
		return "duration"
	case typeStringList:
		return "string[]"
	default:
		return "unknown"
	}
}

// flagSpec 是一個公開選項的集中宣告。
//
// 長名稱、單字母短名稱、型別、預設值、可選值、重複規則與適用命令都只在此宣告，
// 解析、說明與測試都讀同一份資料，避免四語言文件與實作漂移。
type flagSpec struct {
	name     string
	short    rune
	typ      flagType
	def      string
	enum     []string
	repeat   bool
	value    string
	descKey  string
	commands []string
	conflict []string
}

// commandSpec 是一個子命令的集中宣告。
type commandSpec struct {
	name        string
	argSpec     string
	argMin      int
	argMax      int
	summaryKey  string
	implemented bool
}

// flagRegistry 是全部公開選項。commands 為空表示全域選項。
var flagRegistry = []flagSpec{
	{name: "help", short: 'h', typ: typeBool, def: "false", descKey: "flag.help.desc"},
	{name: "version", short: 'V', typ: typeBool, def: "false", descKey: "flag.version.desc"},
	{name: "lang", short: 'l', typ: typeEnum, def: "auto", enum: []string{"auto", "zh-CN", "zh-TW", "en-US", "ja-JP"}, value: "<tag>", descKey: "flag.lang.desc"},
	{name: "verbose", short: 'v', typ: typeBool, def: "false", descKey: "flag.verbose.desc", conflict: []string{"quiet"}},
	{name: "quiet", short: 'q', typ: typeBool, def: "false", descKey: "flag.quiet.desc", conflict: []string{"verbose"}},
	{name: "progress", short: 'p', typ: typeBool, def: "true", descKey: "flag.progress.desc"},

	{name: "db", short: 'd', typ: typeString, def: "", value: "<path>", descKey: "flag.db.desc", commands: []string{"index", "find", "info", "export-log"}},
	{name: "log-dir", short: 'L', typ: typeString, def: "", value: "<dir>", descKey: "flag.log-dir.desc", commands: []string{"index", "export-log"}},
	{name: "temp-dir", short: 't', typ: typeString, def: "", value: "<dir>", descKey: "flag.temp-dir.desc", commands: []string{"index", "recover"}},
	{name: "sha256", short: 's', typ: typeBool, def: "true", descKey: "flag.sha256.desc", commands: []string{"index"}, conflict: []string{"deep"}},
	{name: "deep", short: 'D', typ: typeBool, def: "false", descKey: "flag.deep.desc", commands: []string{"index"}, conflict: []string{"sha256"}},
	{name: "change-log", short: 'c', typ: typeBool, def: "false", descKey: "flag.change-log.desc", commands: []string{"index"}},
	{name: "exclude", short: 'e', typ: typeStringList, def: "", repeat: true, value: "<pattern>", descKey: "flag.exclude.desc", commands: []string{"index"}},
	{name: "one-file-system", short: 'x', typ: typeBool, def: "false", descKey: "flag.one-file-system.desc", commands: []string{"index"}},
	{name: "workers", short: 'w', typ: typeInt, def: "0", value: "<n>", descKey: "flag.workers.desc", commands: []string{"index"}},
	{name: "lock-timeout", short: 'T', typ: typeDuration, def: "30s", value: "<duration>", descKey: "flag.lock-timeout.desc", commands: []string{"index"}},
	{name: "read-retries", short: 'R', typ: typeInt, def: "1", value: "<n>", descKey: "flag.read-retries.desc", commands: []string{"index"}},

	{name: "name", short: 'n', typ: typeString, def: "", value: "<pattern>", descKey: "flag.name.desc", commands: []string{"find"}},
	{name: "path", short: 'P', typ: typeString, def: "", value: "<pattern>", descKey: "flag.path.desc", commands: []string{"find"}},
	{name: "filter", short: 'f', typ: typeString, def: "", value: "<expr>", descKey: "flag.filter.desc", commands: []string{"find"}},
	{name: "case", short: 'C', typ: typeEnum, def: "auto", enum: []string{"auto", "sensitive", "insensitive"}, value: "<policy>", descKey: "flag.case.desc", commands: []string{"find"}},
	{name: "details", short: 'i', typ: typeBool, def: "false", descKey: "flag.details.desc", commands: []string{"find", "dupes"}, conflict: []string{"null"}},
	{name: "fields", short: 'F', typ: typeString, def: "", value: "<list>", descKey: "flag.fields.desc", commands: []string{"find", "dupes"}},
	{name: "sort", short: 'S', typ: typeString, def: "", value: "<field[:asc|desc]>", descKey: "flag.sort.desc", commands: []string{"find", "dupes"}},
	{name: "limit", short: 'N', typ: typeInt, def: "0", value: "<n>", descKey: "flag.limit.desc", commands: []string{"find", "dupes"}},
	{name: "offset", short: 'O', typ: typeInt, def: "0", value: "<n>", descKey: "flag.offset.desc", commands: []string{"find", "dupes"}},
	{name: "format", short: 'o', typ: typeEnum, def: "text", enum: []string{"text", "jsonl", "csv"}, value: "<format>", descKey: "flag.format.desc", commands: []string{"find", "dupes"}},
	{name: "null", short: 'z', typ: typeBool, def: "false", descKey: "flag.null.desc", commands: []string{"find", "dupes"}, conflict: []string{"details"}},

	{name: "db-a", short: 'a', typ: typeString, def: "", value: "<path>", descKey: "flag.db-a.desc", commands: []string{"dupes"}},
	{name: "db-b", short: 'b', typ: typeString, def: "", value: "<path>", descKey: "flag.db-b.desc", commands: []string{"dupes"}},
	{name: "match", short: 'm', typ: typeEnum, def: "auto", enum: []string{"auto", "metadata", "sha256"}, value: "<basis>", descKey: "flag.match.desc", commands: []string{"dupes"}},
	{name: "verify", short: 'r', typ: typeEnum, def: "none", enum: []string{"none", "metadata", "sha256"}, value: "<basis>", descKey: "flag.verify.desc", commands: []string{"dupes"}},

	{name: "run", short: 'u', typ: typeString, def: "", value: "<run-id>", descKey: "flag.run.desc", commands: []string{"export-log", "recover"}},
}

// commandRegistry 是全部子命令。本步驟所有業務命令都尚未實作，
// 只提供解析、預設值、互斥檢查與說明，執行時明確回報尚未實作。
var commandRegistry = []commandSpec{
	{name: "index", argSpec: "<dir>", argMin: 1, argMax: 1, summaryKey: "cmd.index.summary", implemented: false},
	{name: "find", argSpec: "", argMin: 0, argMax: 0, summaryKey: "cmd.find.summary", implemented: false},
	{name: "dupes", argSpec: "<dir-a> <dir-b>", argMin: 2, argMax: 2, summaryKey: "cmd.dupes.summary", implemented: false},
	{name: "info", argSpec: "", argMin: 0, argMax: 0, summaryKey: "cmd.info.summary", implemented: false},
	{name: "export-log", argSpec: "", argMin: 0, argMax: 0, summaryKey: "cmd.export-log.summary", implemented: false},
	{name: "recover", argSpec: "", argMin: 0, argMax: 0, summaryKey: "cmd.recover.summary", implemented: false},
}

// lookupCommand 依名稱尋找子命令（長名稱、區分大小寫、不縮寫）。
func lookupCommand(name string) *commandSpec {
	for i := range commandRegistry {
		if commandRegistry[i].name == name {
			return &commandRegistry[i]
		}
	}
	return nil
}

// lookupLong 依完整長名稱尋找選項；不支援前綴縮寫。
func lookupLong(name string) *flagSpec {
	for i := range flagRegistry {
		if flagRegistry[i].name == name {
			return &flagRegistry[i]
		}
	}
	return nil
}

// lookupShort 依單字母短名稱尋找選項；短名稱區分大小寫。
func lookupShort(short rune) *flagSpec {
	for i := range flagRegistry {
		if flagRegistry[i].short == short {
			return &flagRegistry[i]
		}
	}
	return nil
}

// appliesTo 判斷選項是否適用於指定命令；全域選項一律適用。
func (f *flagSpec) appliesTo(command string) bool {
	if len(f.commands) == 0 {
		return true
	}
	for _, name := range f.commands {
		if name == command {
			return true
		}
	}
	return false
}

// flagsForCommand 回傳該命令的說明順序：先全域選項，再命令專屬選項。
func flagsForCommand(command string) []flagSpec {
	var specs []flagSpec
	for _, spec := range flagRegistry {
		if len(spec.commands) == 0 {
			specs = append(specs, spec)
		}
	}
	for _, spec := range flagRegistry {
		if len(spec.commands) > 0 && spec.appliesTo(command) {
			specs = append(specs, spec)
		}
	}
	return specs
}

// globalFlags 回傳全域選項。
func globalFlags() []flagSpec {
	specs := make([]flagSpec, 0, len(flagRegistry))
	for _, spec := range flagRegistry {
		if len(spec.commands) == 0 {
			specs = append(specs, spec)
		}
	}
	return specs
}

// commandOnlyFlags 回傳命令專屬選項。
func commandOnlyFlags(command string) []flagSpec {
	var specs []flagSpec
	for _, spec := range flagRegistry {
		if len(spec.commands) > 0 && spec.appliesTo(command) {
			specs = append(specs, spec)
		}
	}
	return specs
}
