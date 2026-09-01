---
description: "Task list for pglogwatch — standalone zero-allocation PostgreSQL log parser module"
---

# Tasks: pglogwatch — Standalone Zero-Allocation PostgreSQL Log Parser Module

**Input**: `tool-pglogwatch-zero-alloc-parser.md` (spec v1.0, 2026-08-30)
**Prerequisites**: spec.md (supplies requirements PKG/FMT/PERF/COR/CON/IFC, acceptance criteria AC-001..AC-025, validation criteria VAL-001..VAL-010)

**Tests**: Tests ARE included. §6 of the spec mandates unit, golden, differential, oracle, fuzz, allocation, benchmark, comparative and integration levels, and VAL-006 gates coverage at ≥ 90 % / ≥ 80 %.

**Organization**: Tasks are grouped by user story. Each story maps to a coherent capability of the module and can be implemented, tested and demoed on its own.

**Repository roots**:

- `pglogwatch/` — the new module `github.com/cybertec-postgresql/pglogwatch`
- `pgwatch/` — the existing pgwatch repository (US10 only)

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1..US10)
- Requirement IDs in parentheses trace back to the spec

---

## User Story Map

| Story | Title | Priority | Delivers |
|---|---|---|---|
| US1 | csvlog zero-allocation parsing | P1 🎯 MVP | FMT-001, PERF-001..011, AC-001, AC-005, AC-011..014 |
| US2 | stderr parsing with `log_line_prefix` | P2 | FMT-003, FMT-004, FMT-006, AC-003, AC-004 |
| US3 | jsonlog parsing | P3 | FMT-002, AC-002, E8, E9 |
| US4 | Format auto-detection, resync & robustness | P4 | FMT-005, FMT-009, FMT-010, COR-002, COR-005/006, AC-007..009 |
| US5 | Log sources: file, directory tail, rotation, byte offsets | P5 | IFC-005..007, COR-007, AC-025 |
| US6 | Parallel scanning | P6 | IFC-008, PERF-029, AC-019 |
| US7 | Nested modules: `compress`, `pgremote` | P7 | PKG-004, IFC-009 |
| US8 | Reference CLI `pglogwatch` | P8 | IFC-010..012, §4.8 |
| US9 | Benchmark harness & comparative proof | P9 | PERF-020..030, TST-001..014, AC-015..018, AC-020 |
| US10 | pgwatch migration onto pglogwatch | P10 | IFC-013..015, CON-007, AC-023, AC-024, VAL-008, VAL-009 |

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Repository, module layout, toolchain and CI skeleton

- [x] T001 Create the public repository `cybertec-postgresql/pglogwatch` with pgwatch's branch protection and release conventions (INF-001); add `LICENSE` (BSD-3-Clause, COM-001) and a `README.md` whose first line reads "zero-allocation PostgreSQL log parser" (PKG-008)
- [x] T002 Create root `go.mod` declaring `module github.com/cybertec-postgresql/pglogwatch` and `go 1.24` with an empty non-test `require` block (PKG-001, PKG-002, PKG-005, PLT-001)
- [x] T003 [P] Add `github.com/stretchr/testify` as a test-only dependency in the root `go.mod` (PKG-003)
- [x] T004 [P] Create nested module skeletons with their own `go.mod`: `compress/`, `pgremote/`, `cmd/pglogwatch/`, `bench/` (PKG-004)
- [x] T005 [P] Configure linting and formatting in `.golangci.yml` (`go vet`, `staticcheck`, `gofumpt`) and add `Makefile` targets `test`, `test-race`, `cover`, `fuzz`, `bench`, `bench-compare`, `corpus`, `lint`
- [x] T006 [P] Create `.github/workflows/ci.yml`: `go vet`, `staticcheck`, `go test -race ./...`, and a `CGO_ENABLED=0` build matrix over `linux/amd64`, `linux/arm64`, `darwin/arm64`, `windows/amd64` (TST-006, CON-001, PLT-002, AC-022)
- [x] T007 [P] Create `doc.go`: package doc leading with "zero-allocation PostgreSQL log parser", stating the borrowed-slice lifetime contract in the first paragraph, and documenting that `Parser` is not goroutine-safe (PKG-008, PERF-002, PERF-012)
- [x] T008 [P] Create `testdata/` and add `.gitattributes` marking `testdata/**` as `-text` so CRLF and invalid-UTF-8 fixtures stay byte-exact (COR-005, COR-006, DAT-002)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The scan primitives, tables and parser scaffolding every format parser is built on

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

### Core types and configuration

- [x] T009 [P] Define the `Format` enum (`FormatAuto`, `FormatStderr`, `FormatCSV`, `FormatJSON`) in `format.go` (§4.1)
- [x] T010 [P] Define the `Flags` bitfield (`FlagNeedsUnquote`, `FlagMultiline`, `FlagTruncated`, `FlagHasDuration`, `FlagHasStatement`) in `flags.go` (§4.1)
- [x] T011 [P] Define `Config` with all defaults and a `normalize()` applying `DetectLines=200`, `MessagesLang="en"`, `MaxRecordBytes=16<<20`, `InitialBufferBytes=64<<10`, `EmitTruncatedTail=true`, `ParseDuration=true` in `config.go` (§4.3)
- [x] T012 [P] Define `Stats` (`Records`, `Bytes`, `Malformed`, `Truncated`, `BufferGrows`) in `stats.go` (§4.3)
- [x] T013 [P] Define preallocated sentinel errors (`ErrRecordTooLarge`, `ErrBadPrefix`, `ErrMalformedLine`, …) in `errors.go`; no `fmt.Errorf` on the hot path (PERF-010)
- [x] T014 Define `Record` as a flat struct with borrowed `[]byte` fields exactly per §4.2 in `record.go` (PERF-002, PERF-005)
- [x] T015 Define `OwnedRecord` and `(*Record).Clone() *OwnedRecord` in `owned.go` using a single backing byte array plus one struct (PERF-003) — depends on T014

