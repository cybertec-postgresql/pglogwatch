---
title: pglogwatch — Standalone Zero-Allocation PostgreSQL Log Parser Module
version: 1.0
date_created: 2026-08-30
last_updated: 2026-08-30
owner: pgwatch maintainers (CYBERTEC PostgreSQL International GmbH)
tags: [tool, architecture, library, parser, performance, logs, golang]
---

# Introduction

This specification defines `pglogwatch`, a standalone, dependency-free Go module that parses
PostgreSQL server log files. It is extracted from the log-parsing code currently embedded in
pgwatch (`internal/reaper/logparser.go`, `logparser_local.go`, `logparser_remote.go`) and
re-implemented as a streaming, zero-allocation parser.

The module MUST be consumable by arbitrary Go applications, MUST parse all three PostgreSQL
log destinations (`stderr`, `csvlog`, `jsonlog`), and MUST demonstrate measurably higher
throughput and lower memory consumption than both `pgbadger` (Perl) and `pgweasel` (Rust) on
an identical corpus and workload.

pgwatch itself becomes the first consumer: `internal/reaper` is reduced to a thin adapter that
maps `pglogwatch` records onto the `server_log_event_counts` measurement.

---

## 1. Purpose & Scope

**Purpose**: Provide a reusable, embeddable, allocation-free PostgreSQL log parsing library —
plus a reference CLI and a benchmark harness that proves the performance claims.

**In scope**:

- A new Go module `github.com/cybertec-postgresql/pglogwatch` (root package `pglogwatch`).
- Streaming record-at-a-time parsing of `stderr`, `csvlog`, and `jsonlog` destinations.
- `log_line_prefix` auto-detection and explicit specification for the `stderr` format.
- Multi-line record assembly (`DETAIL:`, `HINT:`, `STATEMENT:`, `QUERY:`, `CONTEXT:` continuations;
  embedded newlines inside quoted CSV fields; multi-line JSON is out of scope, see below).
- Severity normalisation across the 10 `lc_messages` locales pgwatch currently supports.
- Log-source readers: single file, directory tail with rotation tracking, and `io.Reader`.
- Optional sub-packages for compressed input and for remote reads via `pg_read_file()`.
- A reference CLI binary `pglogwatch` with pgweasel-comparable subcommands.
- A reproducible benchmark harness comparing `pglogwatch` against `pgbadger` and `pgweasel`.
- Migration of `internal/reaper` in pgwatch onto the new module.

**Out of scope**:

