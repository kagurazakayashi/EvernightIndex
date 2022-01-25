# EvernightIndex

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja-JP.md)

EvernightIndex 是使用 Go 开发的 Windows、macOS、Linux 命令行工具，为一个目录树建立 SQLite 索引，并据此回答查询：属性与名称查找、变化日志，以及两个已索引目录之间的重复文件检测。它不会删除、移动用户文件，也不会自动建立硬链接。

本页是简体中文版，与英文主文档结构、版本保持同步。

## 当前状态

- 版本 `0.1.0-dev`（工程骨架、存储与运行控制层、扫描器与串流 SHA256）。集中参数声明、四语言资源、SQLite 基础、schema 版本 2、暂存与发布代次、跨进程写锁、运行生命周期、串流摘要模块与每秒统计都已就位。
- `info` 已实现为只读命令：报告格式版本、数据库 UUID、绑定根目录、已发布代次、条目与摘要覆盖率、最近一次运行以及待导出日志。它不会建立、迁移或锁定数据库。
- 遍历、元数据与模式匹配模块（`internal/scan`、`internal/fsmeta`、`internal/match`）与摘要模块（`internal/hash`）已实现，并在 Windows 上以真实测试验证；`index` 现在把它们串成完整流程。具体行为见下方「扫描与元数据采集」。
- `index` 创建并更新同一个索引：解析根目录与 `--db`、核对既有格式版本与绑定根目录、读取上次发布状态、遍历目录、把条目流式写入暂存区、逐文件决定计算/复用/保留/清除摘要、分类每次变化，最后在单个事务内发布。只有遍历真正覆盖到的区域才会推断删除。变化日志 CSV、查询、重复检测、临时工作副本与恢复仍然**尚未实现**。

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
evernightindex index /data
evernightindex info --db /data/index.db
```

`cmd` 中路径含空格时用双引号（`--db "C:\\我的 数据\\index.db"`）；PowerShell 中用单引号通常更省事。`info` 以只读方式打开既有数据库：缺少 `--db` 时以退出码 `2` 失败，数据库缺失、非本程序建立、损坏、版本较新或索引不完整时以稳定错误码拒绝，不会修改任何文件。`index /data` 会建立 `<根目录>/index.db`（含 SHA256）并以 `0` 结束；再次执行会重新遍历同一棵树、复用未变化文件的摘要，并发布下一个代次。

## 命令

| 命令 | 位置参数 | 状态 | 用途 |
| --- | --- | --- | --- |
| `index` | `<目录>` | 已实现（创建与更新） | 创建或更新一个目录的索引 |
| `find` | 无 | 尚未实现 | 按名称、路径或类型化筛选查询已索引条目 |
| `dupes` | `<目录A> <目录B>` | 尚未实现 | 比较两个已索引目录中的重复普通文件 |
| `info` | 无 | 已实现（只读） | 显示绑定根目录、格式版本、运行状态与摘要覆盖率 |
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

- 标准输出只放结果：查询行、重复分组、`info` 报告或明确要求的结构化输出。
- 标准错误放进度、警告、错误、逐路径详细记录与统计。
- 运行期间每秒向标准错误输出一次统计；不足一秒的运行也会输出最终汇总，因此已处理、失败、错误事件与已发布数量始终可见。统计把已发现、已处理、成功、失败、错误事件与已发布分开：已处理不代表已保存，取消或回滚后已发布数量保持 0。
- `--quiet` 关闭常规进度**与**最终汇总（错误永不屏蔽），`--verbose` 增加逐路径记录，`--progress=false` 只关闭每秒节拍并保留最终汇总。`--quiet` 与 `--verbose` 同时使用会被拒绝。

## 创建与更新索引

```sh
evernightindex index /data                 # 创建 /data/index.db，含 SHA256
evernightindex index /data --sha256=false  # 只记录元数据，绝不打开文件内容
evernightindex index /data --db /backup/index.db --deep
```

- 同一个命令既创建也更新。未指定 `--db` 时目标是 `<根目录>/index.db`，显式相对 `--db` 按调用时工作目录解析。数据库不存在时创建；存在时先以只读方式核对格式版本、完整性、绑定根目录的路径与卷身份，根目录不同就报 `E_ROOT_MISMATCH` 拒绝，绝不自动改绑。被中断的首次创建留下的数据库没有任何已发布结果，因此下一次执行建立第一个已发布代次，而不是更新既有索引。
- 每次执行都重新遍历目录并采集可取得的全部元数据；目录修改时间没有变化也不会跳过其子树。默认快速模式比较条目类型、逻辑大小、修改时间、文件身份与元数据变更时间，身份或变更时间变化会保守触发重算。变更时间本身不能证明内容变化，因此仅修改权限也会多读一次内容。
- 摘要决策表：`--sha256=true` 且快速模式时，新增文件、快速判据变化或没有可用旧摘要的文件会读取内容并保存摘要，其余文件保留旧摘要并记为 `reused`，运行报告会说明有多少文件没有读取内容。`--sha256=false` 完全不打开文件内容：新增文件没有摘要，判据变化的文件清除旧摘要，其余保留旧摘要但不称为本次已验证。`--sha256=true --deep` 会重新读取并保存每个可读取普通文件的摘要。`--sha256=false --deep` 在扫描与写库之前以退出码 `2` 拒绝。
- 摘要有效性：重算失败、文件读不到或读取期间持续变化时不保留任何摘要（`failed` 加 `error_code` 与 `error_stage`），旧摘要绝不会被当成当前摘要。修改时间变了但摘要相同属于元数据变化，不是内容变化；补充基线与重算得到相同摘要都不会被报告成内容变化。
- 保守删除：只有遍历完整结束、且路径不在任何枚举失败、部分枚举或排除区域之内时才会移除。枚举失败或部分枚举目录下的条目保留并标为 `unverified`；被排除模式或本程序自产文件排除的目录保留并标为 `excluded`，同时产生 `excluded` 事件，绝不当作删除；`--one-file-system` 跳过的挂载点子树保持 `unverified`。这类运行发布部分完成结果并以退出码 `3` 结束，成功区域仍然更新。
- 类型变化记为 `type_changed`；目录变成文件时，它原有的子项会因为覆盖证据充分而被移除。本版不推断重命名或移动：旧路径记为移除、新路径记为新增，绝不只凭相同摘要断言移动。
- 变化事件带 before/after 快照，与索引在同一个事务内提交。只有开启变化日志时才长期保存；关闭时仍然报告新增、更新、移除、因排除移出、未验证与未变化的计数，但不保留长期历史。
- 快速模式无法证明内容没有变化：如果类型、大小、修改时间、文件身份与变更时间全都没有变化，内容修改不会被发现。需要逐文件重新读取时请使用 `--deep`。
- 已在 Windows 11 amd64 以自动化测试（`go test ./internal/index ./internal/store ./internal/app ./internal/cli`）与合成目录的真实命令行运行验证：创建后新增、移除、类型替换与排除规则变化，判据未变时复用摘要且不读内容，细致模式全量重算，`--sha256=false` 清除旧摘要，重算失败不留摘要，覆盖缺口把旧记录保留为 `unverified`，以及被拒绝或失败的执行不改变上一代次与原有条目。
- 所有选项在任何扫描与写入之前检查。`--deep` 与 `--sha256=false` 同时使用时以退出码 `2` 拒绝；`--change-log` 与 `--temp-dir` 以 `E_NOT_IMPLEMENTED` 明确拒绝而不是默默忽略，因为变化日志与临时工作副本是后续步骤。
- 先解析路径并校验根目录，再创建数据库，因此不可用的根目录（`E_ROOT_UNAVAILABLE`）不会产出空索引。
- 摘要状态：普通文件在 `--sha256=true` 时以串流方式计算 SHA256（`computed`）；`--sha256=false` 记为 `disabled`，完全不打开文件；目录、符号链接与特殊文件记为 `not_applicable`，永远没有摘要。空文件会得到标准空输入 SHA256。内容读不到，或读取期间持续变化时，记为 `failed` 并写入 `error_code` 与 `error_stage`，摘要一律为空，同时出现在覆盖缺口里。更新时未被读取的保留文件沿用旧摘要并记为 `reused`，明确表示本次没有重新验证内容。
- 每次读取只打开文件一次，读前读后都检查同一个控制句柄（身份、大小、修改时间），并与扫描时记录的值比对；不一致时按 `--read-retries`（默认 1）重试，仍不一致就记为失败。这能发现「扫描后路径被替换」与「读取期间被改写」；不能排除所有并发写入（例如绕过文件系统接口的写入，或改动后又把时间戳还原）。
- 内容读取以每个 worker 固定的 128 KiB 缓冲串流进行，同时在途的只有一批条目；条目按批写入暂存区，因此内存不随文件大小或目录规模增长。`--workers` 限制摘要 worker 数（`1..max(1, 逻辑 CPU 数/2)`，默认 1）。
- 运行记录根目录（绝对路径、大小写策略、卷与文件身份）、扫描参数、条目元数据、摘要状态、覆盖缺口与计数。统计包含已发现、已处理、成功、失败、文件、目录、摘要、读取字节，最后是已发布数量；`--verbose` 另加每个条目与每次摘要的逐条记录。
- 部分完成是刻意允许并发布的：读不到的目录或失败的摘要得到 `coverage = partial`，失败路径写进 `coverage_json`，并以退出码 `3` 结束，让成功区域保持可用而缺口保持可见。用户显式指定的 `--exclude` 同样记入覆盖清单，但不会让结果降级为部分失败。
- 中断：第一次 Ctrl+C 停止新任务、丢弃暂存结果、保留上一代次（首次创建则留下明确未发布的数据库），以 `130` 退出。提交之后才到达的取消不会改写状态，只记录「提交后取消」，绝不报告成回滚。
- 数据库本身、它的 `-journal`/`-wal`/`-shm` 与锁文件会被排除，即使它们在扫描过程中才出现。变化日志目录会在变化日志实现后一并排除。

## 数据库基础

- 驱动：`modernc.org/sqlite v1.62.0`，纯 Go、无需 CGO。选型依据是实测事务、取消、一致性备份与跨平台构建，而不是「纯 Go」这个标签本身。
- 连接：每进程单个连接池，WAL 日志，`busy_timeout`、`foreign_keys=1`、`synchronous=NORMAL`。
- 识别与版本：`PRAGMA application_id = 0x45564E49`（"EVNI"），`PRAGMA user_version` 与 schema 版本（当前 `2`）一致，`store_meta` 表保存 `schema_version` 与 `db_uuid`。只读连接不会切换日志模式，因为那需要写入数据库头部。
- schema 版本 2：`root_dir`（每个数据库恰好一个绑定根目录）、`run`（每次运行一行，含状态、基线代次与已发布代次、覆盖状态与计数）、`entry`（已发布条目，以根目录与路径为键）、`entry_stage`（本次运行的暂存变更）、`entry_event`（变化事件）与 `change_export`（待导出状态）。NULL 一律表示「不可得或不适用」，绝不等同 0 或空字符串：目录没有 `size_bytes`、无摘要的条目没有 `hash_sha256`、平台无法提供创建时间时 `btime_utc` 保持 NULL。
- 暂存与发布：扫描只写 `entry_stage`；一个发布交易把暂存行搬入 `entry`、套用暂存删除、写入变化事件、递增 `root_dir.published_generation` 并把运行标记为已发布。因此读取方只会看到完整代次或上一代次，不会看到半次更新；发布交易失败则整批回滚。首次建立直到提交成功前都保持未发布（`published_generation` 为 NULL），被中断的首次运行不会呈现为完整索引。部分完成的扫描可以发布，但状态为 `coverage = partial` 并记录缺口。
- 写入锁：所有写入口在写模式打开数据库前先取得跨进程锁 `<数据库>.evernightindex.lock`（`store.OpenTarget` 拒绝没有匹配锁的写入开启）。锁是操作系统的字节范围锁，进程结束（含崩溃）即被释放。等待受 `--lock-timeout` 限制且可被取消；锁文件留在原处，其中记录的持有者仅供诊断。
- 运行状态：一次运行从 `running` 转到 `published`、`failed`、`interrupted` 或 `aborted`；残留的 `running` 行会被下一个持有锁的运行标记为 `abandoned`。发布前会比对开始时的代次，已被他人覆盖的结果会被拒绝而不是被覆盖（`E_PUBLISH_CONFLICT`）。
- 事务：`store.WithTx` 成功则提交，否则一定回滚，调用方只会看到「已提交」或「未提交」两种状态；取消信号会传递给驱动。
- 一致性备份：`store.Backup` 使用 `VACUUM INTO` 写入目标目录的临时文件后再更名，不裸复制可能遗漏 WAL 内容的主文件，并拒绝覆盖已存在的目标。
- 拒绝处理：其他程序建立的数据库、较新的格式版本、未知或不完整的结构、损坏的文件、缺失的数据库、把目录当数据库路径，以及绑定根目录不一致，都会得到稳定错误码（`E_DATABASE_FOREIGN`、`E_DATABASE_VERSION_TOO_NEW`、`E_DATABASE_UNKNOWN_SCHEMA`、`E_DATABASE_CORRUPT`、`E_DATABASE_MISSING`、`E_DATABASE_IO`、`E_ROOT_MISMATCH`），且不会覆盖任何内容。
- 迁移：只迁移明确支持的旧版本（版本 1 到 2）；版本 1 文件中若含迁移不认识的表，直接拒绝，不自动重建。

在 Windows amd64 上由 `go test ./internal/store ./internal/app ./internal/output ./internal/cli` 实际执行验证：WAL 建库、提交、回滚可见性、排除未提交事务的一致性快照、备份完整性检查、`PRAGMA integrity_check`、长查询中途取消且之后连接可继续使用、只读打开拒绝写入、暂存条目发布前不可见发布后可见、发布交易失败时旧代次与旧条目完全不变、代次冲突、首次建立被中断后保持未发布、部分完成与缺口记录、变化事件与待导出状态随索引同交易提交、版本 1 迁移保留数据库 UUID、拒绝外来/较新版本/未知结构/损坏的数据库、根目录不一致，以及第二个进程持有锁时的竞争、逾时、取消与「持有进程结束后锁被释放且旧索引完整并通过完整性检查」。

## 平台验证状态

| 平台 | 构建 | 运行 | 说明 |
| --- | --- | --- | --- |
| Windows 11 / amd64 | 通过 | 通过 | 本步骤实际构建、`go test ./...` 与命令运行 |
| macOS arm64 与 amd64 | 交叉编译通过 | 未验证 | 仅 `CGO_ENABLED=0` 交叉构建；`defaults` 语言识别未在实机验证 |
| Linux amd64 | 交叉编译通过 | 未验证 | 仅 `CGO_ENABLED=0` 交叉构建；未在 Linux 上运行测试 |

交叉编译通过不等于运行验证。扫描器、元数据采集器与摘要模块只在 Windows 上以真实测试验证；macOS 与 Linux 的实现可以编译，对应的测试二进制也能交叉编译，但没有在这两个系统上实际执行。

## 扫描与元数据采集

遍历、元数据与模式匹配模块已就位、有测试覆盖，并且已经接入 `index`；本节说明它们的行为。

- 路径只有一个来源。所有显式相对路径（数据库、日志目录、临时目录、根目录）都按进程启动时取得的调用工作目录解析，不随后续的进程工作目录、扫描根目录或临时目录改变。根目录先转成绝对路径并解析一次符号链接，因此同一个目录永远产生同一组相对路径。
- 遍历记录根目录自身、隐藏项、空目录与所有可访问后代。某个目录的修改时间没有变化也照样进入其子树：mtime 未变不能证明子树没变。
- 默认不跟随链接。符号链接、Windows junction 与其他重解析点都只记录本身，因此不存在环，也不会走出根目录；目标文字另存，状态为 `resolved`、`dangling` 或 `unreadable`。FIFO、socket 与设备文件只读取元数据、绝不打开，因此没有写入端的 FIFO 不会让扫描挂起。
- 字段：类型、大小（仅普通文件）、Go 文件模式、mtime 与 atime、平台提供的创建时间、元数据变更时间、POSIX 的 uid/gid/设备号/inode/硬链接数、Windows 的属性位/卷序列号/64 位文件 ID/重解析点标签，以及 macOS 的 BSD 旗标。Unix 的 `ctime` 绝不当创建时间，取不到的字段保持 NULL 而不是 0。

| 平台 | 创建时间 | 变更时间 | 其他扩展 | 采集方式 |
| --- | --- | --- | --- | --- |
| Windows | 创建时间 | `FILE_BASIC_INFO.ChangeTime` | 属性位、卷序列号、64 位文件 ID、重解析点标签、硬链接数 | 一次 `lstat`，外加一个只申请 `FILE_READ_ATTRIBUTES` 的句柄 |
| macOS | `lstat` 的 birth time | `lstat` 的 ctime | BSD 旗标、uid/gid、设备号、inode、硬链接数 | 单次 `lstat` |
| Linux | `statx` 的 `STATX_BTIME` | `lstat` 的 ctime | uid/gid、设备号、inode、硬链接数 | `lstat` 外加一次 `statx` |

- 路径编码保留原始字节。相对路径、名称与比较键都保留原始字节；显示与日志输出把控制字符与无效 UTF-8 字节转义成 `\xNN`，且该转义可逆。因此含空格、换行或以横线开头的名称，以及 Unix 上的非 UTF-8 名称，都能无损区分。Windows 文件名不允许换行，该样本只在其他平台上验证。
- 通配符语义只在一处定义。`/` 与 `\` 都视为分隔符，且没有转义字符；`*` 匹配任意多个非分隔符字符，`?` 匹配恰好一个，`[abc]`、`[a-z]`、`[!abc]` 是字符类；`**` 只有在整段出现时才匹配零个或多个路径段；开头的 `/` 锚定到根目录，不含分隔符的模式匹配任意深度的名称，结尾的 `/` 限定只匹配目录。匹配遵循数据库记录的根目录大小写策略，无效模式报 `E_INVALID_PATTERN`。
- 排除范围：显式模式、本程序自产的文件（数据库、它的 `-journal`/`-wal`/`-shm` 辅助文件与 `<数据库>.evernightindex.lock` 锁文件）、配置的变化日志目录，以及本次执行专属的工作子目录。只排除这些明确路径与目录，临时父目录中的无关用户文件照常索引。
- 覆盖证据保持显式。目录枚举成功、仅部分成功、枚举失败、条目被排除与根目录失效分别表达，缺口以相对路径前缀清单记录。只有成功枚举的目录才能作为后续删除缺失条目的依据；被排除、失败或跨文件系统的子树永远不能。未知总量不会被编造成百分比。
- 并发与取消都有上限：目录枚举使用至多 `max(1, 逻辑 CPU 数 / 2)` 个 worker（默认 1）与有界队列，一个消费端把条目批次交给调用方，另一个消费端输出事件。单个条目或目录读不到只记录并继续；取消会停止新工作并把结果标记为不完整。
- 本步骤不读取普通文件内容：不计算 SHA256，也不打开任何文件数据；Windows 上的元数据句柄只申请属性权限。

## 尚未实现

`index` 现在能建立完整的第一份索引，`info`、统计、运行生命周期、暂存/发布边界与写锁都已就位。仍缺：带摘要复用与保守删除判定的增量更新与细致更新（目前直接拒绝既有索引）、变化日志 CSV 导出与重试、类型化筛选、字段选择与 JSONL/CSV/NUL 输出、`auto`/`metadata`/`sha256` 重复检测与现场复核、临时工作副本的发布与恢复。

## 目录结构

```text
cmd/evernightindex      入口、版本注入、两段式中断处理、退出码交接
internal/cli            参数集中声明、解析、帮助、分派
internal/app            运行生命周期（锁、运行记录、发布或放弃）、info 报告、输出层
internal/model          领域类型、稳定错误码与退出码
internal/store          SQLite 结构定义与迁移、暂存与发布、写入锁、备份
internal/fsmeta         按平台采集元数据、身份比较、大小写策略探测
internal/scan           遍历、排除规则、单文件系统范围、覆盖事件
internal/hash           串流 SHA256、读前读后稳定性检查、受控 worker
internal/index          扫描结果转暂存、摘要决策、覆盖缺口
internal/match          共享的名称/路径通配符匹配
internal/platform       以构建标签隔离的系统事实（系统语言）
internal/output         标准输出/标准错误分流、详细度、线程安全统计、每秒节拍
internal/i18n           四语言消息资源、语言解析、资源检查
```

## 许可证与署名

- 许可证：木兰宽松许可证第 2 版（`MulanPSL-2.0`），完整文本见 [LICENSE](LICENSE)。
- 版权与指定署名：`Copyright (c) KagurazakaMiyabi(KagurazakaYashi)`，再分发时须保持这一写法。
- 第三方依赖保留各自的许可证：直接依赖 `modernc.org/sqlite` 为 BSD-3-Clause，`go list -m -all` 可列出全部模块，`go.mod` 锁定本工程使用的版本。