### Scan primitives

- [x] T016 [P] Implement `unsafe.go` (`unsafe.String` / `unsafe.Slice` no-copy conversions) and the `//go:build purego` fallback `safe.go` (PKG-007)
- [x] T017 [P] Implement `num.go`: `parseUint([]byte) (uint64, bool)` and `parseInt([]byte) (int64, bool)` over bytes, never `strconv.Atoi(string(b))` (PERF-006)
- [x] T018 [P] Implement `timestamp.go`: fixed-layout digit scanner for `YYYY-MM-DD HH:MM:SS[.fff]` covering both the `%t` and `%m` forms (PERF-007)
- [x] T019 Implement `tzcache.go`: per-parser cache resolving numeric offsets (`+05:30`, E19) and zone abbreviations (`CEST`, E20) exactly once; unknown abbreviation falls back to UTC without incrementing `Malformed`; never call `time.LoadLocation` on the hot path (PERF-007) — depends on T018
- [x] T020 [P] Implement `scan.go`: byte-scanner helpers over `bytes.IndexByte` / `bytes.IndexAny` with explicit bounds hints; no `regexp` anywhere (PERF-004, GUD-001)
- [x] T021 [P] Implement `buf.go`: reusable read buffer with amortised doubling growth, `MaxRecordBytes` cap, `BufferGrows` accounting, and record-skip (not panic) when the cap is exceeded (PERF-008, PERF-011, E18)
- [x] T022 [P] Implement `unquote.go`: `AppendUnquoted(dst, src []byte) []byte` handling doubled CSV quotes and JSON `\"`, `\\`, `\uXXXX` escapes; never eager on the hot path (PERF-009, PAT-003)
- [x] T023 [P] Implement `severity.go`: `Severity` enum, `String()`, `IsProblem()` and `ParseSeverity([]byte) Severity` resolved by a length/branch table with no map lookup and no allocation (FMT-008, §4.1)
- [x] T024 Implement `severity_locales.go`: severity tables for `C.`, `de`, `fr`, `it`, `ko`, `pl`, `ru`, `sv`, `tr`, `zh` ported from pgwatch, selected by `Config.MessagesLang`, unknown locale passes through (FMT-007, E12, E13) — depends on T023

### Parser scaffolding

- [x] T025 Implement the `Parser` struct and `New(r io.Reader, cfg Config) *Parser` in `parser.go`, wiring buffer, config, stats and the single reused `Record` (§4.3, IFC-002) — depends on T011, T014, T021
- [x] T026 Implement `Next() bool`, `Record() *Record`, `Err() error`, `Stats() Stats`, `DetectedFormat()`, `DetectedPrefix()`, `Reset(io.Reader)` as a format-dispatching skeleton (IFC-001, IFC-003, PERF-011) — depends on T025
- [x] T027 [P] Implement `iter.go`: `(*Parser).All() iter.Seq2[*Record, error]`, documented as yielding a borrowed, reused pointer with no per-iteration allocation (IFC-004, GUD-004)
- [x] T028 [P] Add the `internal/allocs` test helper wrapping `testing.AllocsPerRun` plus a zero assertion, used by every allocation gate (PERF-001, AC-011)
- [x] T029 [P] Add `import_test.go` asserting via `go/packages` that the root package imports no `net`, `os/exec`, `database/sql` or `encoding/json`, and no non-stdlib package (CON-004, PKG-002, AC-021)
- [x] T030 [P] Add `api_test.go` counting exported identifiers in the root package, failing above 40, and asserting every exported identifier has a doc comment (CON-006, VAL-007)
- [x] T031 [P] Unit tests for `num.go`, `timestamp.go`, `tzcache.go`, `severity.go`, `severity_locales.go`, `unquote.go`, `buf.go` in the matching `*_test.go` files, each with an allocation gate (§6.1 Unit)
- [x] T032 [P] Commit hand-written edge-case fixtures under `testdata/`: doubled quotes, embedded newline, embedded comma, CRLF, invalid UTF-8, localised severities, truncated tail, BOM-only file, empty file, 8 MiB single statement (TST-004, DAT-002, E1–E3, E10, E11, E16, E17)

**Checkpoint**: Scan primitives, tables and the `Parser` shell exist and are unit-tested at 0 allocs/op — format work can begin

---

## Phase 3: User Story 1 - csvlog zero-allocation parsing (Priority: P1) 🎯 MVP

**Goal**: Parse PostgreSQL `csvlog` for versions 12–18 at ≥ 250 MB/s with 0 allocations per record, exposing every column as a borrowed slice.

**Independent Test**: `go test -run TestCSV -bench BenchmarkCSV -benchmem ./...` parses the committed csvlog fixtures, matches the golden NDJSON, and reports `0 B/op 0 allocs/op`.

### Tests for User Story 1 ⚠️

> Write these first; they MUST fail before implementation.

- [x] T033 [P] [US1] Column-layout table test in `csv_layout_test.go`: 23-column (PG12), 24-column (PG13) and 26-column (PG14–18) variants detected automatically (FMT-001)
- [x] T034 [P] [US1] Golden test in `golden_test.go` with an `-update` flag: parse `testdata/csv/*.csv` to NDJSON and diff against `testdata/golden/*.ndjson` (§6.1 Golden)
- [x] T035 [P] [US1] Edge-case test in `csv_edge_test.go` covering E1 (doubled quote), E2 (raw newline in quotes), E3 (comma in quotes), E4 (empty `leader_pid` / `query_id`) and AC-005
- [x] T036 [P] [US1] Allocation gate in `alloc_csv_test.go`: `testing.AllocsPerRun(10, parseAllCSV) == 0` (AC-011, PERF-001)
- [x] T037 [P] [US1] Buffer tests in `buf_growth_test.go`: `BufferGrows` increments once then stays flat for same-size records (AC-013); `MaxRecordBytes=1024` with a 2 KiB record yields `Err()==nil`, `Truncated==1`, no panic (AC-014, E18)
- [x] T038 [P] [US1] Benchmarks `BenchmarkCSVFullParse` and `BenchmarkCSVSeverityOnly` in `bench_csv_test.go` reporting MB/s and allocs (PERF-020, PERF-021, AC-012)

