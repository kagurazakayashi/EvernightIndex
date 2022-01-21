# EvernightIndex

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja-JP.md)

EvernightIndex 是以 Go 開發的 Windows、macOS、Linux 命令列工具，為一個目錄樹建立 SQLite 索引，並據此回答查詢：屬性與名稱搜尋、變更日誌，以及兩個已索引目錄之間的重複檔案偵測。它不會刪除、搬移使用者檔案，也不會自動建立硬連結。

本頁是繁體中文版，與英文主文件的結構、版本保持同步。

## 目前狀態

- 版本 `0.1.0-dev`（工程骨架並已加入儲存與執行控制層）。命令契約、四語言訊息資源、SQLite 基礎、schema 版本 2、暫存與發佈代次、跨行程寫入鎖、執行生命週期與每秒統計已就位。
- `info` 已實作為唯讀命令：報告格式版本、資料庫 UUID、綁定根目錄、已發佈代次、條目與摘要覆蓋率、最近一次執行以及待匯出日誌。它不會建立、遷移或鎖定資料庫。
- 走訪與中繼資料模組（`internal/scan`、`internal/fsmeta`）與共用模式比對模組（`internal/match`）已實作，並在 Windows 上以真實測試驗證；目前還沒有任何命令呼叫它們，因此命令列仍不會掃描目錄。實際行為見下方「掃描與中繼資料蒐集」。
- 其餘業務命令（`index`、`find`、`dupes`、`export-log`、`recover`）只做參數解析與驗證，隨後以退出碼 `1` 與 `E_NOT_IMPLEMENTED` 說明失敗。把掃描接入 `index`、SHA256 計算、變更日誌 CSV、查詢、重複偵測、暫存工作副本與復原**尚未實作**；以下章節描述已固定的契約，不代表這些功能可用。

## 環境需求

- 建置需要 Go `1.26.0` 或更新版本（SQLite 驅動的最低要求）。本步驟實際以 `go1.26.5 windows/amd64` 建置與測試。
- 不需要 CGO 與 C 編譯器：`CGO_ENABLED=0` 即可建置 Windows、macOS、Linux 版本。
- 執行期除資料庫所在的可寫位置外沒有額外相依；資料庫預設路徑為 `<根目錄>/index.db`。

## 建置與測試

```sh
git clone https://github.com/kagurazakayashi/EvernightIndex.git
cd EvernightIndex
go build ./cmd/evernightindex
go test ./...
```

注入版本資訊：

```sh
go build -ldflags "-X main.version=0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=2026-11-11" ./cmd/evernightindex
```