- HTML/graphical report generation (pgbadger's primary output). The CLI emits text and JSON only.
- `syslog`, `logplex`, `rds`, `redshift`, and `pgbouncer` log formats (pgbadger supports these;
  they MAY be added post-1.0 and MUST NOT influence the 1.0 API).
- Query normalisation/fingerprinting beyond a documented, optional helper.
- Persisted state stores, databases, or long-running daemons inside the library.
- pgwatch's logrus-based emitting facility in `internal/log`, which is a separate concern and is
  explicitly NOT part of this module.

**Intended audience**: Go developers implementing the module; pgwatch maintainers performing the
integration; reviewers validating the performance claims.

**Assumptions**:

- Go toolchain version 1.26 or newer (matching pgwatch).
- Input logs are produced by PostgreSQL 12 through 18.
- Input encoding is UTF-8 or any ASCII-compatible single-byte encoding.

---

## 2. Definitions

| Term | Definition |
|---|---|
| **Allocation** | A Go heap allocation, as reported by `testing.B.ReportAllocs()` / `allocs/op`. |
| **Zero-allocation** | Steady-state parsing performs exactly 0 heap allocations per record after warm-up. Fixed one-time setup allocations (buffers, parser struct) are permitted and excluded. |
| **Record** | One logical PostgreSQL log event, possibly spanning multiple physical lines. |
| **Physical line** | Bytes between two newline delimiters in the input stream. |
| **Borrowed slice** | A `[]byte` that aliases the parser's internal buffer and is invalidated by the next parser advance. |
| **Steady state** | Parsing after the internal buffer has grown to accommodate the largest record seen so far. |
| **csvlog** | PostgreSQL `log_destination = 'csvlog'` output. |
| **jsonlog** | PostgreSQL `log_destination = 'jsonlog'` output (PostgreSQL 15 and newer). |
| **stderr** | PostgreSQL `log_destination = 'stderr'` output, prefixed per `log_line_prefix`. |
| **log_line_prefix** | PostgreSQL GUC controlling the `stderr` line prefix, built from percent-escapes. |
| **Prefix template** | A compiled representation of a `log_line_prefix` value used to parse `stderr` lines. |
| **Severity** | PostgreSQL error severity: `DEBUG1`..`DEBUG5`, `INFO`, `NOTICE`, `WARNING`, `ERROR`, `LOG`, `FATAL`, `PANIC`, plus the continuation labels `STATEMENT`, `DETAIL`, `HINT`, `QUERY`, `CONTEXT`. |
| **SQLSTATE** | PostgreSQL five-character error code, e.g. `42P01`. |
| **pgbadger** | Perl PostgreSQL log analyzer, <https://github.com/darold/pgbadger>. Baseline A. |
| **pgweasel** | Rust PostgreSQL log parser CLI, <https://github.com/kmoppel/pgweasel>. Baseline B. |
| **Corpus** | The fixed, versioned set of log files used for all comparative benchmarks. |
| **Reference machine** | The hardware/OS configuration defined in §6 on which the headline numbers are measured. |
| **GUC** | Grand Unified Configuration — a PostgreSQL server setting. |
| **RSS** | Resident set size, peak physical memory used by a process. |
| **NDJSON** | Newline-delimited JSON: one complete JSON object per line. |

---

## 3. Requirements, Constraints & Guidelines

### 3.1 Module & Packaging

- **PKG-001**: The module path MUST be `github.com/cybertec-postgresql/pglogwatch`, published from a
  dedicated repository, licensed BSD-3-Clause to match pgwatch.
- **PKG-002**: The root package `pglogwatch` MUST have **zero non-standard-library dependencies**.
  The `require` block in `go.mod` MUST be empty for the root module's non-test build.
- **PKG-003**: Test-only dependencies (`github.com/stretchr/testify`) MUST be declared in the
  root `go.mod` but MUST NOT be reachable from non-test code.
- **PKG-004**: Functionality requiring third-party dependencies MUST live in separate nested
  modules so that consumers never inherit them:

  | Nested module | Purpose | Permitted dependencies |
  |---|---|---|
  | `pglogwatch/pgremote` | Reading logs over a connection via `pg_ls_logdir()` / `pg_read_file()` | `github.com/jackc/pgx/v5` |
  | `pglogwatch/compress` | Transparent `.gz`, `.zst`, `.bz2`, `.xz` input | `klauspost/compress`, `ulikunitz/xz` |
  | `pglogwatch/cmd/pglogwatch` | Reference CLI | CLI flag / pretty-print libraries |
  | `pglogwatch/bench` | Comparative benchmark harness | benchmark tooling |

- **PKG-005**: The module MUST declare `go 1.26` and MUST build on `linux/amd64`,
  `linux/arm64`, `darwin/arm64`, and `windows/amd64`.
- **PKG-006**: The public API of the root package MUST be frozen at v1.0.0 and follow semantic
  versioning thereafter.
- **PKG-007**: `unsafe` MAY be used only for `unsafe.String` / `unsafe.Slice` no-copy conversions,
  MUST be confined to a single file `unsafe.go`, and MUST have a `//go:build purego` guarded
  safe fallback in `safe.go`.
- **PKG-008**: Because the name primes readers to expect a monitoring agent, the repository
  description and the root package doc comment MUST both lead with the phrase
  "zero-allocation PostgreSQL log parser", mentioning log watching and tailing only afterwards.
  The package doc comment MUST state the borrowed-slice lifetime contract (PERF-002) in its
  first paragraph.

### 3.2 Format Support

- **FMT-001**: The parser MUST support `csvlog` for PostgreSQL 12–18, detecting the column count
  variant automatically:

  | Columns | PostgreSQL versions | Trailing fields |
  |---|---|---|
  | 23 | 12 | ... `application_name` |
  | 24 | 13 | ... `application_name`, `backend_type` |
  | 26 | 14–18 | ... `backend_type`, `leader_pid`, `query_id` |

- **FMT-002**: The parser MUST support `jsonlog` (PostgreSQL 15+), keyed on the documented key
  names: `timestamp`, `user`, `dbname`, `pid`, `remote_host`, `remote_port`, `session_id`,
  `line_num`, `ps`, `session_start`, `vxid`, `txid`, `error_severity`, `state_code`, `message`,
  `detail`, `hint`, `internal_query`, `internal_position`, `context`, `statement`,
  `cursor_position`, `func_name`, `file_name`, `file_line_num`, `application_name`,
  `backend_type`, `leader_pid`, `query_id`. Absent keys MUST yield zero-valued fields, not errors.
- **FMT-003**: The parser MUST support `stderr` output with an arbitrary `log_line_prefix`,
  supporting every escape defined by PostgreSQL — `%a %u %d %r %h %b %p %P %t %m %n %i %e %c %l
  %s %v %x %q %Q %%` — including the padding form `%-5p` / `%5p`.
- **FMT-004**: When the `stderr` `log_line_prefix` is not supplied by the caller, the parser MUST
  auto-detect it by inspecting up to N leading lines (default 200, configurable) and selecting the
  highest-scoring candidate from a built-in template list plus a generic heuristic scanner.
  The result MUST be reported via `Parser.DetectedPrefix() string`.
- **FMT-005**: Format auto-detection between `csvlog`, `jsonlog`, and `stderr` MUST be performed
  from the first non-empty line: a leading `{` implies `jsonlog`; a leading ISO-8601 timestamp
  followed by a comma at a valid CSV column boundary implies `csvlog`; otherwise `stderr`.
  Detection MUST be overridable via explicit `Format` configuration.
- **FMT-006**: The parser MUST assemble multi-line records:
  - `csvlog`: newlines inside double-quoted fields MUST NOT terminate a record.
  - `stderr`: a physical line that does not match the prefix template MUST be appended to the
    previous record's message continuation region; recognised secondary labels
    (`DETAIL:`, `HINT:`, `STATEMENT:`, `QUERY:`, `CONTEXT:`) MUST populate the corresponding
    `Record` field rather than being emitted as separate records, unless
    `Config.SplitContinuations` is set.
  - `jsonlog`: PostgreSQL emits exactly one JSON object per physical line; the parser MUST NOT
    attempt multi-line JSON assembly.
- **FMT-007**: The parser MUST normalise localised severities to English using the locale tables
  already present in pgwatch (`C.`, `de`, `fr`, `it`, `ko`, `pl`, `ru`, `sv`, `tr`, `zh`),
  selected via `Config.MessagesLang`. An unknown locale MUST fall back to pass-through.
- **FMT-008**: Severity MUST be exposed as a `Severity uint8` enum with a `String()` method, and
  MUST be resolved without a map lookup or heap allocation.
- **FMT-009**: The parser MUST tolerate a truncated final record (no trailing newline) by
  returning it as a complete record at EOF when `Config.EmitTruncatedTail` is true (default),
  or discarding it when false.
- **FMT-010**: A line that cannot be parsed MUST NOT abort the stream. It MUST increment
  `Parser.Stats().Malformed` and, when `Config.OnMalformed` is set, invoke that callback with a
  borrowed slice of the offending bytes.

### 3.3 Zero-Allocation Requirements

- **PERF-001**: In steady state, `Parser.Next()` MUST perform **0 heap allocations per record**
  for all three formats, verified by `testing.AllocsPerRun` over the full corpus.
- **PERF-002**: All string-valued `Record` fields MUST be exposed as **borrowed** `[]byte`
  slices aliasing the parser's internal buffer. The API MUST document that they are invalid
  after the next `Next()` call.
- **PERF-003**: `Record` MUST provide `Clone() *OwnedRecord` for callers that need to retain a
  record. `Clone` is the only sanctioned allocation path and MUST perform at most 2 allocations
  (one backing byte array, one struct).
- **PERF-004**: The parser MUST NOT use `regexp` anywhere on the hot path. Field extraction MUST
  use hand-written byte scanners driven by `bytes.IndexByte` / `bytes.IndexAny`.
- **PERF-005**: The parser MUST NOT build a map per record. `Record` MUST be a flat struct.
- **PERF-006**: Integer fields MUST be parsed by an internal `parseUint` / `parseInt` over
  `[]byte`; `strconv.Atoi(string(b))` MUST NOT be used.
- **PERF-007**: Timestamps MUST be parsed by a fixed-layout digit scanner, not `time.Parse`.
  The timezone offset MUST be resolved once per distinct offset string and cached in the parser;
  `time.LoadLocation` MUST NOT be called on the hot path.
- **PERF-008**: The internal read buffer MUST be reused across records and MUST grow only when a
  record exceeds the current capacity. Growth MUST be amortised (doubling) and capped by
  `Config.MaxRecordBytes` (default 16 MiB); a record exceeding the cap MUST be skipped and
  counted, never panicked on.
- **PERF-009**: CSV fields containing escaped quotes (a doubled `"`) MUST NOT be unescaped
  eagerly. The parser MUST set `Record.Flags & FlagNeedsUnquote` and provide
  `AppendUnquoted(dst, src []byte) []byte` so the caller owns any allocation.
  The same rule applies to JSON string escapes (backslash-quote, backslash-backslash, and
  backslash-u sequences).
- **PERF-010**: Error values returned on the hot path MUST be preallocated sentinels.
  `fmt.Errorf` MUST NOT be called per record.
- **PERF-011**: The `Parser` MUST be reusable across files via `Reset(io.Reader)` without
  reallocating its buffer.
- **PERF-012**: `Parser` MUST NOT be safe for concurrent use by multiple goroutines; this MUST be
  documented. Concurrency is the caller's responsibility, or is provided by `pglogwatch.ParallelScan`
  (see IFC-008), which shards by file or by byte range at record boundaries.

### 3.4 Comparative Performance Requirements

All figures are measured on the reference machine (§6) against the versioned corpus (§6),
single process, warm page cache, output discarded.

- **PERF-020**: Single-core `csvlog` full-field parse throughput MUST be **≥ 250 MB/s**.
- **PERF-021**: Single-core `csvlog` severity-only scan throughput MUST be **≥ 800 MB/s**.
- **PERF-022**: Single-core `stderr` full-field parse throughput MUST be **≥ 200 MB/s**.
- **PERF-023**: Single-core `jsonlog` full-field parse throughput MUST be **≥ 150 MB/s**.
- **PERF-024**: For every benchmark workload in §6.4, `pglogwatch` MUST achieve **≥ 10× the throughput
  of `pgbadger -j 1`**. (Baseline reference: pgbadger's own documentation reports 9.5 GB in
  1 h 41 min on one CPU, approximately 1.6 MB/s.)
- **PERF-025**: For every benchmark workload in §6.4, `pglogwatch` MUST achieve **≥ 1.0× the
  throughput of `pgweasel`** (parity), with a **target of ≥ 1.2×**. Failing to reach 1.2× MUST
  NOT block release; failing parity MUST block release.
- **PERF-026**: Peak RSS for any streaming workload MUST be **O(1) in input size** and MUST NOT
  exceed **64 MiB** for a 10 GB input.
- **PERF-027**: Peak RSS MUST be **< 25 % of pgbadger's** and **≤ 1.25× of pgweasel's** on the
  same workload.
- **PERF-028**: Top-K aggregations MUST be O(K) in memory, not O(distinct queries).
- **PERF-029**: `pglogwatch.ParallelScan` MUST achieve ≥ 0.75× linear scaling up to 8 cores on a
  multi-file workload.
- **PERF-030**: A CI job MUST fail the build if any benchmark regresses more than **5 %** in
  ns/op, or gains **any** allocation, compared to the committed baseline, using `benchstat`.

### 3.5 Correctness & Robustness

- **COR-001**: Parsing MUST be lossless: every field present in the input MUST be retrievable
  from `Record` without truncation.
- **COR-002**: The parser MUST NOT panic on any input. This MUST be enforced by fuzz targets.
- **COR-003**: For a corpus generated with a known event sequence, `pglogwatch`'s per-severity counts
  MUST equal `pgbadger`'s and `pgweasel`'s counts exactly (AC-010).
- **COR-004**: `csvlog` and `jsonlog` inputs describing the same server activity MUST yield equal
  `Record` values for all shared fields.
- **COR-005**: Invalid UTF-8 MUST be passed through unchanged, not replaced or rejected.
- **COR-006**: CRLF line endings MUST be handled; a trailing carriage return MUST NOT appear in
  any field value.
- **COR-007**: The directory tailer MUST NOT double-count records across log rotation, including
  when `log_truncate_on_rotation` is on and a filename is reused.

### 3.6 Constraints

- **CON-001**: No cgo. The module MUST build with `CGO_ENABLED=0`.
- **CON-002**: No global mutable state; all state MUST live in `Parser` / `Config` values.
- **CON-003**: No logging from the library. Diagnostics MUST be surfaced via return values,
  `Stats()`, and optional callbacks.
- **CON-004**: The root package MUST NOT import `net`, `os/exec`, `database/sql`, or
  `encoding/json`.
- **CON-005**: The library MUST NOT read GUCs, connect to a server, or touch the filesystem
  outside of the explicitly requested reader constructors.
- **CON-006**: Public API surface at v1.0 MUST be small enough to review: target ≤ 40 exported
  identifiers in the root package.
- **CON-007**: pgwatch's existing `server_log_event_counts` measurement schema (per-severity
  lowercase `<severity>` and `<severity>_total` int64 columns) MUST remain byte-identical after
  migration, so existing dashboards and sinks continue to work.