### Implementation for User Story 1

- [x] T039 [US1] Implement CSV record framing in `csv.go`: split records on newlines outside double quotes, setting `FlagMultiline` for embedded newlines (FMT-006, E2)
- [x] T040 [US1] Implement the quote-aware column splitter in `csv.go`: field boundaries, `FlagNeedsUnquote` on a doubled quote, no eager unescaping (PERF-009, E1, E3)
- [x] T041 [US1] Implement column-count detection and the version→layout table in `csv_layout.go` (FMT-001, DAT-003)
- [x] T042 [US1] Map columns onto `Record` fields for all three layouts, including `SQLState [5]byte`, `Location`, `LeaderPID` and `QueryID` (COR-001, §4.2) — depends on T041
- [x] T043 [US1] Wire timestamp, severity and integer parsing into the CSV path using the T017–T024 primitives (PERF-006, PERF-007, FMT-007, FMT-008)
- [x] T044 [US1] Implement `duration: N.NNN ms` scanning over `Message` when `Config.ParseDuration` is set, populating `Duration` and `FlagHasDuration` (§4.2, §4.3)
- [x] T045 [US1] Populate `Record.Offset` and `Record.Raw` for every record (§4.2)
- [x] T046 [US1] Strip a trailing carriage return from every field for CRLF input (COR-006, E10)
- [x] T047 [US1] Bounds-check-elimination and inlining pass on the CSV hot loop; verify with `-gcflags=-d=ssa/check_bce/debug=1` and `-gcflags=-m`, documenting escape-analysis-sensitive spots (GUD-001, GUD-002, GUD-003)
- [x] T048 [US1] Oracle test in `oracle_csv_test.go` (container-backed, build tag `integration`): `COPY` the same file into a `postgres_log` table and compare field-for-field (AC-001, §6.1 Oracle)

**Checkpoint**: csvlog parses losslessly at 0 allocs/op — the MVP already replaces pgwatch's current parser

---

## Phase 4: User Story 2 - stderr parsing with `log_line_prefix` (Priority: P2)

**Goal**: Parse `stderr` logs with an arbitrary `log_line_prefix`, auto-detecting the prefix when it is not supplied, and assembling multi-line continuation records.

**Independent Test**: Parse `testdata/stderr/*.log` with no `LinePrefix` configured; `DetectedPrefix()` returns the generating prefix and every record has `Severity != SeverityUnknown`.

### Tests for User Story 2 ⚠️

- [x] T049 [P] [US2] Prefix-compiler table test in `prefix_test.go` covering every escape `%a %u %d %r %h %b %p %P %t %m %n %i %e %c %l %s %v %x %q %Q %%` (FMT-003)
- [x] T050 [P] [US2] Padding test in `prefix_test.go` for `%-5p` and `%5p` (E6)
- [x] T051 [P] [US2] `%q` test: a background-worker record whose post-`%q` segments are absent still parses (E5)
- [x] T052 [P] [US2] Auto-detection test in `prefix_detect_test.go`: `log_line_prefix = '%m [%p] %q%u@%d '` recovered from the first 200 lines (AC-003, FMT-004)
- [x] T053 [P] [US2] Continuation test in `stderr_multiline_test.go`: `DETAIL:`, `HINT:`, `STATEMENT:` produce exactly one `Record` with `FlagMultiline` (AC-004); `SplitContinuations: true` produces separate records (FMT-006)
- [x] T054 [P] [US2] Wrapped-statement test: a continuation line beginning with whitespace appends to the previous field with no new record (E7)
- [x] T055 [P] [US2] Localised-severity test: `ОШИБКА` with `MessagesLang: "ru"` yields `SeverityError` and preserves `RawSeverity` (AC-006, E12); the wrong `MessagesLang` yields `SeverityUnknown` without incrementing `Malformed` (E13)
- [x] T056 [P] [US2] Allocation gate `alloc_stderr_test.go` and benchmark `BenchmarkStderrFullParse` (AC-011, PERF-022)

### Implementation for User Story 2

- [x] T057 [US2] Implement the prefix compiler in `prefix.go`: compile a `log_line_prefix` string once into a slice of literal and escape segments (PAT-002, FMT-003)
- [x] T058 [US2] Add padding-width support (`%-5p`, `%5p`) to the compiler and scanner (E6) — depends on T057
- [x] T059 [US2] Implement `%q` conditional-segment handling: segments after `%q` are optional for non-session backends (E5) — depends on T057
- [x] T060 [US2] Implement the linear prefix scanner in `stderr.go`, walking the compiled template and filling `Record` fields (PAT-002, PERF-004) — depends on T057
- [x] T061 [US2] Implement the built-in candidate template list plus the generic heuristic scanner in `prefix_detect.go`, scoring over `Config.DetectLines` lines and reporting via `DetectedPrefix()` (FMT-004)
- [x] T062 [US2] Implement continuation assembly in `stderr.go`: a physical line that does not match the template appends to the previous record's continuation region (FMT-006) — depends on T060
- [x] T063 [US2] Route recognised secondary labels `DETAIL:`, `HINT:`, `STATEMENT:`, `QUERY:`, `CONTEXT:` into `Detail`, `Hint`, `Statement`, `Query`, `Context`, setting `FlagMultiline` and `FlagHasStatement` (FMT-006, AC-004) — depends on T062
- [x] T064 [US2] Honour `Config.SplitContinuations` by emitting continuations as separate records (§4.3) — depends on T063
- [x] T065 [US2] Wire `MessagesLang` severity normalisation into the stderr path, keeping `RawSeverity` as the original bytes (FMT-007, AC-006)
- [x] T066 [US2] BCE/inlining pass on the stderr hot loop, verified as in T047 (GUD-001, GUD-002)

