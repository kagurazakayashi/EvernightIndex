# EvernightIndex

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja-JP.md)

EvernightIndex 是使用 Go 开发的 Windows、macOS、Linux 命令行工具，为一个目录树建立 SQLite 索引，并据此回答查询：属性与名称查找、变化日志，以及两个已索引目录之间的重复文件检测。它不会删除、移动用户文件，也不会自动建立硬链接。

本页是简体中文版，与英文主文档结构、版本保持同步。

## 当前状态

- 版本 `0.1.0-dev`（工程骨架）。命令契约、四语言消息资源与 SQLite 基础已就位。
- 全部业务命令（`index`、`find`、`dupes`、`info`、`export-log`、`recover`）只做参数解析与校验，随后以退出码 `1` 和 `E_NOT_IMPLEMENTED` 说明失败。`--help` 与 `--version` 不会建立数据库，也不会扫描目录。
- 扫描、索引创建与更新、SHA256 计算、变化日志 CSV、查询、重复检测、临时工作副本与恢复**尚未实现**。下列章节描述已经固定的契约，不代表这些功能可用。

## 环境要求

- 构建需要 Go `1.26.0` 或更新版本（SQLite 驱动的最低要求）。本步骤实际使用 `go1.26.5 windows/amd64` 构建与测试。
- 不需要 CGO 与 C 编译器：`CGO_ENABLED=0` 即可构建 Windows、macOS、Linux 版本。
- 运行期除数据库所在的可写位置外没有额外依赖；数据库默认路径是 `<根目录>/index.db`。

## 构建与测试

```sh
git clone https://github.com/kagurazakayashi/EvernightIndex.git
cd EvernightIndex
go build ./cmd/evernightindex
go test ./...
```

注入版本信息：

```sh
go build -ldflags "-X main.version=0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=2026-11-11" ./cmd/evernightindex
```

