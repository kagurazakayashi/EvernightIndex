# EvernightIndex

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja-JP.md)

EvernightIndex は Go で実装した Windows / macOS / Linux 向けのコマンドラインツールです。1 つのディレクトリツリーの SQLite 索引を保持し、属性と名前の検索、変更ログ、索引済みの 2 ディレクトリ間の重複検出に答えます。利用者のファイルを削除・移動・ハードリンク化することはありません。

本ページは日本語版です。構成とバージョンは英語の主文書と同期しています。

## 現在の状態

- バージョン `0.1.0-dev`（プロジェクトの骨組み）。コマンド契約、4 言語のメッセージ資源、SQLite の基盤が揃っています。
- 業務コマンド（`index`、`find`、`dupes`、`info`、`export-log`、`recover`）は引数の解析と検証だけを行い、終了コード `1` と `E_NOT_IMPLEMENTED` で失敗します。`--help` と `--version` はデータベースを作成せず、ディレクトリも走査しません。
- 走査、索引の作成と更新、SHA256 計算、変更ログ CSV、検索、重複検出、一時作業コピーと復旧は**未実装**です。以下の章は固定済みの契約を示すもので、利用可能な機能ではありません。

## 動作要件

- ビルドには Go `1.26.0` 以降が必要です（SQLite ドライバの下限）。本ステップでは `go1.26.5 windows/amd64` でビルドとテストを実施しました。
- CGO と C コンパイラは不要です。`CGO_ENABLED=0` で Windows / macOS / Linux をビルドできます。
- 実行時に必要なのはデータベースを置ける書き込み可能な場所だけです。既定のデータベースパスは `<ルート>/index.db` です。

## ビルドとテスト

```sh
git clone https://github.com/kagurazakayashi/EvernightIndex.git
cd EvernightIndex
go build ./cmd/evernightindex
go test ./...
```

バージョン情報の注入:

```sh
go build -ldflags "-X main.version=0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=2026-11-11" ./cmd/evernightindex
```