**Checkpoint**: stderr — PostgreSQL's default destination — parses with no server configuration change

---

## Phase 5: User Story 3 - jsonlog parsing (Priority: P3)

**Goal**: Parse PostgreSQL 15+ `jsonlog` NDJSON without `encoding/json` and without allocation.

**Independent Test**: Parse `testdata/json/*.json`; every record equals `encoding/json` unmarshalling of the same line into the reference struct.

### Tests for User Story 3 ⚠️

- [x] T067 [P] [US3] Reference-struct equivalence test in `json_test.go` comparing against `encoding/json` (test-only import) for every documented key (AC-002, FMT-002)
- [x] T068 [P] [US3] Missing-key test: absent optional keys yield zero-valued fields and no error (E8, FMT-002)
- [x] T069 [P] [US3] Escape test: `\"`, `\\` and `\uXXXX` sequences pass through escaped, `FlagNeedsUnquote` is set, and `AppendUnquoted` reproduces the text (E9, PERF-009)
- [x] T070 [P] [US3] Allocation gate `alloc_json_test.go` and benchmark `BenchmarkJSONFullParse` (AC-011, PERF-023)
- [x] T071 [P] [US3] Differential test in `differential_test.go`: the same server activity in `csvlog`, `jsonlog` and `stderr` yields equal `Record` values for all shared fields (COR-004, §6.1 Differential)

### Implementation for User Story 3

- [x] T072 [US3] Implement the NDJSON line framer in `json.go` — exactly one object per physical line, no multi-line assembly (FMT-006)
- [x] T073 [US3] Implement the hand-written object scanner in `json.go`: walk key/value pairs with `bytes.IndexByte`, no `encoding/json`, no map (CON-004, PERF-004, PERF-005)
- [x] T074 [US3] Implement key dispatch for all 29 documented keys via a length-switch / perfect hash, no map lookup (FMT-002, PERF-005) — depends on T073
- [x] T075 [US3] Map values onto `Record` fields, including `session_start` → `SessionStart` and the split `func_name` / `file_name` / `file_line_num` → `Location` (FMT-002, §4.2) — depends on T074
- [x] T076 [US3] Set `FlagNeedsUnquote` on any string value containing a backslash escape; never unescape eagerly (PERF-009, E9)
- [x] T077 [US3] BCE/inlining pass on the JSON hot loop, verified as in T047 (GUD-001, GUD-002)

**Checkpoint**: All three PostgreSQL log destinations parse at 0 allocs/op

---

## Phase 6: User Story 4 - Format auto-detection, resync & robustness (Priority: P4)

**Goal**: `FormatAuto` picks the right parser from the first non-empty line, malformed input never aborts a stream, the parser never panics, and `Seek` resynchronises to a record boundary.

**Independent Test**: Feed a mixed fixture set with no `Format` configured; every file is detected correctly, `Stats().Malformed` accounts for every bad line, and `Err()` is nil.

### Tests for User Story 4 ⚠️

- [x] T078 [P] [US4] Detection test in `detect_test.go`: leading `{` → jsonlog; ISO-8601 plus a comma at a valid CSV column boundary → csvlog; otherwise stderr; explicit `Format` overrides detection (FMT-005)
- [x] T079 [P] [US4] Malformed-line test: an unparseable line advances to the next record, increments `Stats().Malformed` by 1, leaves `Err()` nil, and invokes `Config.OnMalformed` with a borrowed slice (AC-007, FMT-010)
- [x] T080 [P] [US4] Truncated-tail test: a last record without a trailing newline is emitted with `FlagTruncated` when `EmitTruncatedTail` is true and discarded when false (AC-008, FMT-009)
- [x] T081 [P] [US4] Boundary tests: empty file (E16), BOM-only file (E17), invalid UTF-8 passed through unchanged (COR-005, E11)
- [x] T082 [P] [US4] `Next()` idempotence test at EOF: keeps returning `false` with no side effects (IFC-001)
- [x] T083 [P] [US4] Fuzz targets `FuzzParseRecord`, `FuzzPrefixTemplate`, `FuzzUnquote` in `fuzz_test.go` with a seed corpus (COR-002, AC-009, §6.1 Fuzz)
- [x] T084 [P] [US4] `Reset` test: reuse across streams performs no buffer reallocation (PERF-011)

### Implementation for User Story 4

- [x] T085 [US4] Implement `detect.go`: first-non-empty-line format detection including the CSV column-boundary check, overridable by `Config.Format` (FMT-005)
- [x] T086 [US4] Implement malformed-line handling across all three formats: count, callback, continue (FMT-010, IFC-003)
- [x] T087 [US4] Implement `EmitTruncatedTail` handling at EOF with `FlagTruncated` (FMT-009, AC-008)
- [x] T088 [US4] Implement `Seek(offset int64) error` in `seek.go`: resynchronise to the next record boundary per format (§4.3, IFC-006)
- [x] T089 [US4] Add BOM consumption and invalid-UTF-8 pass-through to the framer (E17, COR-005)
- [x] T090 [US4] Add the nightly 30-minute fuzz job in `.github/workflows/fuzz.yml`, filing an issue on any new crasher (TST-007, VAL-005)

**Checkpoint**: The parser is safe on arbitrary input and needs no configuration in the common cases

---

## Phase 7: User Story 5 - Log sources: file, directory tail, rotation, byte offsets (Priority: P5)

**Goal**: Read logs from a single file, an `io.Reader`, or a followed directory with rotation tracking and O(1) byte-offset resumption.

