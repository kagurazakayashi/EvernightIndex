# EvernightIndex

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja-JP.md)

EvernightIndex is a Go command-line tool that keeps a SQLite index of one directory tree on Windows, macOS and Linux, and answers questions about it: attribute and name search, change logs, and duplicate detection between two indexed directories. It never deletes, moves or hard-links user files.

This file is the English master; the other three editions mirror its structure and version.

## Status

- Version `0.1.0-dev` (project skeleton). The command contract, the four message catalogs and the SQLite foundation are in place.
- Every business command (`index`, `find`, `dupes`, `info`, `export-log`, `recover`) parses and validates its options, then exits with code `1` and an `E_NOT_IMPLEMENTED` message. `--help` and `--version` never create a database and never scan a directory.
- Scanning, index creation and update, SHA256 hashing, change-log CSV export, queries, duplicate detection, temporary working copies and recovery are **not implemented yet**. The sections below describe the fixed contract; they do not claim working features.

## Requirements

- Go `1.26.0` or newer to build (required by the SQLite driver). This step was built and tested with `go1.26.5 windows/amd64`.
- No CGO and no C compiler: `CGO_ENABLED=0` is enough for Windows, macOS and Linux.
- No runtime dependency besides a writable location for the database; the default database path is `<root>/index.db`.

## Build and test

```sh
git clone https://github.com/kagurazakayashi/EvernightIndex.git
cd EvernightIndex
go build ./cmd/evernightindex
go test ./...
```

Inject version information:

```sh
go build -ldflags "-X main.version=0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=2026-11-11" ./cmd/evernightindex
```

