# EvernightIndex

[English](README.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [日本語](README.ja-JP.md)

EvernightIndex is a Go command-line tool that keeps a SQLite index of one directory tree on Windows, macOS and Linux, and answers questions about it: attribute and name search, change logs, and duplicate detection between two indexed directories. It never deletes, moves or hard-links user files.

This file is the English master; the other three editions mirror its structure and version.

## Status

- Version `0.1.0-dev` (project skeleton, storage and run-control layer, scanner and streamed SHA256). The command contract, the four message catalogs, the SQLite foundation, schema version 2, staging and publish generations, the cross-process write lock, the run lifecycle, the streaming digest module and the per-second statistics are in place.
- `info` is implemented as a read-only command: it reports the format version, database UUID, bound root, published generation, entry and digest coverage, the latest run and pending change-log exports. It never creates, migrates or locks a database.
- The traversal, metadata and pattern modules (`internal/scan`, `internal/fsmeta`, `internal/match`) and the digest module (`internal/hash`) are implemented and covered by real tests on Windows, and `index` now drives them end to end. See "Scanning and metadata" below for the exact behavior.
- `index` creates and updates one index: it resolves the root and `--db`, verifies the existing format, version, bound root and published generation, walks the tree, streams entries into the staging table, decides per file whether to compute, reuse, keep or clear the digest, classifies every change and publishes in one transaction. Deletions are inferred only where the walk really covered the tree. Change-log CSV export, queries, duplicate detection, temporary working copies and recovery remain **not implemented yet**.

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
evernightindex index /data
evernightindex info --db /data/index.db
```

In `cmd` use double quotes around paths with spaces (`--db "C:\\my data\\index.db"`); in PowerShell single quotes are often simpler. `info` opens an existing database read-only: without `--db` it exits with code `2`, and a missing, foreign, corrupt, newer or incomplete database is refused with a stable error code instead of being modified. `index /data` creates `<root>/index.db` with SHA256 digests and exits with `0`; running it again walks the same tree, reuses the digests of unchanged files and publishes the next generation.

## Commands

| Command | Arguments | Status | Purpose |
| --- | --- | --- | --- |
| `index` | `<dir>` | implemented (create and update) | Create or update the index of one directory |
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

## Creating and updating an index

```sh
evernightindex index /data                 # create /data/index.db with SHA256
evernightindex index /data --sha256=false  # metadata only, file content is never opened
evernightindex index /data --db /backup/index.db --deep
```

- The same command creates or updates. Without `--db` the target is `<root>/index.db`, and an explicit relative `--db` is resolved against the calling working directory. A missing database is created; an existing one is checked read-only first (format version, integrity, bound root path and volume identity) and a different tree is refused with `E_ROOT_MISMATCH` instead of being rebound. A database left unpublished by an interrupted first run holds no published result, so the next run builds the first generation instead of updating one.
- Every run walks the tree again and re-reads all the metadata it can obtain; a directory whose modification time did not change is never skipped. Quick mode (the default) compares entry type, logical size, modification time, file identity and the metadata change time, and a changed identity or change time triggers a conservative recomputation. A change time alone never proves that content changed, so a permission change costs one extra read.
- Digest decision table: with `--sha256=true` in quick mode, new files, files whose quick criteria changed and files without a usable stored digest are read and get a stored digest; every other file keeps its stored digest as `reused`, and the run reports how many files it did not read. With `--sha256=false` no file content is opened at all: new files have no digest, a file whose quick criteria changed loses its stored digest, and the rest keep the previous digest without being called verified. With `--sha256=true --deep` every readable regular file is read again and gets a stored digest. `--sha256=false --deep` is rejected with exit code `2` before anything is scanned or written.
- Digest validity: a failed recomputation, an unreadable file or a file that kept changing leaves no digest (`failed` plus `error_code` and `error_stage`), so an old digest is never presented as current. A changed modification time with an identical digest is a metadata change, not a content change; adding a baseline or recomputing an identical digest is never reported as a content change.
- Conservative deletion: a path is removed only when the walk finished and the path is not inside a failed, partly enumerated or excluded area. Entries under an unreadable or partly enumerated directory are kept and marked `unverified`; a directory excluded by pattern or produced by this program itself is kept and marked `excluded` with an `excluded` event, never as a deletion; the subtree behind a `--one-file-system` mount point stays `unverified`. Such a run publishes a partial result with exit code `3` while the covered area is still updated.
- A path whose type changed is recorded as `type_changed`, and when a directory becomes a file its former children are removed because the coverage evidence proves they are gone. Renames and moves are not inferred in this version: the old path is removed and the new path is added, and a rename is never guessed from an identical digest.
- Change events carry before/after snapshots and are committed in the same transaction as the index. They are kept only when change logs are enabled; with change logs off the run still reports added, updated, removed, excluded, unverified and unchanged counts but keeps no long-term history.
- Quick mode cannot prove that content is unchanged: a modification that leaves type, size, modification time, identity and change time all unchanged is not detected. Use `--deep` when every file must be read again.
- Verified on Windows 11 amd64 by automated tests (`go test ./internal/index ./internal/store ./internal/app ./internal/cli`) and by real CLI runs over a synthetic tree: creation followed by updates that add, remove, retype and exclude entries, digest reuse without reading unchanged files, deep-mode recomputation, digest clearing with `--sha256=false`, a failed recomputation leaving no digest, coverage gaps keeping old records as `unverified`, and a refused or failed run leaving the previous generation and its entries untouched.
- Every option is checked before anything is scanned or written. `--deep` with `--sha256=false` is rejected with exit code `2`; `--change-log` and `--temp-dir` are refused with `E_NOT_IMPLEMENTED` instead of being silently ignored, because change logs and temporary working copies are later steps.
- The root is resolved and validated before the database is created, so an unavailable root (`E_ROOT_UNAVAILABLE`) never produces an empty index.
- Digest states: a regular file stored with `--sha256=true` gets a streamed SHA256 (`computed`); with `--sha256=false` it is recorded as `disabled` and no file is opened at all; directories, symlinks and special files are `not_applicable` and never carry a digest. An empty file gets the standard SHA256 of empty input. A file whose content cannot be read, or that keeps changing while it is read, is recorded as `failed` with an `error_code` and `error_stage`, with no digest at all, and it appears in the coverage gaps. An update keeps the stored digest of a file it did not read and marks it `reused`, which says plainly that it was not verified again in this run.
- Each read opens the file once and checks the same handle before and after reading (identity, size, modification time) plus the values recorded by the scan; a mismatch is retried `--read-retries` times (default 1) and then recorded as a failure. This catches a path that was replaced after the scan and a file modified while it was read; it cannot rule out every concurrent writer (for example a write that bypasses the file system interface, or a change that also restores the timestamps).
- Content is read through a fixed 128 KiB buffer per worker with one bounded batch in flight, and entries are staged batch by batch, so memory does not grow with the file size or the tree size. `--workers` bounds the digest workers (`1..max(1, logical CPUs / 2)`, default 1).
- The run records the root (absolute path, case policy, volume and file identity), the scan parameters, entry metadata, digest state, coverage gaps and the run counters. Statistics cover discovered, processed, succeeded, failed, files, directories, digests, bytes read and the published count, and `--verbose` adds one line per entry and per digest.
- Partial results are published on purpose: an unreadable subdirectory or a failed digest yields `coverage = partial` with the failing paths recorded in `coverage_json` and exit code `3`, so a successful area stays usable while the gap stays visible. An explicit `--exclude` is recorded in the coverage list as well, but it does not turn the run into a partial failure.
- Interruption: the first Ctrl+C stops new work, discards the staged result, keeps the previous generation (or leaves a clearly unpublished database after a first creation) and exits with `130`. A cancel that arrives after the commit does not rewrite the status; it is reported as "cancel after commit" and never as a rollback.
- The database itself, its `-journal`/`-wal`/`-shm` companions and its lock file are excluded even when they appear while the scan is running. The change-log directory will be excluded once change logs exist.

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

Cross-compiling is not runtime verification. The scanner, the metadata collector and the digest module are verified by real tests on Windows; the macOS and Linux implementations are compiled and their test binaries compile for those targets, but they were not executed there.

## Scanning and metadata

The traversal, metadata and pattern modules are in place, tested, and drive `index`; this section describes their behavior.

- Paths come from exactly one place. Every explicit relative path (database, log directory, temporary directory, root) is resolved against the working directory captured when the process starts, never against the process working directory later and never against the scan root or a temporary directory. The root is made absolute and its symlinks are resolved once, so the same directory always yields the same relative paths.
- Traversal records the root itself, hidden entries, empty directories and every accessible descendant. A directory whose modification time did not change is still descended: a stable mtime is not proof that a subtree is unchanged.
- Links are never followed. Symlinks, Windows junctions and every other reparse point are recorded as the jump object itself, so there is no cycle and no access outside the root, and the target text is stored with a state of `resolved`, `dangling` or `unreadable`. FIFOs, sockets and device files are read for metadata only and are never opened, so a FIFO without a writer cannot block the scan.
- Fields: type, size (regular files only), Go file mode, mtime and atime, birth time when the platform reports it, metadata change time, POSIX uid/gid/device/inode/link count, Windows attribute bits/volume serial/64-bit file ID/reparse tag, and macOS BSD flags. Unix `ctime` is never stored as a creation time, and an unavailable field stays NULL instead of 0.

| Platform | Birth time | Change time | Other extensions | How it is read |
| --- | --- | --- | --- | --- |
| Windows | creation time | `FILE_BASIC_INFO.ChangeTime` | attribute bits, volume serial, 64-bit file ID, reparse tag, link count | one `lstat` plus a handle opened with `FILE_READ_ATTRIBUTES` only |
| macOS | `lstat` birth time | `lstat` ctime | BSD flags, uid/gid, device, inode, link count | a single `lstat` |
| Linux | `statx` `STATX_BTIME` | `lstat` ctime | uid/gid, device, inode, link count | `lstat` plus one `statx` call |

- Path encoding keeps raw bytes. The relative path, the name and the comparison key keep the original bytes; display and log output escape control characters and invalid UTF-8 bytes as `\xNN`, and that escaping is reversible. A name with spaces, a newline or a leading dash, and a non-UTF-8 name on Unix, therefore stay distinguishable and lossless. Windows cannot represent a newline in a file name, so that sample is verified only on the other platforms.
- Pattern semantics are defined once in the shared matcher. Both `/` and `\` are separators and there is no escape character; `*` matches any run of non-separator characters, `?` exactly one, `[abc]` / `[a-z]` / `[!abc]` a character class; `**` matches zero or more whole path segments but only as a whole segment; a leading `/` anchors to the root, a pattern with no separator matches the name of an entry at any depth, and a trailing `/` restricts the pattern to directories. Matching follows the case policy recorded for the root, and an invalid pattern is reported as `E_INVALID_PATTERN`.
- Exclusions cover explicit patterns, the artifacts this program produces (the database, its `-journal`/`-wal`/`-shm` companions and the `<database>.evernightindex.lock` file), the configured change-log directory and the working subdirectory of the current run. Only those explicit files and directories are excluded, so a temporary parent directory keeps indexing unrelated user files.
- Coverage evidence stays explicit. Successful enumeration, partial enumeration, enumeration failure, excluded entries and an invalid root are separate events, and the gaps are recorded as a prefix list. Only a successfully enumerated directory can later justify removing a missing entry; an excluded, failed or cross-file-system subtree never can. An unknown total is never turned into a percentage.
- Concurrency and cancellation are bounded: directory enumeration runs on at most `max(1, logical CPUs / 2)` workers (default 1) with bounded queues, one consumer hands entry batches to the caller and one consumer emits events. One unreadable entry or directory is recorded and the scan continues; cancellation stops new work and reports the result as incomplete.
- This step reads no ordinary file content. No SHA256 is computed and no file data is opened; a metadata handle on Windows asks for attributes only.

## Not implemented yet

`index` now creates a complete first index, and `info`, the statistics, the run lifecycle, the staging/publish boundary and the write lock are in place. Still missing: incremental and detailed update with digest reuse and conservative deletion rules (an existing index is refused for now), change-log CSV export and retry, typed filters, field selection and JSONL/CSV/NUL output, duplicate detection with `auto`/`metadata`/`sha256` plus on-demand verification, and temporary working copies with publish and recovery.

## Project layout

```text
cmd/evernightindex      entry point, version injection, two-stage interrupt, exit-code hand-over
internal/cli            option registry, parser, help, dispatch
internal/app            run lifecycle (lock, run record, publish or abandon), info report, output layer
internal/model          domain types, stable error codes and exit codes
internal/store          SQLite schema and migration, staging and publish, write lock, backup
internal/fsmeta         per-OS metadata collection, identity comparison, case-policy probe
internal/scan           traversal, exclusions, one-file-system scope, coverage events
internal/hash           streamed SHA256, stability checks before and after reading, bounded workers
internal/index          scan-to-stage conversion, digest decision, coverage gaps
internal/match          shared name/path wildcard matcher
internal/platform       per-OS facts (system locale) behind build tags
internal/output         stdout/stderr separation, verbosity, thread-safe statistics, per-second ticks
internal/i18n           four message catalogs, language resolution, catalog checks
```

## License and author

- License: Mulan Permissive Software License, Version 2 (`MulanPSL-2.0`); the full text is in [LICENSE](LICENSE).
- Copyright and required signature: `Copyright (c) KagurazakaMiyabi(KagurazakaYashi)`. Keep this exact form in redistributed notices.
- Third-party dependencies keep their own licenses: the direct dependency `modernc.org/sqlite` is BSD-3-Clause, `go list -m -all` lists every module, and `go.mod` pins the versions used here.