**Independent Test**: Run a tailer against a directory while rotating files (including filename reuse under `log_truncate_on_rotation`); no record is counted twice or skipped.

### Tests for User Story 5 ⚠️

- [x] T091 [P] [US5] `FileSet` ordering test in `fileset_test.go`: directory plus glob resolves to an ordered reader sequence (§4.5)
- [x] T092 [P] [US5] Rotation test: no double counting across rotation, including filename reuse detected by size shrink or inode change with the offset reset to 0 (COR-007, E15)
- [x] T093 [P] [US5] Partial-record test: a partially-written trailing record is not emitted while following (IFC-005, E14)
- [x] T094 [P] [US5] Offset test: resumption performs a single `Seek`, not a line-by-line re-read, and offsets are byte offsets (IFC-006, AC-025)
- [x] T095 [P] [US5] `OffsetStore` test: the default in-memory store is bounded at 2500 entries with LRU eviction (IFC-007)

### Implementation for User Story 5

- [x] T096 [US5] Implement `source_file.go`: single-file reader constructor honouring `Config` and byte offsets (PAT-004, CON-005)
- [x] T097 [US5] Implement `FileSet` in `fileset.go` with `Dir`, per-`Format` `Glob` defaults, `Follow`, `TruncateOnRotation`, `PollInterval`, `Offsets` (§4.5)
- [x] T098 [US5] Implement `(*FileSet).Open(ctx) (io.ReadCloser, error)` returning a reader that transparently continues across rotation and withholds partial trailing records (IFC-005) — depends on T097
- [x] T099 [US5] Implement rotation detection by size shrink and inode/file-id change, portable to Windows (COR-007, E15, PKG-005) — depends on T098
- [x] T100 [US5] Define the `OffsetStore` interface and implement the bounded LRU in-memory default in `offsets.go` (IFC-006, IFC-007)
- [x] T101 [US5] Wire `OffsetStore` into `FileSet.Open` so restarts neither re-read nor skip (AC-025) — depends on T098, T100

**Checkpoint**: The module can follow a live PostgreSQL log directory correctly across rotations

---

## Phase 8: User Story 6 - Parallel scanning (Priority: P6)

**Goal**: `ParallelScan` shards input at record boundaries across N workers with near-linear scaling.

**Independent Test**: `ParallelScan` over an 8-file corpus with 8 workers yields the same aggregate severity counts as a single-threaded scan, at ≥ 0.75× linear scaling.

### Tests for User Story 6 ⚠️

- [x] T102 [P] [US6] Equivalence test in `parallel_test.go`: aggregate counts from `ParallelScan` equal the single-parser result for all three formats (IFC-008)
- [x] T103 [P] [US6] Boundary test: byte-range sharding never splits or duplicates a record (IFC-008)
- [x] T104 [P] [US6] Scaling benchmark `BenchmarkParallelScan` at 1/2/4/8 workers asserting ≥ 0.75× linear scaling (PERF-029, AC-019)
- [x] T105 [P] [US6] Race test: `go test -race` over `ParallelScan` with per-worker `Parser` instances (PERF-012)

### Implementation for User Story 6

- [x] T106 [US6] Implement `ParallelScan(ctx, srcs []io.ReaderAt, cfg Config, workers int, fn func(worker int, r *Record) error) error` in `parallel.go`, one `Parser` per worker (IFC-008)
- [x] T107 [US6] Implement byte-range sharding that snaps to record boundaries using `Seek`, plus file-level sharding for multi-file input (IFC-008, PERF-029) — depends on T088, T106
- [x] T108 [US6] Document that ordering across workers is not guaranteed and that `Parser` is single-goroutine (IFC-008, PERF-012)

**Checkpoint**: Multi-core throughput is available to the CLI and to library consumers

---

## Phase 9: User Story 7 - Nested modules: `compress` and `pgremote` (Priority: P7)

**Goal**: Optional compressed input and remote reads over a PostgreSQL connection, without adding a single dependency to the root module.

**Independent Test**: `go list -deps github.com/cybertec-postgresql/pglogwatch` still shows only standard-library packages while both nested modules build and pass their tests.

### Tests for User Story 7 ⚠️

- [x] T109 [P] [US7] `compress/compress_test.go`: `.gz`, `.zst`, `.bz2` and `.xz` fixtures decode to the identical record stream as their plaintext counterparts
- [x] T110 [P] [US7] `pgremote/pgremote_test.go` with `pgxmock`: chunked `pg_read_file` reads never hand the parser a partial trailing record at a chunk boundary (IFC-009)
- [x] T111 [P] [US7] Dependency test asserting the root module's dependency graph is unchanged now that both nested modules exist (PKG-002, PKG-004, AC-021)

### Implementation for User Story 7

- [x] T112 [P] [US7] Implement `compress/open.go`: extension- and magic-byte-based transparent decompression using `klauspost/compress` and `ulikunitz/xz` (PKG-004)
- [x] T113 [P] [US7] Implement `pgremote/conn.go`: the `Conn` interface over `pgx.Rows` / `pgx.Row` (§4.7)
- [x] T114 [US7] Implement `pgremote/open.go`: `Open(ctx, conn, cfg)` using `pg_ls_logdir()` and `pg_read_file(path, offset, len)` with `ChunkSize` default `10<<20` (IFC-009, §4.7) — depends on T113
- [x] T115 [US7] Implement chunk-boundary carry-over so a partial trailing record is held until the next chunk (IFC-009) — depends on T114
- [x] T116 [US7] Wire `OffsetStore`-compatible byte offsets into the remote path so it composes with `FileSet` semantics (IFC-006) — depends on T100, T114

**Checkpoint**: Optional capabilities exist; the root package is still dependency-free

---

## Phase 10: User Story 8 - Reference CLI `pglogwatch` (Priority: P8)

**Goal**: A CLI mirroring pgweasel's subcommands, emitting text and NDJSON, usable for like-for-like benchmarking.