### 3.7 Guidelines

- **GUD-001**: Prefer table-driven byte scanners with explicit bounds hints to help the compiler
  eliminate bounds checks; verify with `-gcflags=-d=ssa/check_bce/debug=1`.
- **GUD-002**: Keep the hot loop small enough to stay inlineable where profitable; verify with
  `-gcflags=-m`.
- **GUD-003**: Document escape-analysis-sensitive code with comments explaining why a value does
  not escape; regressions here are the usual cause of surprise allocations.
- **GUD-004**: Provide `iter.Seq2` convenience wrappers, but keep the low-level
  `Next()` / `Record()` / `Err()` API as the documented zero-allocation path.
- **GUD-005**: Follow the repository's `modern-go` guidance for Go 1.26+ idioms.
- **GUD-006**: Every performance claim in the README or release notes MUST be reproducible via
  `make bench-compare` and MUST cite the corpus version and reference machine.

### 3.8 Patterns

- **PAT-001**: **Borrow-by-default, clone-on-demand.** The parser hands out views into its buffer;
  retention is an explicit, caller-driven allocation.
- **PAT-002**: **Compile-then-scan.** `log_line_prefix` is compiled once into a prefix template
  (a slice of literal and escape segments); scanning is a linear walk over that template.
- **PAT-003**: **Deferred unescaping.** Quote and escape processing is a flag plus a helper,
  never eager work on the hot path.
- **PAT-004**: **Reader/Parser separation.** `io.Reader` sourcing (file, directory tail, remote,
  compressed) is entirely decoupled from record parsing.
- **PAT-005**: **Adapter at the boundary.** pgwatch keeps its `MeasurementEnvelope` construction
  in `internal/reaper`; `pglogwatch` knows nothing about pgwatch types.

---

## 4. Interfaces & Data Contracts

### 4.1 Core Types

```go
package pglogwatch

// Format identifies a PostgreSQL log destination.
type Format uint8

const (
    FormatAuto Format = iota // detect from input
    FormatStderr
    FormatCSV
    FormatJSON
)

// Severity is a PostgreSQL error severity, normalised to English.
type Severity uint8

const (
    SeverityUnknown Severity = iota
    SeverityDebug5
    SeverityDebug4
    SeverityDebug3
    SeverityDebug2
    SeverityDebug1
    SeverityLog
    SeverityInfo
    SeverityNotice
    SeverityWarning
    SeverityError
    SeverityFatal
    SeverityPanic
)

func (s Severity) String() string   // "ERROR"; "" for SeverityUnknown
func (s Severity) IsProblem() bool  // >= SeverityWarning
func ParseSeverity(b []byte) Severity

// Flags describes cheap facts about a Record discovered during scanning.
type Flags uint16

const (
    FlagNeedsUnquote Flags = 1 << iota // one or more fields contain escapes
    FlagMultiline                      // record spanned more than one physical line
    FlagTruncated                      // record hit MaxRecordBytes, or EOF mid-record
    FlagHasDuration                    // Duration is populated
    FlagHasStatement                   // Statement is populated
)
```

