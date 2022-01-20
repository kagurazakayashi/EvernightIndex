# EvernightIndex

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja-JP.md)

EvernightIndex is a Go command-line tool that keeps a SQLite index of one directory tree on Windows, macOS and Linux, and answers questions about it: attribute and name search, change logs, and duplicate detection between two indexed directories. It never deletes, moves or hard-links user files.

This file is the English master; the other three editions mirror its structure and version.

## Status

- Version `0.1.0-dev` (project skeleton plus the storage and run-control layer). The command contract, the four message catalogs, the SQLite foundation, schema version 2, staging and publish generations, the cross-process write lock, the run lifecycle and the per-second statistics are in place.
- `info` is implemented as a read-only command: it reports the format version, database UUID, bound root, published generation, entry and digest coverage, the latest run and pending change-log exports. It never creates, migrates or locks a database.
- Every other business command (`index`, `find`, `dupes`, `export-log`, `recover`) parses and validates its options, then exits with code `1` and an `E_NOT_IMPLEMENTED` message. Scanning, SHA256 hashing, change-log CSV export, queries, duplicate detection, temporary working copies and recovery are **not implemented yet**; the sections below describe the fixed contract and do not claim working features.

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
evernightindex info --db /data/index.db
```

In `cmd` use double quotes around paths with spaces (`--db "C:\\my data\\index.db"`); in PowerShell single quotes are often simpler. `info` opens an existing database read-only: without `--db` it exits with code `2`, and a missing, foreign, corrupt, newer or incomplete database is refused with a stable error code instead of being modified. Running `index /data` without `--help` still fails with code `1` and touches nothing.

## Commands

| Command | Arguments | Status | Purpose |
| --- | --- | --- | --- |
| `index` | `<dir>` | not implemented | Create or update the index of one directory |
| `find` | none | not implemented | Query indexed entries by name, path or typed filter |
| `dupes` | `<dir-a> <dir-b>` | not implemented | Compare two indexed directories for duplicate regular files |
| `info` | none | implemented (read-only) | Show bound root, format version, run status and digest coverage |
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

- stdout carries results only: query rows, duplicate groups, the `info` report, or explicitly requested structured output.
- stderr carries progress, warnings, errors, per-path verbose records and statistics.
- During a run, statistics go to stderr once per second; a run that finishes in less than a second still prints a final summary, so processed, failed, error-event and published counts are always visible. Statistics keep discovered, processed, succeeded, failed, error events and published entries apart: processed does not mean saved, and a cancelled or rolled-back run keeps `published` at zero.
- `--quiet` disables the regular progress **and** the final summary (errors are never suppressed), `--verbose` adds one line per processed path, and `--progress=false` disables only the per-second ticks while keeping the final summary. `--quiet` together with `--verbose` is rejected.

## Database foundation

- Driver: `modernc.org/sqlite v1.62.0`, pure Go, no CGO. It was chosen after verifying transactions, cancellation, consistent backup and cross-compilation, not for the "pure Go" label alone.
- Connection: a single pooled connection per process, WAL journal, `busy_timeout`, `foreign_keys=1`, `synchronous=NORMAL`. Read-only connections never switch the journal mode, because that writes the database header.
- Identity and version: `PRAGMA application_id = 0x45564E49` ("EVNI"), `PRAGMA user_version` mirrors the schema version (currently `2`), and `store_meta` holds `schema_version` and `db_uuid`.
- Schema version 2: `root_dir` (exactly one bound root per database), `run` (one row per run with status, baseline and published generation, coverage and counters), `entry` (published entries keyed by root and path), `entry_stage` (staged changes of the running run), `entry_event` (change events of a run) and `change_export` (pending CSV export state). NULL always means "not available or not applicable" and never stands for 0 or an empty string: a directory has no `size_bytes`, an entry without a digest has no `hash_sha256`, and `btime_utc` stays NULL when the platform cannot report a creation time.
- Staging and publish: scanning writes only to `entry_stage`; one publish transaction copies the staged rows into `entry`, applies staged deletions, writes the change events, bumps `root_dir.published_generation` and marks the run published. Readers therefore see a complete generation or the previous one, never a half update, and a failing publish transaction rolls back entirely. The first creation stays unpublished (`published_generation` NULL) until a commit succeeds, so an interrupted first run is never presented as a complete index. A partial sweep is published with `coverage = partial` plus the recorded gaps.
- Write lock: every write path takes the cross-process lock `<database>.evernightindex.lock` before opening the database for writing (`store.OpenTarget` refuses a writable open without the matching lock). It is an operating-system byte-range lock, so the process ending releases it even after a crash. Waiting honours `--lock-timeout` and is cancellable; the lock file stays on disk and records the current holder for diagnostics only.
- Runs: a run moves from `running` to `published`, `failed`, `interrupted` or `aborted`; a leftover `running` row is marked `abandoned` by the next run that holds the lock. Publishing compares the generation read at start, so a result that was superseded meanwhile is refused instead of overwritten (`E_PUBLISH_CONFLICT`).
- Transactions: `store.WithTx` commits on success and always rolls back otherwise, so callers only ever observe a committed or an uncommitted state. A cancelled context is passed through to the driver.
- Consistent backup: `store.Backup` runs `VACUUM INTO` into a temporary file next to the target and renames it afterwards. It never copies a main file that may be missing data still held in the WAL, and it refuses an existing target.
- Refusals: a file created by another program, a newer format version, an unknown or incomplete schema, a corrupt file, a missing database, a database path that is a directory and a root that differs from the bound one yield stable error codes (`E_DATABASE_FOREIGN`, `E_DATABASE_VERSION_TOO_NEW`, `E_DATABASE_UNKNOWN_SCHEMA`, `E_DATABASE_CORRUPT`, `E_DATABASE_MISSING`, `E_DATABASE_IO`, `E_ROOT_MISMATCH`) and nothing is overwritten.
- Migration: only explicitly supported older versions are migrated (version 1 to 2); a version 1 file that holds tables the migration does not know is refused instead of being rebuilt.

Verified on Windows amd64 by real execution (`go test ./internal/store ./internal/app ./internal/output ./internal/cli`): creation with WAL, commit, rollback visibility, consistent snapshot that excludes an uncommitted transaction, backup integrity check, `PRAGMA integrity_check`, cancellation in the middle of a long query with a usable connection afterwards, read-only open rejecting writes, staged entries invisible before the publish and visible after it, a failed publish transaction leaving the previous generation and entries untouched, generation conflicts, an interrupted first creation staying unpublished, partial coverage with recorded gaps, change events and pending exports committing with the index, version 1 migration preserving the database UUID, refusal of foreign, too-new, unknown and corrupt databases, root mismatch, and a second process holding the lock (contention, timeout, cancellation, and the lock being released when that process exits while the previous index stays intact and passes the integrity check).

## Platform verification

| Platform | Build | Run | Notes |
| --- | --- | --- | --- |
| Windows 11 / amd64 | yes | yes | Real build, `go test ./...` and CLI runs in this step |
| macOS arm64 and amd64 | cross-compiled | no | `CGO_ENABLED=0` cross build only; `defaults` locale detection is untested |
| Linux amd64 | cross-compiled | no | `CGO_ENABLED=0` cross build only; tests were not executed on Linux |

Cross-compiling is not runtime verification. Platform-specific metadata collection still has to be implemented and verified per platform.

## Not implemented yet

Scanning with exclusions and one-file-system limits; platform metadata collection (this step only models the fields); streamed SHA256 with read-stability checks; index creation and fast/deep update with conservative deletion rules; change-log CSV export and retry; typed filters, field selection and JSONL/CSV/NUL output; duplicate detection with `auto`/`metadata`/`sha256` plus on-demand verification; temporary working copies with publish and recovery. `info` is implemented, and the statistics, run lifecycle, staging and publish boundary and write lock are in place and verified with synthetic entries, not with a real scanner yet.

## Project layout

```text
cmd/evernightindex      entry point, version injection, two-stage interrupt, exit-code hand-over
internal/cli            option registry, parser, help, dispatch
internal/app            run lifecycle (lock, run record, publish or abandon), info report, output layer
internal/model          domain types, stable error codes and exit codes
internal/store          SQLite schema and migration, staging and publish, write lock, backup
internal/platform       per-OS facts (system locale) behind build tags
internal/output         stdout/stderr separation, verbosity, thread-safe statistics, per-second ticks
internal/i18n           four message catalogs, language resolution, catalog checks
```

## License and author

- License: Mulan Permissive Software License, Version 2 (`MulanPSL-2.0`); the full text is in [LICENSE](LICENSE).
- Copyright and required signature: `Copyright (c) KagurazakaMiyabi(KagurazakaYashi)`. Keep this exact form in redistributed notices.
- Third-party dependencies keep their own licenses: the direct dependency `modernc.org/sqlite` is BSD-3-Clause, `go list -m -all` lists every module, and `go.mod` pins the versions used here.