**Independent Test**: `pglogwatch stats testdata/` prints a severity histogram; `pglogwatch parse --output json testdata/` emits valid NDJSON on stdout with diagnostics on stderr.

### Tests for User Story 8 ⚠️

- [x] T117 [P] [US8] Golden CLI output tests in `cmd/pglogwatch/cli_test.go` for each subcommand, in both text and JSON form
- [x] T118 [P] [US8] stdin test: no path arguments reads standard input (IFC-010)
- [x] T119 [P] [US8] Stream-separation test: `--output json` writes NDJSON to stdout only, all diagnostics to stderr (IFC-011)
- [x] T120 [P] [US8] Exit-code test: `0` success, `1` usage or I/O error, `2` no input matched (IFC-012)
- [x] T121 [P] [US8] Top-K memory test: aggregations are O(K), not O(distinct queries) (PERF-028)

### Implementation for User Story 8

- [x] T122 [US8] Implement `cmd/pglogwatch/main.go` with global flags `--format`, `--line-prefix`, `--lang`, `--begin`, `--end`, `--jobs`, `--output text|json`, `--no-color` (§4.8)
- [x] T123 [P] [US8] Implement `parse` — every record as NDJSON, the canonical machine output (§4.8)
- [x] T124 [P] [US8] Implement `stats` — counts of errors, connections, checkpoints, autovacuums, temp files (§4.8, AC-010)
- [x] T125 [P] [US8] Implement `errors` — WARNING and above, histogram plus top-N messages (§4.8)
- [x] T126 [P] [US8] Implement `slow` — statements above `--min-duration`, top-N slowest plus aggregates (§4.8)
- [x] T127 [P] [US8] Implement `connections` — counts by database, user, application, client (§4.8)
- [x] T128 [P] [US8] Implement `locks` — lock waits, deadlocks, recovery conflicts (§4.8)
- [x] T129 [P] [US8] Implement `peaks` — busiest time buckets, default 10 minutes (§4.8)
- [x] T130 [P] [US8] Implement `system` — server lifecycle and internal events (§4.8)
- [x] T131 [P] [US8] Implement `grep <pattern>` — record-aware search with `-A` / `-B` / `-C` context (§4.8)
- [x] T132 [P] [US8] Implement `bench` — parse and discard, reporting MB/s, ns/record, allocs and peak RSS (§4.8, W1)
- [x] T133 [US8] Implement the bounded top-K aggregator shared by `errors`, `slow`, `connections` and `peaks` (PERF-028) — depends on T124–T130
- [x] T134 [US8] Wire `--jobs` to `ParallelScan` and `compress` to transparent decompression of path arguments (IFC-008, PKG-004) — depends on T107, T112

**Checkpoint**: The CLI can produce every artifact the comparative benchmark needs

---

## Phase 11: User Story 9 - Benchmark harness & comparative proof (Priority: P9)

**Goal**: A reproducible corpus, a comparative harness against pgbadger and pgweasel, and a CI regression gate that makes every published performance claim verifiable.

**Independent Test**: `make corpus && make bench-compare` regenerates the corpus and emits the §6.4 results table with every PERF-024 / PERF-025 threshold evaluated.

### Tests for User Story 9 ⚠️

- [x] T135 [P] [US9] Corpus determinism test in `bench/gen/gen_test.go`: the same seed reproduces byte-identical output and a matching manifest (TST-003)
- [x] T136 [P] [US9] Cross-format equality test: the generator's `stderr`, `csvlog` and `jsonlog` renderings describe the identical event stream (TST-002, COR-004)
- [x] T137 [P] [US9] Count-agreement test: `pglogwatch stats`, `pgbadger` and `pgweasel` report identical per-severity counts on the corpus (AC-010, COR-003)

### Implementation for User Story 9

- [x] T138 [US9] Implement the corpus generator `bench/gen`: seeded mix of connection events, slow queries, errors with `DETAIL` / `HINT` / `STATEMENT`, autovacuum, checkpoint, deadlock and temp-file messages (TST-001)
- [x] T139 [US9] Emit each generated event stream in `stderr`, `csvlog` and `jsonlog` form for every supported major version's column layout (TST-002) — depends on T138
- [x] T140 [US9] Add `make corpus` plus the committed `corpus-v1` manifest recording seed, size, record count and severity histogram; do not commit raw log files (TST-003, DAT-001) — depends on T139
- [x] T141 [P] [US9] Write `bench/MACHINE.md` pinning CPU model, core count, RAM, kernel, filesystem and Go / Perl / Rust versions (TST-014, INF-002)
- [x] T142 [US9] Implement the comparative harness `bench/compare`: run W1–W5 via `hyperfine --warmup 3`, at least 10 runs, capturing median wall-clock and max peak RSS via `/usr/bin/time -v` (TST-011, §6.4)
- [x] T143 [US9] Emit the results table recording tool version, corpus version, machine spec, format, input size, wall-clock, MB/s, peak RSS and output artifact size (TST-013) — depends on T142
- [x] T144 [US9] Document the pgbadger configuration used for W1–W4 and state explicitly what each tool produced; never present unequal outputs as equal (TST-012) — depends on T142
- [x] T145 [US9] Add `make bench-compare` reproducing every README performance claim, citing corpus version and reference machine (GUD-006, VAL-004) — depends on T142
- [ ] T146 [US9] Provision the pinned self-hosted benchmark runner with `pgbadger` and `pgweasel` at pinned versions (INF-002, INF-003, SVC-002) — NOT done: the repository has no registered self-hosted runner. `bench/RUNNER.md` is the procedure; it is hardware and an organisation account, not code.
- [~] T147 [US9] ~~Add `.github/workflows/bench.yml` on the self-hosted runner comparing against the committed baseline with `benchstat`, failing on a > 5 % ns/op regression or any allocation gain (PERF-030, TST-008, AC-020)~~ — REMOVED: the pinned runner (T146) does not exist, so the job only ever queued. PERF-030 is gated by hand per `bench/THRESHOLDS.md`.
- [~] T148 [US9] ~~Add `.github/workflows/bench-compare.yml` running weekly and on release tags, publishing the results table as a build artifact (TST-009)~~ — REMOVED with T147, and for the same reason.
- [x] T149 [US9] Measure and record the throughput floors: csvlog full parse ≥ 250 MB/s, csvlog severity-only ≥ 800 MB/s, stderr ≥ 200 MB/s, jsonlog ≥ 150 MB/s (PERF-020..023, AC-015)
- [x] T150 [US9] Verify ≥ 10× `pgbadger -j 1` and ≥ 1.0× `pgweasel` (target 1.2×) for every W1–W5 workload (PERF-024, PERF-025, AC-016, AC-017)
- [x] T151 [US9] Verify peak RSS < 64 MiB on a 10 GB input, < 25 % of pgbadger's and ≤ 1.25× pgweasel's (PERF-026, PERF-027, AC-018)