### 4.2 Record

All `[]byte` fields are **borrowed** and are invalidated by the next `Parser.Next()`.

```go
type Record struct {
    Time         time.Time // log_time / %m / %t / timestamp
    SessionStart time.Time

    Severity    Severity
    RawSeverity []byte // as it appeared, before locale normalisation

    User            []byte
    Database        []byte
    ConnectionFrom  []byte // host:port, or the socket path
    ApplicationName []byte
    BackendType     []byte
    CommandTag      []byte

    ProcessID      int32
    LeaderPID      int32
    SessionID      []byte // %c
    SessionLineNum int64
    VirtualXID     []byte
    TransactionID  int64
    QueryID        int64

    SQLState         [5]byte // zero value means absent
    Message          []byte
    Detail           []byte
    Hint             []byte
    Query            []byte // csvlog "query" / stderr STATEMENT:
    InternalQuery    []byte
    Context          []byte
    Statement        []byte
    QueryPos         int32
    InternalQueryPos int32
    Location         []byte // func:file:line for csvlog; split keys in jsonlog

    Duration time.Duration // parsed from "duration: N.NNN ms" when present

    Flags  Flags
    Offset int64  // byte offset of the record's first byte in the stream
    Raw    []byte // the complete borrowed record bytes
}

func (r *Record) Clone() *OwnedRecord
```

### 4.3 Parser

```go
type Config struct {
    Format             Format // default FormatAuto
    LinePrefix         string // stderr only; empty means auto-detect
    DetectLines        int    // default 200
    MessagesLang       string // "en", "de", ...; default "en"
    MaxRecordBytes     int    // default 16 << 20
    InitialBufferBytes int    // default 64 << 10
    SplitContinuations bool   // emit DETAIL/HINT/... as separate records
    EmitTruncatedTail  bool   // default true
    ParseDuration      bool   // scan Message for "duration: N ms"; default true
    OnMalformed        func(line []byte, err error)
}

type Stats struct {
    Records     int64
    Bytes       int64
    Malformed   int64
    Truncated   int64
    BufferGrows int64
}

type Parser struct{ /* unexported */ }

func New(r io.Reader, cfg Config) *Parser

func (p *Parser) Next() bool               // advance; false at EOF or on fatal error
func (p *Parser) Record() *Record           // valid until the next Next()
func (p *Parser) Err() error                // nil at clean EOF
func (p *Parser) Stats() Stats
func (p *Parser) DetectedFormat() Format
func (p *Parser) DetectedPrefix() string
func (p *Parser) Reset(r io.Reader)         // reuse buffers for a new stream
func (p *Parser) Seek(offset int64, whence int) (int64, error) // io.Seeker; resyncs to a boundary
```

- **IFC-001**: `Next()` MUST return `false` exactly once at end of input; further calls MUST keep
  returning `false` without side effects.
- **IFC-002**: `Record()` MUST return a stable pointer for the lifetime of the `Parser`; only the
  pointed-to contents change.
- **IFC-003**: `Err()` MUST return `nil` for clean EOF and MUST NOT report per-record malformed
  lines (those are counted in `Stats`).

### 4.4 Iterator Convenience API

```go
// All yields the same *Record pointer repeatedly; it does not allocate per record.
func (p *Parser) All() iter.Seq2[*Record, error]
```

- **IFC-004**: `All()` MUST NOT allocate per iteration and MUST be documented as yielding a
  borrowed, reused pointer.

### 4.5 Sources

```go
// FileSet resolves a directory plus glob into an ordered, rotation-aware sequence of readers.
type FileSet struct {
    Dir                string
    Glob               string // default "*.csv" / "*.log" / "*.json" by Format
    Follow             bool   // tail for new data and new files
    TruncateOnRotation bool
    PollInterval       time.Duration
    Offsets            OffsetStore // nil means in-memory, bounded
}

func (fs *FileSet) Open(ctx context.Context) (io.ReadCloser, error)

// OffsetStore persists per-file byte offsets so restarts neither re-read nor skip.
type OffsetStore interface {
    Get(path string) (offset int64, ok bool)
    Set(path string, offset int64)
}
```

- **IFC-005**: `FileSet.Open` MUST return a reader that transparently continues across rotation
  and MUST NOT emit a partially-written trailing record.
- **IFC-006**: Offsets MUST be **byte offsets**, not line counts. (The current pgwatch
  implementation re-reads and discards lines to resume, which is O(n) per restart; byte offsets
  make resumption O(1) and compose with the offset-based remote read path.)
- **IFC-008**: `Parser.Seek` MUST match the `io.Seeker` signature, so a `Parser` may be passed
  wherever that interface is accepted. `io.SeekCurrent` MUST resolve against the parser's own
  consumed offset rather than the underlying reader's position, which the buffer has read past.
  The returned offset MUST be the position resynchronisation settled on, so a caller seeking to
  an approximate position learns the real record boundary.