交叉編譯（三個目標都使用 `CGO_ENABLED=0`）：

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/evernightindex
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build ./cmd/evernightindex
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build ./cmd/evernightindex
```

Windows PowerShell 中先設定 `$env:CGO_ENABLED="0"` 與 `$env:GOOS="windows"`，再執行同樣的 `go build`。

`scripts/verify.sh` 一次跑完格式檢查、vet、測試與四個目標的交叉編譯（`JOBS`、`GOMAXPROCS`、`OUT` 分別限制並行度與產物位置）。

## 快速開始

```sh
evernightindex --version
evernightindex --help
evernightindex index --help
evernightindex --lang zh-TW --help
evernightindex index /data --help
evernightindex info --db /data/index.db
```

`cmd` 中路徑含空白時用雙引號（`--db "C:\\我的 資料\\index.db"`）；PowerShell 中用單引號通常更省事。`info` 以唯讀方式開啟既有資料庫：缺少 `--db` 時以退出碼 `2` 失敗，資料庫缺失、非本程式建立、損壞、版本較新或索引不完整時以穩定錯誤碼拒絕，不會修改任何檔案。不帶 `--help` 直接執行 `index /data` 仍會以退出碼 `1` 失敗，且不改動任何檔案。

## 命令

| 命令 | 位置參數 | 狀態 | 用途 |
| --- | --- | --- | --- |
| `index` | `<目錄>` | 尚未實作 | 建立或更新一個目錄的索引 |
| `find` | 無 | 尚未實作 | 依名稱、路徑或型別化篩選查詢已索引項目 |
| `dupes` | `<目錄A> <目錄B>` | 尚未實作 | 比較兩個已索引目錄中的重複一般檔案 |
| `info` | 無 | 已實作（唯讀） | 顯示綁定根目錄、格式版本、執行狀態與摘要覆蓋率 |
| `export-log` | 無 | 尚未實作 | 匯出某一次執行中待匯出的變更日誌事件 |
| `recover` | 無 | 尚未實作 | 重試發布已完成且驗證通過的暫存工作副本 |

## 參數

| 長參數 | 短參數 | 型別 | 預設 | 說明 |
| --- | --- | --- | --- | --- |
| `--help` | `-h` | bool | `false` | 全域 |
| `--version` | `-V` | bool | `false` | 全域 |
| `--lang` | `-l` | enum | `auto` | 全域; 可選值: auto / zh-CN / zh-TW / en-US / ja-JP |
| `--verbose` | `-v` | bool | `false` | 全域 |
| `--quiet` | `-q` | bool | `false` | 全域 |
| `--progress` | `-p` | bool | `true` | 全域 |
| `--db` | `-d` | string | （無） | index, find, info, export-log |
| `--log-dir` | `-L` | string | （無） | index, export-log |
| `--temp-dir` | `-t` | string | （無） | index, recover |
| `--sha256` | `-s` | bool | `true` | index; 互斥: --deep |
| `--deep` | `-D` | bool | `false` | index; 互斥: --sha256 |
| `--change-log` | `-c` | bool | `false` | index |
| `--exclude` | `-e` | string[] | （無） | index; 可重複 |
| `--one-file-system` | `-x` | bool | `false` | index |
| `--workers` | `-w` | int | `0` | index; 0..floor(CPU/2) |
| `--lock-timeout` | `-T` | duration | `30s` | index |
| `--read-retries` | `-R` | int | `1` | index |
| `--name` | `-n` | string | （無） | find |
| `--path` | `-P` | string | （無） | find |
| `--filter` | `-f` | string | （無） | find |
| `--case` | `-C` | enum | `auto` | find; 可選值: auto / sensitive / insensitive |
| `--details` | `-i` | bool | `false` | find, dupes; 互斥: --null |
| `--fields` | `-F` | string | （無） | find, dupes |
| `--sort` | `-S` | string | （無） | find, dupes |
| `--limit` | `-N` | int | `0` | find, dupes |
| `--offset` | `-O` | int | `0` | find, dupes |
| `--format` | `-o` | enum | `text` | find, dupes; 可選值: text / jsonl / csv |
| `--null` | `-z` | bool | `false` | find, dupes; 互斥: --details |
| `--db-a` | `-a` | string | （無） | dupes |
| `--db-b` | `-b` | string | （無） | dupes |
| `--match` | `-m` | enum | `auto` | dupes; 可選值: auto / metadata / sha256 |
| `--verify` | `-r` | enum | `none` | dupes; 可選值: none / metadata / sha256 |
| `--run` | `-u` | string | （無） | export-log, recover |

每個參數恰好有一個長名稱與一個單字母短名稱，兩種寫法行為完全一致。支援 `--option=value`、`--option value`、`-o value` 與 `-o=value`。短選項不支援串連（`-vq` 會被拒絕），也不接受把值與短名稱黏在一起（`-dpath` 會被拒絕）。同一純量參數重複指定取最後一次；可重複列表參數依出現順序累積。`--` 終止選項解析，因此路徑可以以 `-` 開頭。長參數不支援前綴縮寫：`--work` 是未知參數，`--workers` 才是。非法組合會在任何掃描或資料庫變更之前被拒絕：`--deep` 與 `--sha256=false`、`--quiet` 與 `--verbose`、`--null` 與 `--details`/`--format=jsonl`/`--format=csv`、`--verify=sha256` 未搭配 `--match=sha256`，以及 `index` 命令中 `--log-dir` 未搭配 `--change-log`。

## 語言

優先順序：`--lang`/`-l` > `EVERNIGHTINDEX_LANG` > 系統語言 > 英語。

- Windows：使用者介面語言（BCP47，例如 `zh-TW`）。
- macOS：`defaults read -g AppleLocale`。
- Linux 與其他 Unix：先 `LC_ALL`，再 `LC_MESSAGES`，最後 `LANG`。`C`/`POSIX` 視為「未指定」，回退英語。

| 標籤 | 別名 |
| --- | --- |
| `zh-CN` | `zh`、`zh_CN`、`zh-Hans`、`zh-SG` |
| `zh-TW` | `zh_TW`、`zh-Hant`、`zh-Hant-TW`、`zh-HK`、`zh-MO` |
| `en-US` | `en`、`en_US`、`en-GB` |
| `ja-JP` | `ja`、`ja_JP` |
| `auto` | 依環境自動判斷 |

明確指定但不合法的語言是錯誤（`E_INVALID_LANGUAGE`），不會被靜默替換。機器欄位——參數名、命令名、列舉值、錯誤碼、CSV 標題、JSONL 鍵與數值——一律不翻譯，只翻譯人類可讀文字。缺少的條目回退英語；測試會檢查四套資源的缺項與佔位符一致性。

## 退出碼

| 退出碼 | 含義 |
| --- | --- |
| `0` | 成功，包括沒有結果 |
| `1` | 致命錯誤（含「尚未實作」） |
| `2` | 參數或格式不合法 |
| `3` | 部分失敗或有日誌待匯出 |
| `130` | 使用者中斷 |

## 輸出串流

- 標準輸出只放結果：查詢列、重複分組、`info` 報告或明確要求的結構化輸出。
- 標準錯誤放進度、警告、錯誤、逐路徑詳細記錄與統計。
- 執行期間每秒向標準錯誤輸出一次統計；不足一秒的執行也會輸出最終彙總，因此已處理、失敗、錯誤事件與已發佈數量始終可見。統計把已發現、已處理、成功、失敗、錯誤事件與已發佈分開：已處理不代表已保存，取消或回滾後已發佈數量保持 0。
- `--quiet` 關閉一般進度**與**最終彙總（錯誤永不隱藏），`--verbose` 增加逐路徑記錄，`--progress=false` 只關閉每秒節拍並保留最終彙總。`--quiet` 與 `--verbose` 同時使用會被拒絕。

## 資料庫基礎

- 驅動：`modernc.org/sqlite v1.62.0`，純 Go、不需 CGO。選型依據是實測交易、取消、一致性備份與跨平台建置，而不是「純 Go」這個標籤本身。
- 連線：每行程單一連線池，WAL 日誌，`busy_timeout`、`foreign_keys=1`、`synchronous=NORMAL`。
- 識別與版本：`PRAGMA application_id = 0x45564E49`（"EVNI"），`PRAGMA user_version` 與 schema 版本（目前 `2`）一致，`store_meta` 表保存 `schema_version` 與 `db_uuid`。唯讀連線不會切換日誌模式，因為那需要寫入資料庫標頭。
- schema 版本 2：`root_dir`（每個資料庫恰好一個綁定根目錄）、`run`（每次執行一列，含狀態、基線代次與已發佈代次、覆蓋狀態與計數）、`entry`（已發佈條目，以根目錄與路徑為鍵）、`entry_stage`（本次執行的暫存變更）、`entry_event`（變更事件）與 `change_export`（待匯出狀態）。NULL 一律表示「不可得或不適用」，絕不等同 0 或空字串：目錄沒有 `size_bytes`、無摘要的條目沒有 `hash_sha256`、平台無法提供建立時間時 `btime_utc` 保持 NULL。
- 暫存與發佈：掃描只寫 `entry_stage`；一個發佈交易把暫存列搬入 `entry`、套用暫存刪除、寫入變更事件、遞增 `root_dir.published_generation` 並把執行標記為已發佈。因此讀取方只會看到完整代次或上一代次，不會看到半次更新；發佈交易失敗則整批回滾。首次建立直到提交成功前都保持未發佈（`published_generation` 為 NULL），被中斷的首次執行不會呈現為完整索引。部分完成的掃描可以發佈，但狀態為 `coverage = partial` 並記錄缺口。
- 寫入鎖：所有寫入口在寫模式開啟資料庫前先取得跨行程鎖 `<資料庫>.evernightindex.lock`（`store.OpenTarget` 拒絕沒有相符鎖的寫入開啟）。鎖是作業系統的位元組範圍鎖，行程結束（含崩潰）即被釋放。等待受 `--lock-timeout` 限制且可被取消；鎖檔留在原處，其中記錄的持有者僅供診斷。
- 執行狀態：一次執行從 `running` 轉到 `published`、`failed`、`interrupted` 或 `aborted`；殘留的 `running` 列會被下一個持有鎖的執行標記為 `abandoned`。發佈前會比對開始時的代次，已被他人覆蓋的結果會被拒絕而不是被覆蓋（`E_PUBLISH_CONFLICT`）。
- 交易：`store.WithTx` 成功則提交，否則一定回滾，呼叫端只會看到「已提交」或「未提交」兩種狀態；取消訊號會傳遞給驅動。
- 一致性備份：`store.Backup` 以 `VACUUM INTO` 寫入目標目錄的暫存檔後再更名，不裸複製可能遺漏 WAL 內容的主檔，並拒絕覆寫已存在的目標。
- 拒絕處理：其他程式建立的資料庫、較新的格式版本、未知或不完整的結構、損壞的檔案、缺失的資料庫、把目錄當資料庫路徑，以及綁定根目錄不一致，都會得到穩定錯誤碼（`E_DATABASE_FOREIGN`、`E_DATABASE_VERSION_TOO_NEW`、`E_DATABASE_UNKNOWN_SCHEMA`、`E_DATABASE_CORRUPT`、`E_DATABASE_MISSING`、`E_DATABASE_IO`、`E_ROOT_MISMATCH`），且不會覆寫任何內容。
- 遷移：只遷移明確支援的舊版本（版本 1 到 2）；版本 1 檔案中若含遷移不認識的表，直接拒絕，不自動重建。

在 Windows amd64 上由 `go test ./internal/store ./internal/app ./internal/output ./internal/cli` 實際執行驗證：WAL 建庫、提交、回滾可見性、排除未提交交易的一致性快照、備份完整性檢查、`PRAGMA integrity_check`、長查詢中途取消且之後連線仍可使用、唯讀開啟拒絕寫入、暫存條目發佈前不可見發佈後可見、發佈交易失敗時舊代次與舊條目完全不變、代次衝突、首次建立被中斷後保持未發佈、部分完成與缺口記錄、變更事件與待匯出狀態隨索引同交易提交、版本 1 遷移保留資料庫 UUID、拒絕外來/較新版本/未知結構/損壞的資料庫、根目錄不一致，以及第二個行程持有鎖時的競爭、逾時、取消與「持有行程結束後鎖被釋放且舊索引完整並通過完整性檢查」。

## 平台驗證狀態

| 平台 | 建置 | 執行 | 說明 |
| --- | --- | --- | --- |
| Windows 11 / amd64 | 通過 | 通過 | 本步驟實際建置、`go test ./...` 與命令執行 |
| macOS arm64 與 amd64 | 交叉編譯通過 | 未驗證 | 僅 `CGO_ENABLED=0` 交叉建置；`defaults` 語言識別未在實機驗證 |
| Linux amd64 | 交叉編譯通過 | 未驗證 | 僅 `CGO_ENABLED=0` 交叉建置；未在 Linux 上執行測試 |

交叉編譯通過不等於執行驗證。掃描器與中繼資料蒐集器只在 Windows 上以真實測試驗證；macOS 與 Linux 的實作可以編譯，對應測試二進位也能交叉編譯，但沒有在這兩個系統上實際執行。

## 掃描與中繼資料蒐集

走訪與中繼資料模組已就位並有測試覆蓋，但 `index` 命令尚未接入，因此本次建置中下列行為都無法從命令列觸發。

- 路徑只有一個來源。所有顯式相對路徑（資料庫、日誌目錄、暫存目錄、根目錄）都依行程啟動時取得的呼叫工作目錄解析，不隨後續的行程工作目錄、掃描根目錄或暫存目錄改變。根目錄先轉成絕對路徑並解析一次符號連結，因此同一個目錄永遠產生同一組相對路徑。
- 走訪記錄根目錄自身、隱藏項目、空目錄與所有可存取後代。某個目錄的修改時間沒有變化也照樣進入其子樹：mtime 未變不能證明子樹沒變。
- 預設不跟隨連結。符號連結、Windows junction 與其他再解析點都只記錄本身，因此不存在環，也不會走出根目錄；目標文字另存，狀態為 `resolved`、`dangling` 或 `unreadable`。FIFO、socket 與裝置檔只讀取中繼資料、絕不開啟，因此沒有寫入端的 FIFO 不會讓掃描懸置。
- 欄位：型別、大小（僅一般檔案）、Go 檔案模式、mtime 與 atime、平台提供的建立時間、中繼資料變更時間、POSIX 的 uid/gid/裝置號/inode/硬連結數、Windows 的屬性位/卷序列號/64 位檔案 ID/再解析點標籤，以及 macOS 的 BSD 旗標。Unix 的 `ctime` 絕不當建立時間，取不到的欄位保持 NULL 而不是 0。

| 平台 | 建立時間 | 變更時間 | 其他擴充 | 蒐集方式 |
| --- | --- | --- | --- | --- |
| Windows | 建立時間 | `FILE_BASIC_INFO.ChangeTime` | 屬性位、卷序列號、64 位檔案 ID、再解析點標籤、硬連結數 | 一次 `lstat`，外加一個只申請 `FILE_READ_ATTRIBUTES` 的句柄 |
| macOS | `lstat` 的 birth time | `lstat` 的 ctime | BSD 旗標、uid/gid、裝置號、inode、硬連結數 | 單次 `lstat` |
| Linux | `statx` 的 `STATX_BTIME` | `lstat` 的 ctime | uid/gid、裝置號、inode、硬連結數 | `lstat` 外加一次 `statx` |

- 路徑編碼保留原始位元組。相對路徑、名稱與比較鍵都保留原始位元組；顯示與日誌輸出把控制字元與無效 UTF-8 位元組轉義成 `\xNN`，且該轉義可逆。因此含空白、換行或以連字號開頭的名稱，以及 Unix 上的非 UTF-8 名稱，都能無損區分。Windows 檔名不允許換行，該樣本只在其他平台上驗證。
- 通配符語意只在一處定義。`/` 與 `\` 都視為分隔符，且沒有轉義字元；`*` 匹配任意多個非分隔符字元，`?` 匹配恰好一個，`[abc]`、`[a-z]`、`[!abc]` 是字元類別；`**` 只有在整段出現時才匹配零個或多個路徑段；開頭的 `/` 錨定到根目錄，不含分隔符的模式匹配任意深度的名稱，結尾的 `/` 限定只匹配目錄。匹配遵循資料庫記錄的根目錄大小寫策略，無效模式回報 `E_INVALID_PATTERN`。
- 排除範圍：顯式模式、本程式自產的檔案（資料庫、它的 `-journal`/`-wal`/`-shm` 輔助檔與 `<資料庫>.evernightindex.lock` 鎖檔）、設定的變更日誌目錄，以及本次執行專屬的工作子目錄。只排除這些明確路徑與目錄，暫存父目錄中的無關使用者檔案照常索引。
- 覆蓋證據保持顯式。目錄列舉成功、僅部分成功、列舉失敗、項目被排除與根目錄失效分別表達，缺口以相對路徑前綴清單記錄。只有成功列舉的目錄才能作為後續刪除缺失項目的依據；被排除、失敗或跨檔案系統的子樹永遠不能。未知總量不會被編造成百分比。
- 並行與取消都有上限：目錄列舉使用至多 `max(1, 邏輯 CPU 數 / 2)` 個 worker（預設 1）與有界佇列，一個消費端把項目批次交給呼叫方，另一個消費端輸出事件。單一項目或目錄讀不到只記錄並繼續；取消會停止新工作並把結果標記為不完整。
- 本步驟不讀取一般檔案內容：不計算 SHA256，也不開啟任何檔案資料；Windows 上的中繼資料句柄只申請屬性權限。

## 尚未實作

掃描、中繼資料與模式比對模組已實作並有測試覆蓋，但 `index` 命令尚未接入。仍缺：具讀取穩定性檢查的串流 SHA256；索引建立與快速/細緻更新及保守刪除判定；變更日誌 CSV 匯出與重試；型別化篩選、欄位選擇與 JSONL/CSV/NUL 輸出；`auto`/`metadata`/`sha256` 重複偵測與現場複核；暫存工作副本的發布與復原。`info` 已實作，統計、執行生命週期、暫存與發佈邊界與寫入鎖已就位。

## 目錄結構

```text
cmd/evernightindex      進入點、版本注入、兩段式中斷處理、退出碼交接
internal/cli            參數集中宣告、解析、說明、分派
internal/app            執行生命週期（鎖、執行記錄、發佈或放棄）、info 報告、輸出層
internal/model          領域型別、穩定錯誤碼與退出碼
internal/store          SQLite 結構定義與遷移、暫存與發佈、寫入鎖、備份
internal/fsmeta         依平台蒐集中繼資料、身分比較、大小寫策略探測
internal/scan           走訪、排除規則、單一檔案系統範圍、覆蓋事件
internal/match          共用的名稱/路徑通配符比對
internal/platform       以建置標籤隔離的系統事實（系統語言）
internal/output         標準輸出/標準錯誤分流、詳細度、執行緒安全統計、每秒節拍
internal/i18n           四語言訊息資源、語言解析、資源檢查
```

## 授權與署名

- 授權：木蘭寬鬆授權條款第 2 版（`MulanPSL-2.0`），完整文本見 [LICENSE](LICENSE)。
- 版權與指定署名：`Copyright (c) KagurazakaMiyabi(KagurazakaYashi)`，再散布時須保持這一寫法。
- 第三方相依保留各自的授權：直接相依 `modernc.org/sqlite` 為 BSD-3-Clause，`go list -m -all` 可列出全部模組，`go.mod` 鎖定本專案使用的版本。