**Checkpoint**: Every performance claim in the spec is measured, reproducible and CI-gated

---

> **Implementation stopped here.** Phases 1-12 (T001-T163) are done, one
> commit per task. Phase 13 below is unstarted.
>
> Phase 12 lives in the pgwatch repository, on branch
> `feat/pglogwatch-migration` (14 commits, 602 lines of parser deleted). It is
> **not mergeable yet**: pglogwatch has no v1.0 release, so pgwatch's `go.mod`
> carries two `replace` directives pointing at a sibling checkout. Deleting
> those and pinning the version is the only change that release requires.
> `pgwatch/internal/reaper/MIGRATION.md` records the rest.
>
> `bench/THRESHOLDS.md` records the performance items that are not met or not
> verified, rather than leaving them implied by the ticks above: PERF-021
> (unmet), PERF-025 (unmet on W3), PERF-029 and PERF-030 (need the pinned
> runner from T146).

## Phase 12: User Story 10 - pgwatch migration onto pglogwatch (Priority: P10)

**Goal**: `internal/reaper` becomes a thin adapter; pgwatch gains `stderr` and `jsonlog` support with a byte-identical `server_log_event_counts` measurement.

**Independent Test**: The existing `TestLogParser*` suite passes unchanged and a source with `log_destination = 'stderr'` produces non-zero counts.

### Tests for User Story 10 ⚠️

- [x] T152 [P] [US10] Envelope-equality test in `pgwatch/internal/reaper/logparser_test.go`: the emitted `MeasurementEnvelope` for `server_log_event_counts` is field-for-field identical to pre-migration output for the same input (AC-023, CON-007)
- [x] T153 [P] [US10] stderr end-to-end test: a source with `log_destination = 'stderr'` produces counts where it previously errored out (AC-024, IFC-014)
- [x] T154 [P] [US10] Restart-resumption test: a mid-file restart counts nothing twice, skips nothing, and performs a single `Seek` (AC-025, IFC-006)
- [x] T155 [P] [US10] `pgxmock`-backed remote-path test through `pgremote` (§6.1 Integration)

### Implementation for User Story 10

- [x] T156 [US10] Add `github.com/cybertec-postgresql/pglogwatch` and `pglogwatch/pgremote` to `pgwatch/go.mod`
- [x] T157 [US10] Write the adapter `pgwatch/internal/reaper/logparser.go`: retain `tryDetermineLogSettings` GUC resolution, construct a `pglogwatch.FileSet` (local) or a `pgremote` reader (remote), count severities per database and per instance, and emit the existing envelope shape (IFC-013, PAT-005) — depends on T098, T114
- [x] T158 [US10] Remove the `log_destination must contain 'csvlog'` hard error and accept `stderr` and `jsonlog` (IFC-014) — depends on T157
- [x] T159 [US10] Retain the `logging_collector is not enabled` error (IFC-015) — depends on T157
- [x] T160 [US10] Replace line-count resumption with `pglogwatch` byte offsets backed by an `OffsetStore` (IFC-006, §7.6) — depends on T100, T157
- [x] T161 [US10] Delete `pgwatch/internal/reaper/logparser_local.go` and `logparser_remote.go` (IFC-013, VAL-008) — depends on T157
- [x] T162 [US10] Verify the `server_log_event_counts` schema is byte-identical: lowercase `<severity>` and `<severity>_total` int64 columns unchanged (CON-007) — depends on T157
- [x] T163 [US10] Run the full pgwatch test suite against the released module and confirm `master` builds with the deleted files (VAL-008, VAL-009) — depends on T161

**Checkpoint**: pgwatch runs on the new module with no dashboard or sink changes

---

## Phase 13: Polish & Cross-Cutting Concerns

**Purpose**: Release readiness for v1.0.0