- **IFC-007**: The default in-memory `OffsetStore` MUST be bounded (default 2500 entries,
  matching pgwatch's `maxTrackedFiles`) with LRU eviction.

### 4.6 Parallel Scanning

```go
func ParallelScan(ctx context.Context, srcs []io.ReaderAt, cfg Config, workers int,
    fn func(worker int, r *Record) error) error
```

- **IFC-008**: `ParallelScan` MUST shard input at record boundaries and MUST call `fn` from
  `workers` goroutines, each with its own `Parser`. Ordering across workers is NOT guaranteed;
  this MUST be documented.

### 4.7 Remote Source (`pglogwatch/pgremote`)

```go
package pgremote

type Config struct {
    Dir       string
    Glob      string
    ChunkSize int64 // default 10 << 20, matching pgwatch's maxChunkSize
}

type Conn interface {
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func Open(ctx context.Context, conn Conn, cfg Config) (io.ReadCloser, error)
```

- **IFC-009**: `pgremote.Open` MUST use `pg_ls_logdir()` and `pg_read_file(path, offset, len)`
  and MUST never hand the parser a partial trailing record at a chunk boundary.

### 4.8 CLI Contract

Binary: `pglogwatch`. Subcommands mirror pgweasel to permit like-for-like benchmarking.

| Command | Behaviour |
|---|---|
| `pglogwatch errors [paths...]` | WARNING and above; histogram and top-N error messages |
| `pglogwatch slow [paths...]` | Statements above `--min-duration`; top-N slowest plus aggregate stats |
| `pglogwatch stats [paths...]` | Counts of errors, connections, checkpoints, autovacuums, temp files |
| `pglogwatch connections [paths...]` | Connection counts by database, user, application, client |
| `pglogwatch locks [paths...]` | Lock waits, deadlocks, recovery conflicts |
| `pglogwatch peaks [paths...]` | Busiest time buckets, default 10 minutes |
| `pglogwatch system [paths...]` | Server lifecycle and internal events |
| `pglogwatch grep <pattern> [paths...]` | Record-aware search with `-A` / `-B` / `-C` context |
| `pglogwatch parse [paths...]` | Emit every record as NDJSON (canonical machine output) |
| `pglogwatch bench [paths...]` | Parse and discard; report MB/s, ns/record, allocs, peak RSS |

Global flags: `--format`, `--line-prefix`, `--lang`, `--begin`, `--end`, `--jobs`,
`--output text|json`, `--no-color`.

- **IFC-010**: With no path arguments, the CLI MUST read from standard input.
- **IFC-011**: `--output json` MUST emit NDJSON, one object per result row, on stdout only; all
  diagnostics MUST go to stderr.
- **IFC-012**: Exit codes: `0` success, `1` usage or I/O error, `2` no input matched.

### 4.9 pgwatch Integration Contract

- **IFC-013**: `internal/reaper/logparser.go`, `logparser_local.go`, and `logparser_remote.go`
  MUST be replaced by a single adapter that:
  1. resolves the log configuration from server GUCs (the existing `tryDetermineLogSettings`
     query is retained),
  2. constructs a `pglogwatch.FileSet` (local) or a `pgremote` reader (remote),
  3. counts severities per database and per instance,
  4. emits the existing `metrics.MeasurementEnvelope` shape unchanged.
- **IFC-014**: The `log_destination must contain 'csvlog'` hard error MUST be removed. With
  `pglogwatch`, the `stderr` and `jsonlog` destinations MUST also be accepted.
- **IFC-015**: The `logging_collector is not enabled` error MUST be retained, since without the
  collector there are no files to read.

---

## 5. Acceptance Criteria

### Parsing correctness

- **AC-001**: Given a `csvlog` file produced by PostgreSQL 12, 13, 14, 15, 16, 17, and 18,
  When parsed with `Format: FormatAuto`, Then every record's field values equal the values
  obtained by loading the same file into a `postgres_log` table via `COPY` and reading it back.
- **AC-002**: Given a `jsonlog` file from PostgreSQL 15+, When parsed, Then every record equals
  the result of `encoding/json` unmarshalling the same line into the reference struct.
- **AC-003**: Given an `stderr` file with `log_line_prefix = '%m [%p] %q%u@%d '`, When parsed
  without an explicit `LinePrefix`, Then `DetectedPrefix()` returns that prefix and every record
  parses with `Severity != SeverityUnknown`.
- **AC-004**: Given an `stderr` record with `DETAIL:`, `HINT:`, and `STATEMENT:` continuation
  lines, When parsed with default config, Then exactly one `Record` is produced with `Detail`,
  `Hint`, and `Statement` populated and `FlagMultiline` set.
- **AC-005**: Given a `csvlog` record whose `message` field contains an embedded newline and a
  doubled quote, When parsed, Then one `Record` is produced, `FlagNeedsUnquote` is set, and
  `AppendUnquoted` reproduces the original text exactly.
- **AC-006**: Given a log with Russian `lc_messages` and severity `ОШИБКА`, When parsed with
  `MessagesLang: "ru"`, Then `Severity == SeverityError` and `RawSeverity` holds the original bytes.
- **AC-007**: Given an input line that matches no known format, When parsed, Then `Next()`
  continues to the following record, `Stats().Malformed` increments by 1, and `Err()` is nil.
- **AC-008**: Given a file whose last record lacks a trailing newline, When parsed with
  `EmitTruncatedTail: true`, Then that record is emitted with `FlagTruncated` set.
- **AC-009**: The parser shall not panic for any input, when that input is produced by the fuzz
  corpus after 10 million executions.
- **AC-010**: Given the reference corpus, When per-severity counts are produced by `pglogwatch stats`,
  `pgbadger`, and `pgweasel`, Then all three counts are identical for every severity.

### Zero allocation

- **AC-011**: Given the reference corpus, When parsed end-to-end after warm-up, Then
  `testing.AllocsPerRun(10, parseAll)` returns exactly `0` for each of `FormatCSV`,
  `FormatStderr`, and `FormatJSON`.
- **AC-012**: Given a `go test -bench . -benchmem` run, When results are inspected, Then every
  parsing benchmark reports `0 B/op` and `0 allocs/op`.
- **AC-013**: Given a record larger than `InitialBufferBytes`, When parsed, Then
  `Stats().BufferGrows` increases, and subsequent records of the same size allocate nothing.
- **AC-014**: Given `Config.MaxRecordBytes = 1024` and a 2 KiB record, When parsed, Then `Err()`
  is nil, the record is skipped, `Stats().Truncated` is 1, and no panic occurs.

### Comparative performance

- **AC-015**: Given the reference machine and corpus, When `make bench-compare` runs, Then
  `pglogwatch` single-core csvlog throughput is ≥ 250 MB/s.
- **AC-016**: Given the same run, Then `pglogwatch` throughput is ≥ 10× `pgbadger -j 1` for every
  workload in §6.4.
- **AC-017**: Given the same run, Then `pglogwatch` throughput is ≥ 1.0× `pgweasel` for every workload
  in §6.4.
- **AC-018**: Given a 10 GB input, When `pglogwatch bench` runs, Then peak RSS is < 64 MiB and is
  < 25 % of pgbadger's peak RSS on the same input.
- **AC-019**: Given a multi-file corpus and `--jobs 8` on an 8-core reference machine, When
  `pglogwatch` runs, Then throughput is ≥ 6× the `--jobs 1` throughput.
- **AC-020**: Given a pull request, When any benchmark regresses more than 5 % in ns/op or gains
  any allocation versus the committed baseline, Then the CI benchmark job fails.

### Packaging and integration

- **AC-021**: Given `go list -deps github.com/cybertec-postgresql/pglogwatch`, When executed, Then no
  non-standard-library package appears.
- **AC-022**: Given `CGO_ENABLED=0 GOOS=windows go build ./...`, When executed, Then the build
  succeeds. The same holds for the other three target platforms.
- **AC-023**: Given pgwatch migrated onto `pglogwatch`, When the existing `TestLogParser*` suite in
  `internal/reaper` runs, Then the emitted `MeasurementEnvelope` for `server_log_event_counts`
  is field-for-field identical to the pre-migration output for the same input.
- **AC-024**: Given a source configured with `log_destination = 'stderr'`, When pgwatch collects
  `server_log_event_counts`, Then counts are produced (previously this configuration errored out).
- **AC-025**: Given pgwatch restarts mid-file, When collection resumes, Then no record is counted
  twice and none is skipped, and resumption performs a single `Seek`, not a line-by-line re-read.

---

## 6. Test Automation Strategy

### 6.1 Test levels

| Level | Scope | Tooling |
|---|---|---|
| Unit | Byte scanners, prefix compiler, severity table, timestamp parser, unquoting | `testing`, `stretchr/testify` |
| Golden | Parse corpus files to NDJSON, diff against committed golden files | `testing` with an `-update` flag |
| Differential | `csvlog` vs `jsonlog` vs `stderr` of the same server activity must agree | `testing` |
| Oracle | `csvlog` parsed by `pglogwatch` vs the same file `COPY`-ed into `postgres_log` | container-backed integration job |
| Fuzz | `FuzzParseRecord`, `FuzzPrefixTemplate`, `FuzzUnquote` | native `testing.F` |
| Allocation | `testing.AllocsPerRun` gates | `testing` |
| Benchmark | ns/op, B/op, allocs/op, MB/s | `testing.B`, `benchstat` |
| Comparative | `pglogwatch` vs `pgbadger` vs `pgweasel` | `pglogwatch/bench` harness, `hyperfine`, `/usr/bin/time -v` |
| Integration | pgwatch reaper end-to-end | existing pgwatch suite plus `pgxmock` |

### 6.2 Test data management

- **TST-001**: A generator `pglogwatch/bench/gen` MUST synthesise reproducible corpora from a seed:
  a configurable mix of connection events, slow queries, errors with `DETAIL` / `HINT` /
  `STATEMENT`, autovacuum reports, checkpoint reports, deadlocks, and temp-file messages.
- **TST-002**: The generator MUST emit the same logical event stream in `stderr`, `csvlog`, and
  `jsonlog` form, for each supported PostgreSQL major version's column layout.
- **TST-003**: The corpus MUST be versioned (`corpus-v1`), described by a manifest recording seed,
  size, record count, and severity histogram, and MUST NOT be committed as raw log files — it is
  regenerated by `make corpus`.
- **TST-004**: Small hand-written fixtures for edge cases (embedded newlines, doubled quotes,
  invalid UTF-8, CRLF, localised severities, truncated tail, an 8 MiB single statement) MUST be
  committed under `testdata/`.
- **TST-005**: Real-world anonymised samples MAY be added; they MUST be scrubbed of identifiers
  before commit.

### 6.3 CI/CD integration

- **TST-006**: GitHub Actions MUST run, on every pull request: `go vet`, `staticcheck`,
  `go test -race ./...`, the allocation gate tests, and a build matrix across the four target
  platforms.
- **TST-007**: A nightly job MUST run each fuzz target for 30 minutes and file an issue on any new
  crasher.
- **TST-008**: A benchmark job MUST run on a dedicated, pinned self-hosted runner, compare against
  the committed baseline with `benchstat`, and fail per PERF-030.
- **TST-009**: The comparative job (with `pgbadger` and `pgweasel` installed at pinned versions)
  MUST run weekly and on release tags, publishing a results table as a build artifact.
- **TST-010**: Coverage MUST be ≥ 90 % of statements for the root package and ≥ 80 % overall.
  Coverage MUST NOT be satisfied by golden-file assertions alone.

### 6.4 Comparative benchmark definition

Workloads, each run against the identical corpus file set:

| ID | Workload | pglogwatch | pgweasel | pgbadger |
|---|---|---|---|---|
| W1 | Parse and discard | `pglogwatch bench` | nearest parse-only equivalent | `pgbadger -o /dev/null` |
| W2 | Severity histogram | `pglogwatch stats` | `pgweasel stats` | pgbadger error section |
| W3 | Errors report | `pglogwatch errors` | `pgweasel errors` | pgbadger |
| W4 | Top slow queries | `pglogwatch slow` | `pgweasel slow` | pgbadger |
| W5 | Parallel, 8 files | `pglogwatch --jobs 8` | `pgweasel` as supported | `pgbadger -J 8` |

- **TST-011**: Each workload MUST be run at least 10 times via `hyperfine --warmup 3`; the
  reported figure is the median wall-clock time and the maximum peak RSS.
- **TST-012**: Because pgbadger's default output is a full HTML report, W1–W4 MUST document the
  pgbadger configuration used to make the comparison as fair as possible, and the results table
  MUST state explicitly what each tool produced. Unequal outputs MUST NOT be presented as equal.
- **TST-013**: The results table MUST record: tool version, corpus version, machine spec, format,
  input size, wall-clock, MB/s, peak RSS, and output artifact size.
- **TST-014**: The reference machine specification MUST be pinned in `bench/MACHINE.md` (CPU model,
  core count, RAM, kernel, filesystem, Go version, Perl version, Rust version) and MUST be
  reproduced verbatim in any published benchmark claim.

---

## 7. Rationale & Context

### 7.1 Why extract

The parsing logic currently lives inside `internal/reaper`, entangled with pgwatch's
`sources.DbConn`, `metrics.MeasurementEnvelope`, and the logrus logger. It is therefore unusable
outside pgwatch, hard to test in isolation, and its performance characteristics are invisible.
PostgreSQL log parsing is a generally useful capability; a standalone module makes it reusable and
makes its cost measurable.

### 7.2 Why the current implementation is slow

The existing implementation has four structural allocation sources per line:

1. `regexp.FindStringSubmatch` allocates a `[]string` of submatches per line
   (`logparser_local.go`, `logparser_remote.go`).
2. `regexMatchesToMap` allocates a `map[string]string` per line and populates it with sub-slices
   — for a record where only two fields (`error_severity`, `database_name`) are ever read.
3. `bufio.Reader.ReadString('\n')` allocates a new `string` per line.
4. The remote path calls `strings.Split(chunk, "\n")` on a 10 MB chunk, allocating a large
   `[]string` plus one string per line.

The regex itself (`csvLogDefaultRegEx`) uses non-greedy groups with optional quote handling, which
is close to the slowest possible way to split a CSV line, and it covers only the first 12 of the
23–26 columns — so the parser is simultaneously slow and incomplete. Resumption after a restart
re-reads and discards lines one at a time instead of seeking to a byte offset.

Removing all four allocation sources and replacing regex with a hand-written scanner is where the
order-of-magnitude improvement comes from; the remaining gains come from avoiding `time.Parse` and
`strconv`, and from bounds-check elimination in the scan loops.

### 7.3 Why borrowed slices rather than strings

Returning `string` fields forces a copy per field per record — the single largest unavoidable
allocation cost in any naive design. Borrowing from the read buffer eliminates it entirely, at the
cost of a lifetime contract the caller must respect. This is the same trade-off made by
`bufio.Scanner.Bytes()` and by `encoding/csv`'s `ReuseRecord`, so it is idiomatic and familiar.
`Clone()` provides the escape hatch, and the distinct `OwnedRecord` type makes retention explicit
in the type system rather than only in documentation.

### 7.4 Why zero third-party dependencies in the root package

A log parser is the kind of utility that gets embedded in agents, sidecars, and operator tooling
where dependency weight and supply-chain surface matter. Splitting pgx, compression, and CLI
concerns into nested modules means a consumer that only needs parsing pulls in nothing at all. It
also keeps pgwatch's own dependency graph from growing during the migration.

### 7.5 Why these specific performance thresholds

pgbadger's documented figure — 9.5 GB in 1 h 41 min on one CPU — is approximately 1.6 MB/s. A Go
scanner that avoids allocation and regex operates in the hundreds of MB/s, so the 10× floor in
PERF-024 is conservative by roughly two orders of magnitude; it exists to catch catastrophic
regressions, not to represent the expected result.

pgweasel is Rust and is itself designed for speed, with a stated goal of being an order of
magnitude faster than pgbadger. Requiring only **parity** (PERF-025) rather than a large margin is
deliberate: a Go implementation beating an optimised Rust one is not guaranteed, and writing an
unachievable requirement into the specification would make the specification useless. Parity is
the release gate; 1.2× is the stated target. If parity cannot be achieved, the gap and its cause
MUST be documented rather than the threshold quietly lowered (VAL-010).

### 7.6 Why byte offsets replace line counts

`logparser_local.go` resumes by calling `reader.ReadString('\n')` in a loop `linesRead` times. On
a large rotated file after a pgwatch restart, this reads and discards the whole file. Byte offsets
make the same operation a single `Seek`, and they compose with the remote
`pg_read_file(path, offset, len)` path, which is already offset-based.

### 7.7 Why format auto-detection matters for pgwatch

pgwatch today refuses to parse logs unless `log_destination` contains `csvlog`. That is a
significant deployment obstacle: `stderr` is the default, and many managed providers do not allow
changing it. Supporting all three destinations removes the precondition and widens where the
`server_log_event_counts` metric can be collected at all.

### 7.8 Why the name

`pglogwatch` was chosen over a neutral alternative for three reasons. It places the module in the
pgwatch family under the same organisation, which is where its maintenance backing actually comes
from. It is accurate rather than merely decorative: `FileSet{Follow: true}` with rotation tracking
is a log watcher, and that is the mode pgwatch itself consumes. And keeping the import path and
the package identifier identical avoids a `gopkg.in/yaml.v3`-style path/name mismatch, which costs
readers more at every call site than a shorter identifier would save them.

The cost is that "watch" primes readers to expect an agent or a daemon, which this module
explicitly is not (CON-003, CON-005). PKG-008 compensates by requiring the repository description
and the package doc to lead with "parser", so that search intent is satisfied by the first line a
prospective user reads.

`pglogwatch` shares its first five characters with `jackc/pglogrepl`, an unrelated
logical-replication library in the same ecosystem. This is a known and accepted minor confusion
risk, not an oversight.

### 7.9 Why the CLI exists

The performance requirements in §3.4 compare against two CLI tools. Without a comparable CLI there
is no way to run a like-for-like benchmark, and no way for a reviewer to reproduce a published
claim. The CLI is therefore part of the proof obligation, not only a convenience — which is why
its subcommand set deliberately mirrors pgweasel's.

---

## 8. Dependencies & External Integrations

### External Systems

- **EXT-001**: PostgreSQL server, versions 12–18 — produces the log files being parsed; queried
  only by `pglogwatch/pgremote` and by pgwatch's GUC-detection code, never by the root package.

### Third-Party Services

- **SVC-001**: GitHub Actions — CI for unit, fuzz, and cross-platform jobs.
- **SVC-002**: A pinned self-hosted benchmark runner — required because shared CI runners have too
  much variance for a 5 % regression gate.

### Infrastructure Dependencies

- **INF-001**: A dedicated public repository under the `cybertec-postgresql` organisation, with
  the same branch protection and release conventions as pgwatch.
- **INF-002**: A benchmark reference machine with a fixed CPU model, pinned CPU governor, boost
  variability disabled where possible, and at least 32 GB RAM so the corpus fits in page cache.
- **INF-003**: `pgbadger` and `pgweasel` installed at pinned versions on the benchmark runner.

### Data Dependencies

- **DAT-001**: Reference corpus — generated, not vendored; regenerated deterministically from a
  seed via `make corpus`. The manifest is committed; the payload is not.
- **DAT-002**: Committed edge-case fixtures under `testdata/`, 1 MB total or less.
- **DAT-003**: PostgreSQL `csvlog` column layouts and `jsonlog` key names per major version,
  sourced from the PostgreSQL documentation and encoded as tables in the module.

### Technology Platform Dependencies

- **PLT-001**: Go 1.26 or newer — required for `iter.Seq2` range-over-func and current
  `unsafe.String` semantics; matches the toolchain pgwatch is on.
- **PLT-002**: `CGO_ENABLED=0` builds on `linux/amd64`, `linux/arm64`, `darwin/arm64`, and
  `windows/amd64`.
- **PLT-003**: Perl runtime — benchmark runner only, for pgbadger.
- **PLT-004**: Rust toolchain or a prebuilt binary — benchmark runner only, for pgweasel.

### Compliance Dependencies

- **COM-001**: BSD-3-Clause licence, matching pgwatch, to permit unrestricted embedding.
- **COM-002**: Log records frequently contain personal data (usernames, client IP addresses, query
  parameters). The library MUST NOT persist, transmit, or log record contents anywhere; any such
  handling is the consumer's responsibility. Committed fixtures MUST be scrubbed.

---

## 9. Examples & Edge Cases

### 9.1 Minimal zero-allocation consumption

```go
p := pglogwatch.New(f, pglogwatch.Config{}) // FormatAuto
var errCount int
for p.Next() {
    r := p.Record()
    if r.Severity >= pglogwatch.SeverityError {
        errCount++
    }
}
if err := p.Err(); err != nil {
    return err
}
// 0 allocations in this loop.
```

### 9.2 Retaining a record

```go
var worst *pglogwatch.OwnedRecord
for p.Next() {
    r := p.Record()
    if r.Duration > threshold {
        worst = r.Clone() // the ONLY allocation; r itself is invalid after the next Next()
    }
}
```

### 9.3 Iterator form

```go
for r, err := range p.All() {
    if err != nil {
        return err
    }
    _ = r.Message // still borrowed, still zero-allocation
}
```

### 9.4 Explicit stderr prefix

```go
p := pglogwatch.New(f, pglogwatch.Config{
    Format:     pglogwatch.FormatStderr,
    LinePrefix: "%m [%p] %q%u@%d ",
})
```

### 9.5 Deferred unquoting

```go
var buf []byte // reused across records by the caller
for p.Next() {
    r := p.Record()
    msg := r.Message
    if r.Flags&pglogwatch.FlagNeedsUnquote != 0 {
        buf = pglogwatch.AppendUnquoted(buf[:0], r.Message)
        msg = buf
    }
    use(msg)
}
```

### 9.6 pgwatch adapter sketch

```go
// internal/reaper: the whole of logparser_local.go and logparser_remote.go collapses to this.
for p.Next() {
    r := p.Record()
    sev := r.Severity.String()
    if bytes.Equal(r.Database, realDbname) {
        lp.eventCounts[sev]++
    }
    lp.eventCountsTotal[sev]++
    if lp.HasSendIntervalElapsed() {
        select {
        case <-ctx.Done():
            return nil
        case lp.StoreCh <- lp.GetMeasurementEnvelope():
            zeroEventCounts(lp.eventCounts)
            zeroEventCounts(lp.eventCountsTotal)
            lp.lastSendTime = time.Now()
        }
    }
}
```

### 9.7 Edge cases the implementation MUST handle

| # | Input | Required behaviour |
|---|---|---|
| E1 | csvlog field containing a doubled quote | one record, `FlagNeedsUnquote` set, no eager unescape |
| E2 | csvlog field containing a raw newline inside quotes | one record, `FlagMultiline` set |
| E3 | csvlog field containing a comma inside quotes | field boundary not split |
| E4 | 26-column csvlog line where `leader_pid` and `query_id` are empty | `LeaderPID == 0`, `QueryID == 0`, no error |
| E5 | stderr prefix contains `%q` and the record is from a background worker | segments after `%q` are absent; parse succeeds |
| E6 | stderr prefix with padding, e.g. `%-5p` | width honoured, `ProcessID` correct |
| E7 | stderr continuation line beginning with whitespace (wrapped statement) | appended to the previous field, no new record |
| E8 | jsonlog line missing optional keys entirely | absent fields zero-valued, no error |
| E9 | jsonlog string containing backslash escapes or a `\u` sequence | passed through as escaped bytes; unescaped only on demand |
| E10 | CRLF line endings throughout | no trailing carriage return in any field |
| E11 | Invalid UTF-8 in `message` | bytes passed through unchanged |
| E12 | Localised severity `ПРЕДУПРЕЖДЕНИЕ` with `MessagesLang: "ru"` | `SeverityWarning` |
| E13 | Localised severity with the wrong `MessagesLang` | `SeverityUnknown`, record still emitted, `Malformed` not incremented |
| E14 | File truncated mid-record (rotation race) | truncated record flagged, next file resumed cleanly |
| E15 | Rotation reusing the same filename with `log_truncate_on_rotation = on` | detected via size shrink or inode change; offset reset to 0 |
| E16 | Empty file | `Next()` returns false immediately, `Err()` nil |
| E17 | File containing only a byte-order mark | BOM consumed, `Next()` returns false, `Err()` nil |
| E18 | Single record larger than `MaxRecordBytes` | skipped, `Stats().Truncated` incremented, stream continues |
| E19 | Timestamp with a non-integral-hour offset, e.g. `+05:30` | correct `time.Time`, zone cached |
| E20 | Timestamp using a zone abbreviation, e.g. `2026-08-30 10:00:00.123 CEST` | parsed via a cached abbreviation table; unknown abbreviation falls back to UTC without incrementing `Malformed` |

### 9.8 Anti-patterns

```go
// WRONG: retains a borrowed slice past the next Next().
var msgs [][]byte
for p.Next() {
    msgs = append(msgs, p.Record().Message) // every entry aliases the same buffer
}

// WRONG: reintroduces a per-record allocation.
for p.Next() {
    m := string(p.Record().Message) // allocates
}

// RIGHT:
var owned []*pglogwatch.OwnedRecord
for p.Next() {
    owned = append(owned, p.Record().Clone())
}
```

---

## 10. Validation Criteria

A release of `pglogwatch` v1.0.0 is compliant when all of the following hold:

- **VAL-001**: All acceptance criteria AC-001 through AC-025 pass in CI.
- **VAL-002**: `go list -deps` on the root package yields only standard-library packages (AC-021).
- **VAL-003**: `go test -bench . -benchmem ./...` reports `0 allocs/op` for every parsing
  benchmark, on all four target platforms.
- **VAL-004**: The comparative benchmark table (§6.4) is published in the release notes, cites
  `bench/MACHINE.md` and the corpus version, and shows every PERF-024 and PERF-025 threshold met.
- **VAL-005**: Fuzz targets have accumulated at least 10 million executions with no crashers.
- **VAL-006**: Statement coverage is ≥ 90 % for the root package and ≥ 80 % overall.
- **VAL-007**: The root package exports 40 or fewer identifiers and every one has a doc comment.
- **VAL-008**: pgwatch `master` builds against the released `pglogwatch` with
  `internal/reaper/logparser_local.go` and `logparser_remote.go` deleted, and the full pgwatch
  test suite passes.
- **VAL-009**: A pgwatch source with `log_destination = 'stderr'` produces non-zero
  `server_log_event_counts` in an end-to-end run.
- **VAL-010**: Where a threshold is not met, the release notes state the measured value, the
  cause, and the remediation plan. Silently relaxing a threshold in this specification is
  non-compliant.

---

## 11. Related Specifications / Further Reading

- [`spec/architecture-prometheus-exporter-source.md`](architecture-prometheus-exporter-source.md) — precedent for adding a capability at the pgwatch source boundary.
- [`spec/design-source-failure-resilience.md`](design-source-failure-resilience.md) — error-handling conventions in the reaper.
- [`spec/refactor-sourceconn-interface.md`](refactor-sourceconn-interface.md) — the `sources.DbConn` boundary the adapter must respect.
- [PostgreSQL: Error Reporting and Logging](https://www.postgresql.org/docs/current/runtime-config-logging.html) — `log_line_prefix` escapes, csvlog columns, jsonlog keys.
- [PostgreSQL: Using CSV-Format Log Output](https://www.postgresql.org/docs/current/runtime-config-logging.html#RUNTIME-CONFIG-LOGGING-CSVLOG) — the canonical `postgres_log` table definition used as the parsing oracle.
- [pgbadger](https://github.com/darold/pgbadger) — baseline A; feature reference for report content.
- [pgweasel](https://github.com/kmoppel/pgweasel) — baseline B; CLI subcommand reference.
- [Go: `iter` package](https://pkg.go.dev/iter) — range-over-func semantics used by `Parser.All`.
- [Go: `unsafe.String`](https://pkg.go.dev/unsafe#String) — no-copy byte-to-string conversion contract.