クロスコンパイル（3 対象とも `CGO_ENABLED=0`）:

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/evernightindex
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build ./cmd/evernightindex
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build ./cmd/evernightindex
```

Windows PowerShell では先に `$env:CGO_ENABLED="0"` と `$env:GOOS="windows"` を設定してから同じ `go build` を実行します。

`scripts/verify.sh` は書式検査、vet、テスト、4 対象のクロスビルドをまとめて実行します（`JOBS`、`GOMAXPROCS`、`OUT` で並列度と出力先を制限できます）。

## クイックスタート

```sh
evernightindex --version
evernightindex --help
evernightindex index --help
evernightindex --lang ja-JP --help
evernightindex index /data --help
```

`cmd` では空白を含むパスを二重引用符で囲みます（`--db "C:\\my data\\index.db"`）。PowerShell では単一引用符が簡単です。`--help` を付けずに `index /data` を実行すると、現状は終了コード `1` で失敗し、何も読み書きしません。

## コマンド

| コマンド | 位置引数 | 状態 | 用途 |
| --- | --- | --- | --- |
| `index` | `<ディレクトリ>` | 未実装 | 1 つのディレクトリの索引を作成または更新する |
| `find` | なし | 未実装 | 名前・パス・型付き条件で索引済み項目を検索する |
| `dupes` | `<ディレクトリA> <ディレクトリB>` | 未実装 | 索引済みの 2 ディレクトリを比較し、重複する通常ファイルを探す |
| `info` | なし | 未実装 | 対象ルート・形式バージョン・実行状態・ダイジェスト網羅率を表示する |
| `export-log` | なし | 未実装 | 指定した実行の未書き出し変更ログを書き出す |
| `recover` | なし | 未実装 | 完了済みで検証に通った一時作業コピーの公開を再試行する |

## オプション

| 長いオプション | 短いオプション | 型 | 既定値 | 備考 |
| --- | --- | --- | --- | --- |
| `--help` | `-h` | bool | `false` | グローバル |
| `--version` | `-V` | bool | `false` | グローバル |
| `--lang` | `-l` | enum | `auto` | グローバル; 許可値: auto / zh-CN / zh-TW / en-US / ja-JP |
| `--verbose` | `-v` | bool | `false` | グローバル |
| `--quiet` | `-q` | bool | `false` | グローバル |
| `--progress` | `-p` | bool | `true` | グローバル |
| `--db` | `-d` | string | （なし） | index, find, info, export-log |
| `--log-dir` | `-L` | string | （なし） | index, export-log |
| `--temp-dir` | `-t` | string | （なし） | index, recover |
| `--sha256` | `-s` | bool | `true` | index; 排他: --deep |
| `--deep` | `-D` | bool | `false` | index; 排他: --sha256 |
| `--change-log` | `-c` | bool | `false` | index |
| `--exclude` | `-e` | string[] | （なし） | index; 繰り返し可 |
| `--one-file-system` | `-x` | bool | `false` | index |
| `--workers` | `-w` | int | `0` | index; 0..floor(CPU/2) |
| `--lock-timeout` | `-T` | duration | `30s` | index |
| `--read-retries` | `-R` | int | `1` | index |
| `--name` | `-n` | string | （なし） | find |
| `--path` | `-P` | string | （なし） | find |
| `--filter` | `-f` | string | （なし） | find |
| `--case` | `-C` | enum | `auto` | find; 許可値: auto / sensitive / insensitive |
| `--details` | `-i` | bool | `false` | find, dupes; 排他: --null |
| `--fields` | `-F` | string | （なし） | find, dupes |
| `--sort` | `-S` | string | （なし） | find, dupes |
| `--limit` | `-N` | int | `0` | find, dupes |
| `--offset` | `-O` | int | `0` | find, dupes |
| `--format` | `-o` | enum | `text` | find, dupes; 許可値: text / jsonl / csv |
| `--null` | `-z` | bool | `false` | find, dupes; 排他: --details |
| `--db-a` | `-a` | string | （なし） | dupes |
| `--db-b` | `-b` | string | （なし） | dupes |
| `--match` | `-m` | enum | `auto` | dupes; 許可値: auto / metadata / sha256 |
| `--verify` | `-r` | enum | `none` | dupes; 許可値: none / metadata / sha256 |
| `--run` | `-u` | string | （なし） | export-log, recover |

すべてのオプションには長い名前が 1 つと 1 文字の短い名前が 1 つあり、どちらも同じ動作です。`--option=value`、`--option value`、`-o value`、`-o=value` を受け付けます。短いオプションは連結できず（`-vq` は拒否）、値の連結（`-dpath`）も拒否します。同じスカラーオプションを複数回指定した場合は最後の値を使い、リスト型は出現順に累積します。`--` はオプション解析を終了するため、`-` で始まるパスも指定できます。長いオプションの前方一致略記は受け付けません（`--work` は不明、`--workers` が正しい）。不正な組み合わせは走査やデータベース変更の前に拒否します: `--deep` と `--sha256=false`、`--quiet` と `--verbose`、`--null` と `--details`/`--format=jsonl`/`--format=csv`、`--verify=sha256` に `--match=sha256` がない場合、`index` で `--log-dir` に `--change-log` がない場合です。

## 言語

優先順位: `--lang`/`-l` > `EVERNIGHTINDEX_LANG` > システム言語 > 英語。

- Windows: ユーザーインターフェース言語（BCP47、例 `zh-TW`）。
- macOS: `defaults read -g AppleLocale`。
- Linux とその他の Unix: `LC_ALL`、`LC_MESSAGES`、`LANG` の順。`C`/`POSIX` は「未指定」とみなし英語に戻します。

| タグ | 別名 |
| --- | --- |
| `zh-CN` | `zh`、`zh_CN`、`zh-Hans`、`zh-SG` |
| `zh-TW` | `zh_TW`、`zh-Hant`、`zh-Hant-TW`、`zh-HK`、`zh-MO` |
| `en-US` | `en`、`en_US`、`en-GB` |
| `ja-JP` | `ja`、`ja_JP` |
| `auto` | 環境から自動判定 |

明示された不正な値はエラー（`E_INVALID_LANGUAGE`）であり、暗黙に他の言語へ置き換えません。機械的な項目（オプション名、コマンド名、列挙値、エラーコード、CSV 見出し、JSONL のキー、数値）は翻訳せず、人間向けの文だけを翻訳します。欠けた項目は英語に戻し、4 言語の欠落キーとプレースホルダーの一致はテストで検査します。

## 終了コード

| コード | 意味 |
| --- | --- |
| `0` | 成功（結果が 0 件の場合を含む） |
| `1` | 致命的エラー（「未実装」を含む） |
| `2` | 引数または形式が不正 |
| `3` | 部分的な失敗、または未書き出しのログがある |
| `130` | 利用者による中断 |

## 出力ストリーム

- 標準出力は結果のみ: 検索結果の行、重複グループ、明示的に要求された構造化出力。
- 標準エラーは進捗、警告、エラー、パスごとの詳細記録、最終統計。
- `--quiet` は通常の進捗を止め（エラーは表示）、`--verbose` はパスごとの記録を追加し、`--progress=false` は進捗を明示的に止めます。`--quiet` と `--verbose` の同時指定は拒否されます。

## データベース基盤

- ドライバ: `modernc.org/sqlite v1.62.0`（純 Go、CGO 不要）。選定は「純 Go」というラベルではなく、トランザクション、キャンセル、一貫性バックアップ、クロスコンパイルを実測した結果です。
- 接続: プロセスごとに 1 接続のプール、WAL、`busy_timeout`、`foreign_keys=1`、`synchronous=NORMAL`。
- 識別とバージョン: `PRAGMA application_id = 0x45564E49`（"EVNI"）、`PRAGMA user_version` は schema バージョン（現在 `1`）と一致し、`store_meta` に `schema_version` と `db_uuid` を保存します。
- トランザクション: `store.WithTx` は成功時にコミットし、それ以外は必ずロールバックするため、呼び出し側は「コミット済み」か「未コミット」のどちらかしか観測しません。キャンセルはドライバまで伝わります。
- 一貫性バックアップ: `store.Backup` は `VACUUM INTO` で対象ディレクトリの一時ファイルに書き出してから名前を変更します。WAL に残るデータを取りこぼす主ファイルの複製は行わず、既存の対象は上書きしません。
- 拒否: 他プログラムが作成したファイル、より新しい形式バージョン、ディレクトリパス、存在しないデータベースには安定したエラーコード（`E_DATABASE_FOREIGN`、`E_DATABASE_VERSION_TOO_NEW`、`E_DATABASE_MISSING`、`E_DATABASE_IO`）を返します。

Windows amd64 で `go test ./internal/store` により実測: WAL での作成、コミット、ロールバックの可視性、未コミットトランザクションを除外した一貫性スナップショット、バックアップの整合性検査、`PRAGMA integrity_check`、長い問い合わせの途中キャンセルとその後も使える接続、読み取り専用での書き込み拒否、外部データベースとより新しいバージョンの拒否、ディレクトリをパスに指定した場合の拒否。

## プラットフォーム検証状況

| プラットフォーム | ビルド | 実行 | 備考 |
| --- | --- | --- | --- |
| Windows 11 / amd64 | 成功 | 成功 | 本ステップで実際にビルド、`go test ./...`、コマンド実行 |
| macOS arm64 と amd64 | クロスビルド成功 | 未検証 | `CGO_ENABLED=0` のクロスビルドのみ。`defaults` による言語判定は実機未検証 |
| Linux amd64 | クロスビルド成功 | 未検証 | `CGO_ENABLED=0` のクロスビルドのみ。Linux 上でのテスト実行なし |

クロスビルドの成功は実行検証ではありません。プラットフォーム固有のメタデータ収集は今後それぞれの実機で実装・検証する必要があります。

## 未実装

除外規則と単一ファイルシステム制限を伴う走査、プラットフォーム別メタデータ収集、読み取り安定性検査付きのストリーミング SHA256、索引の作成と高速/詳細更新および保守的な削除判定、変更ログ CSV の書き出しと再試行、型付き条件・フィールド選択・JSONL/CSV/NUL 出力、`auto`/`metadata`/`sha256` の重複検出と現場再確認、一時作業コピーの公開と復旧、毎秒統計。

## ディレクトリ構成

```text
cmd/evernightindex      エントリポイント、バージョン注入、終了コードの受け渡し
internal/cli            オプションの集中宣言、解析、ヘルプ、振り分け
internal/app            タスク編成（現在は未実装コマンドを拒否）
internal/model          安定したエラーコードと終了コード
internal/store          SQLite のオープン、識別、トランザクション、バックアップ基盤
internal/platform       ビルドタグで隔離した OS 事実（システム言語）
internal/output         標準出力/標準エラーの分離と詳細度
internal/i18n           4 言語メッセージ資源、言語解決、資源検査
```

## ライセンスと署名

- ライセンス: Mulan Permissive Software License, Version 2（`MulanPSL-2.0`）。全文は [LICENSE](LICENSE) にあります。
- 著作権と指定の署名: `Copyright (c) KagurazakaMiyabi(KagurazakaYashi)`。再配布時はこの表記を保ってください。
- 第三者依存はそれぞれのライセンスを保持します。直接依存の `modernc.org/sqlite` は BSD-3-Clause で、`go list -m -all` で全モジュールを確認でき、`go.mod` が本プロジェクトで使うバージョンを固定しています。