交叉编译（三个目标都使用 `CGO_ENABLED=0`）：

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/evernightindex
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build ./cmd/evernightindex
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build ./cmd/evernightindex
```

Windows PowerShell 中先设置 `$env:CGO_ENABLED="0"` 与 `$env:GOOS="windows"`，再执行同样的 `go build`。

`scripts/verify.sh` 一次跑完格式检查、vet、测试与四个目标的交叉编译（`JOBS`、`GOMAXPROCS`、`OUT` 分别限制并行度与产物位置）。

## 快速开始

```sh
evernightindex --version
evernightindex --help
evernightindex index --help
evernightindex --lang zh-CN --help
evernightindex index /data --help
```

`cmd` 中路径含空格时用双引号（`--db "C:\\我的 数据\\index.db"`）；PowerShell 中用单引号通常更省事。不带 `--help` 直接运行 `index /data` 目前会以退出码 `1` 失败，且不改动任何文件。

## 命令

| 命令 | 位置参数 | 状态 | 用途 |
| --- | --- | --- | --- |
| `index` | `<目录>` | 尚未实现 | 创建或更新一个目录的索引 |
| `find` | 无 | 尚未实现 | 按名称、路径或类型化筛选查询已索引条目 |
| `dupes` | `<目录A> <目录B>` | 尚未实现 | 比较两个已索引目录中的重复普通文件 |
| `info` | 无 | 尚未实现 | 显示绑定根目录、格式版本、运行状态与摘要覆盖率 |
| `export-log` | 无 | 尚未实现 | 导出某一次运行中待导出的变化日志事件 |
| `recover` | 无 | 尚未实现 | 重试发布已完成且校验通过的临时工作副本 |

## 参数

| 长参数 | 短参数 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `--help` | `-h` | bool | `false` | 全局 |
| `--version` | `-V` | bool | `false` | 全局 |
| `--lang` | `-l` | enum | `auto` | 全局; 可选值: auto / zh-CN / zh-TW / en-US / ja-JP |
| `--verbose` | `-v` | bool | `false` | 全局 |
| `--quiet` | `-q` | bool | `false` | 全局 |
| `--progress` | `-p` | bool | `true` | 全局 |
| `--db` | `-d` | string | （无） | index, find, info, export-log |
| `--log-dir` | `-L` | string | （无） | index, export-log |
| `--temp-dir` | `-t` | string | （无） | index, recover |
| `--sha256` | `-s` | bool | `true` | index; 互斥: --deep |
| `--deep` | `-D` | bool | `false` | index; 互斥: --sha256 |
| `--change-log` | `-c` | bool | `false` | index |
| `--exclude` | `-e` | string[] | （无） | index; 可重复 |
| `--one-file-system` | `-x` | bool | `false` | index |
| `--workers` | `-w` | int | `0` | index; 0..floor(CPU/2) |
| `--lock-timeout` | `-T` | duration | `30s` | index |
| `--read-retries` | `-R` | int | `1` | index |
| `--name` | `-n` | string | （无） | find |
| `--path` | `-P` | string | （无） | find |
| `--filter` | `-f` | string | （无） | find |
| `--case` | `-C` | enum | `auto` | find; 可选值: auto / sensitive / insensitive |
| `--details` | `-i` | bool | `false` | find, dupes; 互斥: --null |
| `--fields` | `-F` | string | （无） | find, dupes |
| `--sort` | `-S` | string | （无） | find, dupes |
| `--limit` | `-N` | int | `0` | find, dupes |
| `--offset` | `-O` | int | `0` | find, dupes |
| `--format` | `-o` | enum | `text` | find, dupes; 可选值: text / jsonl / csv |
| `--null` | `-z` | bool | `false` | find, dupes; 互斥: --details |
| `--db-a` | `-a` | string | （无） | dupes |
| `--db-b` | `-b` | string | （无） | dupes |
| `--match` | `-m` | enum | `auto` | dupes; 可选值: auto / metadata / sha256 |
| `--verify` | `-r` | enum | `none` | dupes; 可选值: none / metadata / sha256 |
| `--run` | `-u` | string | （无） | export-log, recover |

每个参数都恰好有一个长名和一个单字母短名，两种写法行为完全一致。支持 `--option=value`、`--option value`、`-o value` 与 `-o=value`。短选项不支持串联（`-vq` 会被拒绝），也不接受把值与短名粘在一起（`-dpath` 会被拒绝）。同一标量参数重复指定取最后一次；可重复列表参数按出现顺序累积。`--` 终止选项解析，因此路径可以以 `-` 开头。长参数不支持前缀缩写：`--work` 是未知参数，`--workers` 才是。非法组合在任何扫描或数据库变更之前就被拒绝：`--deep` 与 `--sha256=false`、`--quiet` 与 `--verbose`、`--null` 与 `--details`/`--format=jsonl`/`--format=csv`、`--verify=sha256` 未搭配 `--match=sha256`，以及 `index` 命令中 `--log-dir` 未搭配 `--change-log`。

## 语言

优先级：`--lang`/`-l` > `EVERNIGHTINDEX_LANG` > 系统语言 > 英语。

- Windows：用户界面语言（BCP47，例如 `zh-TW`）。
- macOS：`defaults read -g AppleLocale`。
- Linux 与其他 Unix：先 `LC_ALL`，再 `LC_MESSAGES`，最后 `LANG`。`C`/`POSIX` 视为「未指定」，回退英语。

| 标签 | 别名 |
| --- | --- |
| `zh-CN` | `zh`、`zh_CN`、`zh-Hans`、`zh-SG` |
| `zh-TW` | `zh_TW`、`zh-Hant`、`zh-Hant-TW`、`zh-HK`、`zh-MO` |
| `en-US` | `en`、`en_US`、`en-GB` |
| `ja-JP` | `ja`、`ja_JP` |
| `auto` | 依环境自动判断 |

显式指定但不合法的语言是错误（`E_INVALID_LANGUAGE`），不会被静默替换。机器字段——参数名、命令名、枚举值、错误码、CSV 标题、JSONL 键与数值——一律不翻译，只翻译人类可读文本。缺少的条目回退英语；测试会检查四套资源的缺项与占位符一致性。

## 退出码

| 退出码 | 含义 |
| --- | --- |
| `0` | 成功，包括没有结果 |
| `1` | 致命错误（含「尚未实现」） |
| `2` | 参数或格式不合法 |
| `3` | 部分失败或有日志待导出 |
| `130` | 用户中断 |

## 输出流

- 标准输出只放结果：查询行、重复分组或明确要求的结构化输出。
- 标准错误放进度、警告、错误、逐路径详细记录与最终统计。
- `--quiet` 关闭常规进度（错误仍显示），`--verbose` 增加逐路径记录，`--progress=false` 显式关闭进度。`--quiet` 与 `--verbose` 同时使用会被拒绝。

## 数据库基础

- 驱动：`modernc.org/sqlite v1.62.0`，纯 Go、无需 CGO。选型依据是实测事务、取消、一致性备份与跨平台构建，而不是「纯 Go」这个标签本身。
- 连接：每进程单个连接池，WAL 日志，`busy_timeout`、`foreign_keys=1`、`synchronous=NORMAL`。
- 识别与版本：`PRAGMA application_id = 0x45564E49`（"EVNI"），`PRAGMA user_version` 与 schema 版本（当前 `1`）一致，`store_meta` 表保存 `schema_version` 与 `db_uuid`。
- 事务：`store.WithTx` 成功则提交，否则一定回滚，调用方只会看到「已提交」或「未提交」两种状态；取消信号会传递给驱动。
- 一致性备份：`store.Backup` 使用 `VACUUM INTO` 写入目标目录的临时文件后再更名，不裸复制可能遗漏 WAL 内容的主文件，并拒绝覆盖已存在的目标。
- 拒绝处理：其他程序建立的数据库、较新的格式版本、目录路径或缺失的数据库都会得到稳定错误码（`E_DATABASE_FOREIGN`、`E_DATABASE_VERSION_TOO_NEW`、`E_DATABASE_MISSING`、`E_DATABASE_IO`）。

在 Windows amd64 上由 `go test ./internal/store` 实际执行验证：WAL 建库、提交、回滚可见性、排除未提交事务的一致性快照、备份完整性检查、`PRAGMA integrity_check`、长查询中途取消且之后连接可继续使用、只读打开拒绝写入、拒绝外来数据库与较新版本、拒绝把目录当数据库路径。

## 平台验证状态

| 平台 | 构建 | 运行 | 说明 |
| --- | --- | --- | --- |
| Windows 11 / amd64 | 通过 | 通过 | 本步骤实际构建、`go test ./...` 与命令运行 |
| macOS arm64 与 amd64 | 交叉编译通过 | 未验证 | 仅 `CGO_ENABLED=0` 交叉构建；`defaults` 语言识别未在实机验证 |
| Linux amd64 | 交叉编译通过 | 未验证 | 仅 `CGO_ENABLED=0` 交叉构建；未在 Linux 上运行测试 |

交叉编译通过不等于运行验证。平台相关元数据采集仍需逐平台实现并验证。

## 尚未实现

带排除规则与单文件系统限制的扫描；平台元数据采集；带读取稳定性检查的流式 SHA256；索引创建与快速/细致更新及保守删除判定；变化日志 CSV 导出与重试；类型化筛选、字段选择与 JSONL/CSV/NUL 输出；`auto`/`metadata`/`sha256` 重复检测与现场复核；临时工作副本的发布与恢复；每秒统计。

## 目录结构

```text
cmd/evernightindex      入口、版本注入、退出码交接
internal/cli            参数集中声明、解析、帮助、分派
internal/app            任务编排（当前拒绝执行未实现命令）
internal/model          稳定错误码与退出码
internal/store          SQLite 打开、识别、事务与备份基础
internal/platform       以构建标签隔离的系统事实（系统语言）
internal/output         标准输出/标准错误分流与详细度
internal/i18n           四语言消息资源、语言解析、资源检查
```

## 许可证与署名

- 许可证：木兰宽松许可证第 2 版（`MulanPSL-2.0`），完整文本见 [LICENSE](LICENSE)。
- 版权与指定署名：`Copyright (c) KagurazakaMiyabi(KagurazakaYashi)`，再分发时须保持这一写法。
- 第三方依赖保留各自的许可证：直接依赖 `modernc.org/sqlite` 为 BSD-3-Clause，`go list -m -all` 可列出全部模块，`go.mod` 锁定本工程使用的版本。