- [ ] T164 [P] Write `README.md`: quick start, borrowed-slice contract, the §9 examples and anti-patterns, and the comparative results table citing corpus version and machine (GUD-006, §9)
- [ ] T165 [P] Freeze and review the public API: ≤ 40 exported identifiers in the root package, each with a doc comment (CON-006, VAL-007) — verified by T030
- [ ] T166 [P] Verify `go list -deps` shows only standard-library packages (AC-021, VAL-002)
- [ ] T167 [P] Verify `go test -bench . -benchmem ./...` reports `0 allocs/op` for every parsing benchmark on all four target platforms (VAL-003)
- [ ] T168 [P] Verify `CGO_ENABLED=0` cross-builds for `linux/amd64`, `linux/arm64`, `darwin/arm64` and `windows/amd64` (AC-022, CON-001, PLT-002)
- [ ] T169 [P] Raise statement coverage to ≥ 90 % root / ≥ 80 % overall, not satisfied by golden-file assertions alone (TST-010, VAL-006)
- [ ] T170 [P] Accumulate ≥ 10 million fuzz executions with no crashers and archive the corpus (AC-009, VAL-005)
- [ ] T171 [P] Scrub any real-world sample fixtures of identifiers before commit (TST-005, COM-002)
- [ ] T172 [P] Audit for global mutable state and confirm all state lives in `Parser` / `Config` values (CON-002)
- [ ] T173 [P] Audit that the library performs no logging and no filesystem access outside the requested reader constructors (CON-003, CON-005)
- [ ] T174 Final BCE and escape-analysis sweep across all three hot loops, with the documenting comments GUD-003 requires (GUD-001, GUD-003)
- [ ] T175 [P] Apply the repository's `modern-go` Go 1.24+ idiom guidance across the module (GUD-005)
- [ ] T176 Write release notes publishing the §6.4 comparative table, citing `bench/MACHINE.md` and the corpus version, and stating the measured value, cause and remediation for any unmet threshold (VAL-004, VAL-010)
- [ ] T177 Tag `v1.0.0` and freeze the root-package API under semantic versioning (PKG-006)
- [ ] T178 Confirm AC-001..AC-025 and VAL-001..VAL-010 all pass in CI (VAL-001)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately
- **Foundational (Phase 2)**: Depends on Setup — BLOCKS every user story
- **US1 csvlog (Phase 3)**: Depends on Foundational only
- **US2 stderr (Phase 4)**: Depends on Foundational only — independent of US1
- **US3 jsonlog (Phase 5)**: Depends on Foundational only — independent of US1/US2
- **US4 detection & robustness (Phase 6)**: Needs at least two format parsers to be meaningful; the detection tests need all three
- **US5 sources (Phase 7)**: Depends on Foundational; needs `Seek` (T088) for offset resumption
- **US6 parallel (Phase 8)**: Depends on `Seek` (T088) for boundary snapping
- **US7 nested modules (Phase 9)**: `compress` depends on Foundational only; `pgremote` depends on the `OffsetStore` interface (T100)
- **US8 CLI (Phase 10)**: Depends on all three formats, `ParallelScan` (T107) and `compress` (T112)
- **US9 benchmarks (Phase 11)**: Depends on the CLI (US8) for like-for-like comparison
- **US10 pgwatch migration (Phase 12)**: Depends on `FileSet` (T098) and `pgremote` (T114)
- **Polish (Phase 13)**: Depends on all desired stories being complete

### Within Each User Story

- Tests are written first and MUST fail before implementation
- Scan primitives before format parsers
- Format parsers before detection
- Parsing before sources; sources before the CLI; the CLI before the comparative benchmark
- Every story ends with an allocation gate and a benchmark before it counts as done

### Parallel Opportunities

- All Setup tasks marked [P] can run together
- Phase 2 primitives T016–T024 are independent files and parallelise fully
- **US1, US2 and US3 are the largest parallel win**: three developers can own `csv.go`, `stderr.go` + `prefix.go`, and `json.go` simultaneously once Phase 2 lands
- US5 (sources) and US7 (`compress`) are independent of format work and can run alongside US1–US3
- All CLI subcommands T123–T132 are separate files and parallelise cleanly
- All [P]-marked test tasks within a story can be written concurrently

---

## Parallel Example: immediately after Phase 2

```bash
# Three format tracks in parallel:
Task: "US1 — implement CSV framing and the column splitter in csv.go"
Task: "US2 — implement the log_line_prefix compiler in prefix.go"
Task: "US3 — implement the NDJSON object scanner in json.go"

# Independent infrastructure at the same time:
Task: "US5 — implement FileSet and rotation detection in fileset.go"
Task: "US7 — implement transparent decompression in compress/open.go"
Task: "US9 — implement the corpus generator in bench/gen"
```

---

## Implementation Strategy

### MVP First (US1 only)

1. Complete Phase 1: Setup
2. Complete Phase 2: Foundational (CRITICAL — blocks everything)
3. Complete Phase 3: US1 csvlog
4. **STOP and VALIDATE**: `0 allocs/op`, golden diffs clean, ≥ 250 MB/s locally
5. This alone already replaces pgwatch's current csvlog parser

### Incremental Delivery

1. Setup + Foundational → primitives ready
2. + US1 csvlog → MVP, benchmarkable
3. + US2 stderr → removes pgwatch's biggest deployment obstacle (§7.7)
4. + US3 jsonlog → all three destinations supported
5. + US4 detection/robustness → zero-configuration, fuzz-clean
6. + US5 sources → live tailing with correct rotation handling
7. + US6 parallel and US7 nested modules → scaling and optional capabilities
8. + US8 CLI → reproducible by third parties
9. + US9 benchmarks → the performance claims become evidence
10. + US10 pgwatch migration → the first consumer ships

### Parallel Team Strategy

1. Everyone completes Setup + Foundational together — it is the shared contract
2. Once Foundational is done:
   - Developer A: US1 (csvlog) → US4 (detection) → US6 (parallel)
   - Developer B: US2 (stderr) → US8 (CLI)
   - Developer C: US3 (jsonlog) → US9 (benchmarks + corpus)
   - Developer D: US5 (sources) → US7 (nested modules) → US10 (pgwatch migration)

---

## Notes

- [P] tasks = different files, no dependencies
- Every parsing task carries an implicit "and it still reports 0 allocs/op" — the allocation gate (T028) is part of the definition of done, not a polish step
- Never introduce `regexp`, `time.Parse`, `strconv.Atoi(string(b))`, `encoding/json`, or a per-record map on the hot path (PERF-004..007, CON-004)
- Borrowed slices are the default; `Clone()` is the only sanctioned allocation path (PAT-001)
- Commit after each task or logical group; keep the `benchstat` baseline current so PERF-030 stays meaningful
- Where a threshold cannot be met, document the measured value, the cause and the remediation — never silently relax the spec (VAL-010)
