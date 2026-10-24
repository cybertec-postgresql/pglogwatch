---
description: "Task list for pglogwatch: mask a record's message into a template"
---

# Tasks: pglogwatch masks a record's message into a template

**Input**: `spec/message-template.md` (spec v1.0, 2026-10-23)
**Prerequisites**: `spec/message-template.md` (requirements REQ-001..REQ-013, CON-001..CON-003, GUD-001, acceptance criteria AC-001..AC-006); `spec/tool-pglogwatch-zero-alloc-parser.md` (PERF-001, PERF-004)

**Tests**: Tests ARE included. Section 5 of the spec makes every example a golden test and asks for a privacy corpus, an allocation test, an idempotency test and a fuzz target. Section 6 names the files.

**Organization**: Tasks are grouped by user story. Each story adds rules to one function, `AppendTemplate`, so the stories run in order, but each one is tested on its own rows of the section 4 table.

**Repository root**: `pglogwatch/`, root package `pglogwatch`. All new code lives next to `Record` (CON-003).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1..US4)
- Requirement IDs in parentheses trace back to the spec

---

## User story map

| Story | Title | Priority | Delivers |
|---|---|---|---|
| US1 | Mask values in a message | P1 MVP | REQ-001..REQ-008, CON-001, CON-002, GUD-001, AC-001 (non-statement rows) |
| US2 | Mask embedded statements and cut plans | P2 | REQ-009, REQ-011, REQ-012, AC-001 (statement and plan rows) |
| US3 | Pattern identity | P3 | REQ-010, AC-003 |
| US4 | Privacy, allocation and robustness proof | P4 | AC-002, AC-004, AC-005, AC-006 |

---

## Phase 1: Setup

**Purpose**: The public API compiles and returns something, so every later task is a rule change plus a test.

- [x] T001 Create `template.go` with `const TemplateRulesVersion = 1`, `func AppendTemplate(dst []byte, r *Record) []byte` (copies `r.Message` unchanged for now) and `func PatternID(r *Record, template []byte) [8]byte` (zero value for now), with doc comments that state the borrowed-slice contract and the "no allocation when `dst` has capacity" promise (REQ-013, CON-003)
- [x] T002 Create `template_test.go` with a table-driven golden test `TestAppendTemplateGolden` holding every row of spec section 4 (severity, SQLSTATE, message, template); rows not yet implemented are marked with the story that delivers them and skipped, so each later task turns rows on (AC-001)

**Checkpoint**: `go build ./...` and `go test ./...` pass; the API exists.

---

## Phase 2: Foundational

**Purpose**: The scanner every rule is built on.

- [x] T003 Implement in `template.go` a byte-level tokenizer over `Record.Message` with no `regexp` and no allocation (CON-001, PERF-004): word characters are ASCII letters, digits, `_` and bytes above `0x7F`; it recognises single-quoted literals (`'…'`, `E'…'`, `U&'…'`, doubled quotes, backslash escapes in `E''`), dollar-quoted literals (`$tag$…$tag$`), double-quoted tokens, and numbers. Add a placeholder writer that emits `$1`, `$2`, … numbered left to right over the whole template (REQ-008), with unit tests in `template_test.go`

**Checkpoint**: tokenizer unit tests pass; the golden test still skips every masking row.

---

## Phase 3: User Story 1, mask values in a message (Priority: P1) MVP

**Goal**: A message that carries no statement maps to a value-free template that keeps its words and identifiers.

**Independent test**: the golden rows `42P01`, `23505`, `22P02`, `28P01`, `connection authorized`, `checkpoint complete`, `temporary file` and `40P01` pass.