Cross-compile (every target builds with `CGO_ENABLED=0`):

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./cmd/evernightindex
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build ./cmd/evernightindex
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build ./cmd/evernightindex
```

In Windows PowerShell set `$env:CGO_ENABLED="0"` and `$env:GOOS="windows"` before running the same `go build`.

`scripts/verify.sh` runs the same format, vet and test checks plus the four cross-compilations in one step (`JOBS`, `GOMAXPROCS` and `OUT` limit parallelism and output location).

## Quick start

```sh
evernightindex --version
evernightindex --help
evernightindex index --help
evernightindex --lang zh-CN --help
evernightindex index /data --help
```

In `cmd` use double quotes around paths with spaces (`--db "C:\\my data\\index.db"`); in PowerShell single quotes are often simpler. Running `index /data` without `--help` currently fails with exit code `1` and touches nothing.

## Commands

| Command | Arguments | Status | Purpose |
| --- | --- | --- | --- |
| `index` | `<dir>` | not implemented | Create or update the index of one directory |
| `find` | none | not implemented | Query indexed entries by name, path or typed filter |
| `dupes` | `<dir-a> <dir-b>` | not implemented | Compare two indexed directories for duplicate regular files |
| `info` | none | not implemented | Show bound root, format version, run status and digest coverage |
| `export-log` | none | not implemented | Export pending change-log events of one run |
| `recover` | none | not implemented | Retry publishing a finished temporary working copy |

## Options

| Long | Short | Type | Default | Notes |
| --- | --- | --- | --- | --- |
| `--help` | `-h` | bool | `false` | global |
| `--version` | `-V` | bool | `false` | global |
| `--lang` | `-l` | enum | `auto` | global; allowed: auto / zh-CN / zh-TW / en-US / ja-JP |
| `--verbose` | `-v` | bool | `false` | global |
| `--quiet` | `-q` | bool | `false` | global |
| `--progress` | `-p` | bool | `true` | global |
| `--db` | `-d` | string | (none) | index, find, info, export-log |
| `--log-dir` | `-L` | string | (none) | index, export-log |
| `--temp-dir` | `-t` | string | (none) | index, recover |
| `--sha256` | `-s` | bool | `true` | index; conflicts: --deep |
| `--deep` | `-D` | bool | `false` | index; conflicts: --sha256 |
| `--change-log` | `-c` | bool | `false` | index |
| `--exclude` | `-e` | string[] | (none) | index; repeatable |
| `--one-file-system` | `-x` | bool | `false` | index |
| `--workers` | `-w` | int | `0` | index; 0..floor(CPU/2) |
| `--lock-timeout` | `-T` | duration | `30s` | index |
| `--read-retries` | `-R` | int | `1` | index |
| `--name` | `-n` | string | (none) | find |
| `--path` | `-P` | string | (none) | find |
| `--filter` | `-f` | string | (none) | find |
| `--case` | `-C` | enum | `auto` | find; allowed: auto / sensitive / insensitive |
| `--details` | `-i` | bool | `false` | find, dupes; conflicts: --null |
| `--fields` | `-F` | string | (none) | find, dupes |
| `--sort` | `-S` | string | (none) | find, dupes |
| `--limit` | `-N` | int | `0` | find, dupes |
| `--offset` | `-O` | int | `0` | find, dupes |
| `--format` | `-o` | enum | `text` | find, dupes; allowed: text / jsonl / csv |
| `--null` | `-z` | bool | `false` | find, dupes; conflicts: --details |
| `--db-a` | `-a` | string | (none) | dupes |
| `--db-b` | `-b` | string | (none) | dupes |
| `--match` | `-m` | enum | `auto` | dupes; allowed: auto / metadata / sha256 |
| `--verify` | `-r` | enum | `none` | dupes; allowed: none / metadata / sha256 |
| `--run` | `-u` | string | (none) | export-log, recover |

All options have exactly one long name and one single-letter short name, and both forms behave identically. Accepted forms are `--option=value`, `--option value`, `-o value` and `-o=value`. Short options never cluster (`-vq` is rejected) and never glue their value (`-dpath` is rejected). A scalar option given twice keeps the last value; list options accumulate in order. `--` ends option parsing so that paths may start with `-`. Long options are never abbreviated: `--work` is unknown, `--workers` is not. Invalid combinations are rejected before any scan or database change: `--deep` with `--sha256=false`, `--quiet` with `--verbose`, `--null` with `--details`/`--format=jsonl`/`--format=csv`, `--verify=sha256` without `--match=sha256`, and `--log-dir` without `--change-log` on `index`.

## Languages

Resolution order: `--lang`/`-l` > `EVERNIGHTINDEX_LANG` > system locale > English.

- Windows: the user interface locale (BCP47, for example `zh-TW`).
- macOS: `defaults read -g AppleLocale`.
- Linux and other Unix systems: `LC_ALL`, then `LC_MESSAGES`, then `LANG`. A `C`/`POSIX` locale counts as "unspecified" and falls back to English.

| Tag | Aliases |
| --- | --- |
| `zh-CN` | `zh`, `zh_CN`, `zh-Hans`, `zh-SG` |
| `zh-TW` | `zh_TW`, `zh-Hant`, `zh-Hant-TW`, `zh-HK`, `zh-MO` |
| `en-US` | `en`, `en_US`, `en-GB` |
| `ja-JP` | `ja`, `ja_JP` |
| `auto` | follow the environment |

An invalid explicit value is an error (`E_INVALID_LANGUAGE`) and is never silently replaced. Machine fields — option names, command names, enum values, error codes, CSV headers, JSONL keys and numbers — are not translated; only human-readable text is. Missing entries fall back to English, and the catalogs are checked for missing keys and placeholder mismatches by the test suite.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | success, including empty results |
| `1` | fatal error (including "not implemented") |
| `2` | invalid arguments or format |
| `3` | partial failure or pending log export |
| `130` | interrupted by the user |

## Output streams

- stdout carries results only: query rows, duplicate groups, or explicitly requested structured output.
- stderr carries progress, warnings, errors, per-path verbose records and the final statistics.
- `--quiet` disables regular progress (errors stay visible), `--verbose` adds one line per processed path, and `--progress=false` disables progress explicitly. `--quiet` together with `--verbose` is rejected.

## Database foundation

- Driver: `modernc.org/sqlite v1.62.0`, pure Go, no CGO. It was chosen after verifying transactions, cancellation, consistent backup and cross-compilation, not for the "pure Go" label alone.
- Connection: a single pooled connection per process, WAL journal, `busy_timeout`, `foreign_keys=1`, `synchronous=NORMAL`.
- Identity and version: `PRAGMA application_id = 0x45564E49` ("EVNI"), `PRAGMA user_version` mirrors the schema version (currently `1`), and `store_meta` holds `schema_version` and `db_uuid`.
- Transactions: `store.WithTx` commits on success and always rolls back otherwise, so callers only ever observe a committed or an uncommitted state. A cancelled context is passed through to the driver.
- Consistent backup: `store.Backup` runs `VACUUM INTO` into a temporary file next to the target and renames it afterwards. It never copies a main file that may be missing data still held in the WAL, and it refuses an existing target.
- Refusals: a file created by another program, a newer format version, a directory path or a missing database yields a stable error code (`E_DATABASE_FOREIGN`, `E_DATABASE_VERSION_TOO_NEW`, `E_DATABASE_MISSING`, `E_DATABASE_IO`).

Verified by `go test ./internal/store` on Windows amd64 (real execution): creation with WAL, commit, rollback visibility, consistent snapshot that excludes an uncommitted transaction, backup integrity check, `PRAGMA integrity_check`, cancellation in the middle of a long query with a usable connection afterwards, read-only open rejecting writes, rejection of a foreign database and of a newer format version, and rejection of a directory given as a database path.

## Platform verification

| Platform | Build | Run | Notes |
| --- | --- | --- | --- |
| Windows 11 / amd64 | yes | yes | Real build, `go test ./...` and CLI runs in this step |
| macOS arm64 and amd64 | cross-compiled | no | `CGO_ENABLED=0` cross build only; `defaults` locale detection is untested |
| Linux amd64 | cross-compiled | no | `CGO_ENABLED=0` cross build only; tests were not executed on Linux |

Cross-compiling is not runtime verification. Platform-specific metadata collection still has to be implemented and verified per platform.

## Not implemented yet

Scanning with exclusions and one-file-system limits; platform metadata collection; streamed SHA256 with read-stability checks; index creation and fast/deep update with conservative deletion rules; change-log CSV export and retry; typed filters, field selection and JSONL/CSV/NUL output; duplicate detection with `auto`/`metadata`/`sha256` plus on-demand verification; temporary working copies with publish and recovery; per-second statistics.

## Project layout

```text
cmd/evernightindex      entry point, version injection, exit-code hand-over
internal/cli            option registry, parser, help, dispatch
internal/app            task orchestration (currently rejects unimplemented commands)
internal/model          stable error codes and exit codes
internal/store          SQLite open/identity/transaction/backup foundation
internal/platform       per-OS facts (system locale) behind build tags
internal/output         stdout/stderr separation and verbosity
internal/i18n           four message catalogs, language resolution, catalog checks
```

## License and author

- License: Mulan Permissive Software License, Version 2 (`MulanPSL-2.0`); the full text is in [LICENSE](LICENSE).
- Copyright and required signature: `Copyright (c) KagurazakaMiyabi(KagurazakaYashi)`. Keep this exact form in redistributed notices.
- Third-party dependencies keep their own licenses: the direct dependency `modernc.org/sqlite` is BSD-3-Clause, `go list -m -all` lists every module, and `go.mod` pins the versions used here.