- [x] T004 [US1] Mask numbers, hex and LSN tokens, and quoted literals: a standalone decimal (integer, decimal, scientific, optional sign, optional unit suffix `ms`, `kB`, `MB`, `GB`, `s`, `%`) becomes `$n`, a number inside a word stays; `0x1F` and `0/16B3748` become `$n`; single- and dollar-quoted literals become `$n` (REQ-001, REQ-002, REQ-003). Golden and unit tests in `template_test.go`
- [ ] T005 [US1] Mask `key=value` values, keeping the keys, and mask process, transaction, virtual transaction, OID and session ids that follow `process`, `transaction`, `relation`, `PID` and similar words (REQ-004, REQ-006). Tests in `template_test.go`
- [ ] T006 [US1] Keep double-quoted identifiers, except: mask every double-quoted token when `Record.SQLState` is in class `22` or `23`, and mask the token after `for type <name>:` or `input syntax for` in every class (REQ-005). Mask file paths starting with `/`, `./`, `base/`, `pg_…/` or a drive letter and `:\`, including inside double quotes (REQ-007). Apply GUD-001 where a rule is in doubt. Tests in `template_test.go`
- [ ] T007 [US1] Make masking idempotent: a `$n` placeholder in the input is renumbered, not masked again, so `AppendTemplate` of a template returns it unchanged (CON-002). Add `TestAppendTemplateIdempotent` over every golden row (AC-005)

**Checkpoint**: all non-statement golden rows pass.

---

## Phase 4: User Story 2, mask embedded statements and cut plans (Priority: P2)

**Goal**: `duration: … statement: …` and the extended-protocol markers produce one template per query shape, as `pg_stat_statements` would.

**Independent test**: the golden rows for `statement:`, `execute S_7:` and `plan:` pass.

- [ ] T008 [US2] Detect the markers `statement: `, `execute <name>: `, `parse <name>: `, `bind <name>: ` and `fastpath function call: `; keep the marker, mask the prepared statement name to `$n`, and mask the statement: literals become `$n`, white space runs collapse to one space, `--` and `/* */` comments are removed, keyword case and identifiers stay (REQ-009). White space collapse also applies before the marker, so `12.345 ms  statement:` becomes `$1 statement:`. Tests in `template_test.go`
- [ ] T009 [US2] Collapse `IN (…)` and `VALUES (…), (…)` lists made only of placeholders to `IN (...)` and `VALUES (...)` (REQ-009). Tests in `template_test.go`
- [ ] T010 [US2] Cut at `plan:` after a duration and end the template with `plan: …` (REQ-012); cut any template longer than 1,024 bytes at the last UTF-8 boundary before 1,024 bytes and append `…` (REQ-011). Tests in `template_test.go`

**Checkpoint**: every row of the section 4 table passes; no golden row is skipped.

---

## Phase 5: User Story 3, pattern identity (Priority: P3)

**Goal**: A consumer can count events by a fixed-size key.

**Independent test**: `TestPatternID` pins the id of one golden row to a hex value computed by hand, and two records that differ only in values share an id.

- [ ] T011 [US3] Implement `PatternID` as the first 8 bytes of SHA-256 over `severity`, `0x00`, `sqlstate` (five bytes, or empty when zero), `0x00`, the decimal `TemplateRulesVersion`, `0x00`, `template`, with no allocation (REQ-010, CON-001). Document how to print it as 16 lowercase hex characters with `encoding/hex`. Add `TestPatternID` and `TestSameShapeSameID` with pairs of records that differ only in values (AC-003)

**Checkpoint**: the API is complete.

---

## Phase 6: User Story 4, privacy, allocation and robustness proof (Priority: P4)

**Goal**: The guarantees of section 5 are tested, not claimed.

**Independent test**: `go test -run 'Template|Pattern' ./...` passes and the fuzz target runs for 30 s without a crasher.

- [ ] T012 [P] [US4] Build the privacy corpus under `testdata/template/` with at least 200 messages across severities and SQLSTATE classes, each seeded with a marker literal (`'SECRET-1'`, `424242`, `user=SECRET-2`, `"SECRET-3"` in class `22`, an e-mail address in a statement), and `TestTemplatePrivacy` in `template_test.go` asserting no marker survives (AC-002)
- [ ] T013 [P] [US4] Add `alloc_template_test.go` with `testing.AllocsPerRun` over the corpus and the golden rows, asserting 0 allocations per call of `AppendTemplate` and `PatternID` with a pre-sized `dst` (AC-004, CON-001)
- [ ] T014 [P] [US4] Add `FuzzAppendTemplate` to `fuzz_test.go`: never panics, never returns a template over 1,024 bytes plus the ellipsis, never disturbs bytes already in `dst`, and is idempotent (AC-006, AC-005). Register it in `Taskfile.yml` (`fuzz`, `fuzz-release`), in the `.github/workflows/fuzz.yml` matrix, and in the targets table of `FUZZING.md`
- [ ] T015 [P] [US4] Add `BenchmarkAppendTemplate` reporting ns per record over the existing `testdata` corpus, parsed once and templated in the loop (spec section 6)

**Checkpoint**: every acceptance criterion has a test.

---

## Phase 7: Polish

- [ ] T016 [P] Document the helper: an `Example` for `AppendTemplate` and `PatternID` in `example_test.go`, a section in `README.md`, an entry in `RELEASE-NOTES.md`, and the new exported names in `api_surface_test.go` if it lists them
- [ ] T017 Run `go vet ./...`, `golangci-lint run` if configured, `go test -race ./...` and a 30 s `FuzzAppendTemplate` run; fix what they report; set `status: implemented` in `spec/message-template.md` and tick these tasks

---

## Dependencies and execution order

- Phase 1 has no dependencies. Phase 2 depends on Phase 1 and blocks every story.
- US1 (T004..T007) runs in order: each task edits `template.go`.
- US2 depends on US1, because a statement is masked with US1's literal rules.
- US3 depends only on Phase 1 for the API, but its golden pin needs the final template of a row, so it runs after US2.
- US4 tasks T012..T015 touch different files and run in parallel once US3 is done.
- Polish runs last.

## Implementation strategy

Every task ends with `go test ./...` green and a commit. US1 alone is a usable helper for error messages; US2 makes it usable for statement logging; US3 makes it countable; US4 proves the privacy promise.
