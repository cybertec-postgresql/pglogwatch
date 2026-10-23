---
title: "pglogwatch masks a record's message into a template"
version: 1.0
date_created: 2026-10-23
date_updated: 2026-10-23
owner: pgwatch maintainers
status: draft
tags: [pglogwatch, logs, normalisation, privacy]
---

# Introduction

`pglogwatch` turns a PostgreSQL log into records: severity, SQLSTATE, message, detail, query and
the rest. A consumer that wants to know *which kinds* of events a server writes, and how often,
has to group records whose messages differ only by values: `relation "orders" does not exist`
is one kind of event, and `duration: 12.3 ms  statement: select * from t where id = 42` and the
same statement with `id = 43` are one kind of event too. Today every consumer writes its own
masking, and each one leaks something different.

This specification adds the "documented, optional helper" that the module specification leaves
room for (`spec/tool-pglogwatch-zero-alloc-parser.md`, section 1, out of scope: "Query
normalisation/fingerprinting beyond a documented, optional helper"). The helper replaces the
*values* in a record's message with placeholders and keeps its *shape*: the words, the
identifiers and the structure of an embedded statement. Two records that differ only by values
produce the same template.

The helper runs on every severity, `LOG` included, because the log is where PostgreSQL writes
first, and a server whose administrator turned on statement or connection logging did so on
purpose.

Code references are against `pglogwatch` v1.0.0. Line numbers are indicative, not contractual.

---

## 1. Purpose & Scope

**Purpose**: one function that maps a record's message to a value-free template and a stable
identity, with no allocation, so every consumer groups and counts log events the same way.

**In scope**:

- The masking rules of section 3.1, applied to `Record.Message`.
- The statement rules of section 3.2 for messages that carry a statement.
- A pattern identity: a fixed-size hash over severity, SQLSTATE and template.
- A rule version, so a consumer can tell a template from an older rule set.
- Golden tests per rule, and a privacy corpus whose literals must never survive.

**Out of scope**:

- `Detail`, `Hint`, `Query`, `InternalQuery`, `Context` and `Statement`. They are not read.
- Merging templates across `lc_messages` languages. A German server and an English server give
  different templates for the same event.
- Extracting numbers from a message (buffers written, temporary file sizes). Durations are
  already parsed by the parser (`Record.Duration`).
- Parsing an `auto_explain` plan. The template ends at the plan marker (REQ-012).
- Any state: the helper is a pure function of one record.

**Audience**: `pglogwatch` maintainers; consumers that count or group log events (pgwatch first).

**Assumptions**: messages are UTF-8 or a single-byte encoding that is ASCII-compatible; the
helper treats bytes above `0x7F` as word characters.

---

## 2. Definitions

| Term | Definition |
|---|---|
| **Template** | `Record.Message` with every value replaced by a placeholder (section 3.1), cut at a statement or plan marker and continued with the masked statement (section 3.2). |
| **Value** | A part of a message that varies between occurrences of one event: a number, a quoted literal, the value of a `key=value` pair, a data value quoted in a data or constraint error. |
| **Identifier** | A name of a database object, quoted with `"` by PostgreSQL in its messages. Kept in the template, except where REQ-005 applies. |
| **Pattern id** | 16 lowercase hex characters: the first 8 bytes of SHA-256 over severity, SQLSTATE, rule version and template (REQ-010). |
| **Rule version** | The integer `TemplateRulesVersion`, raised whenever a rule change can change a template. |

---

## 3. Requirements, Constraints & Guidelines

### 3.1 Masking

- **REQ-001**: A decimal number that stands alone as a token (integer, decimal, scientific,
  optional sign, optional unit suffix such as `ms`, `kB`, `MB`, `s`) becomes `$n`. A number
  inside a word (`pg_stat_16`, `xid8`) stays.
- **REQ-002**: A hexadecimal or LSN-shaped token (`0/16B3748`, `0x1F`) becomes `$n`.
- **REQ-003**: A single-quoted literal (`'…'`, `E'…'`, `U&'…'`) and a dollar-quoted literal
  (`$tag$…$tag$`) become `$n`, with doubled quotes and backslash escapes handled.
- **REQ-004**: In a `key=value` sequence such as `connection authorized: user=bob
  database=shop application_name=psql`, each value becomes `$n`; the keys stay.
- **REQ-005**: A double-quoted token is an identifier and stays, with two exceptions where
  PostgreSQL quotes *data* with `"`:
  - when `Record.SQLState` is in class `22` (data exception) or `23` (integrity constraint
    violation), every double-quoted token becomes `$n`;
  - when the token follows `for type <name>:` or `input syntax for`, it becomes `$n` in every
    class.
- **REQ-006**: A process id, transaction id, virtual transaction id, OID or session id written
  in the message (`process 1234`, `transaction 7890`, `relation 16384`, `PID 1234`) becomes
  `$n`.
- **REQ-007**: A file path (a token that starts with `/`, `./`, `base/`, `pg_` followed by `/`,
  or a drive letter and `:\`) becomes `$n`; a path is a value, not a shape.
- **REQ-008**: Placeholders are numbered `$1`, `$2`, … from left to right over the whole
  template, as `pg_stat_statements` numbers its parameters.

### 3.2 Statements and plans

- **REQ-009**: When the message contains `statement: `, `execute <name>: `, `parse <name>: `,
  `bind <name>: ` or `fastpath function call: `, the part after the marker is a statement. The
  template keeps the marker and masks the statement as follows:
  - every literal of REQ-001 to REQ-003 becomes `$n`;
  - an `IN (…)` or `VALUES (…), (…)` list of only placeholders collapses to `IN (...)` and
    `VALUES (...)`;
  - runs of white space collapse to one space, and comments are removed;
  - keywords keep their case as written; identifiers stay.
  A prepared statement name (`execute S_1:`) becomes `$n`.
- **REQ-010**: The pattern id is the first 8 bytes, in lowercase hex, of SHA-256 over
  `severity`, `0x00`, `sqlstate` (five bytes, or empty), `0x00`, the decimal rule version,
  `0x00`, and the template.
- **REQ-011**: A template longer than 1,024 bytes is cut at the last UTF-8 boundary before
  1,024 bytes and ends with `…`. The cut happens before the hash, so long statements that share
  their first kilobyte share a pattern.
- **REQ-012**: When the message contains `plan:` after a duration (`auto_explain` output), the
  template ends at `plan:` followed by ` …`. The plan text is never part of a template.

### 3.3 API and performance

- **REQ-013**: The API is:

  ```go
  // TemplateRulesVersion changes whenever a rule change can change a template.
  const TemplateRulesVersion = 1

  // AppendTemplate appends the masked template of r.Message to dst.
  func AppendTemplate(dst []byte, r *Record) []byte

  // PatternID returns the pattern identity of a template made by AppendTemplate.
  func PatternID(r *Record, template []byte) [8]byte
  ```

- **CON-001**: `AppendTemplate` and `PatternID` perform no heap allocation when `dst` has
  enough capacity (PERF-001 of the module specification), and use no `regexp` (PERF-004).
- **CON-002**: Masking is idempotent: `AppendTemplate` of a record whose message is already a
  template returns it unchanged.
- **CON-003**: The helper lives in the root package next to `Record` and adds no dependency;
  `crypto/sha256` is standard library.
- **CON-004**: The three new exported names (`TemplateRulesVersion`, `AppendTemplate`,
  `PatternID`) fit the API budget of CON-006 in the module specification, as amended in its
  version 1.1 to count each enumeration once. The root package goes from 18 to 21 counted
  identifiers out of 40. Adding them is a minor release (PKG-006).
- **GUD-001**: When a rule is in doubt, mask. A template that is too coarse costs a little
  precision; a template that keeps a value leaks it.

---

## 4. Interfaces & Data Contracts

Examples, English `lc_messages`:

| Severity, SQLSTATE | Message | Template |
|---|---|---|
| `ERROR 42P01` | `relation "orders_2026" does not exist` | `relation "orders_2026" does not exist` |
| `ERROR 23505` | `duplicate key value violates unique constraint "users_email_key"` | `duplicate key value violates unique constraint $1` |
| `ERROR 22P02` | `invalid input syntax for type integer: "abc"` | `invalid input syntax for type integer: $1` |
| `FATAL 28P01` | `password authentication failed for user "bob"` | `password authentication failed for user "bob"` |
| `LOG` | `connection authorized: user=bob database=shop application_name=psql` | `connection authorized: user=$1 database=$2 application_name=$3` |
| `LOG` | `duration: 12.345 ms  statement: SELECT * FROM t WHERE id = 42 AND name = 'x'` | `duration: $1 statement: SELECT * FROM t WHERE id = $2 AND name = $3` |
| `LOG` | `duration: 3.1 ms  execute S_7: select a from b where c in (1, 2, 3)` | `duration: $1 execute $2: select a from b where c in (...)` |
| `LOG` | `checkpoint complete: wrote 123 buffers (0.8%); 0 WAL file(s) added, …` | `checkpoint complete: wrote $1 buffers ($2); $3 WAL file(s) added, …` |
| `LOG` | `temporary file: path "base/pgsql_tmp/pgsql_tmp1234.0", size 1048576` | `temporary file: path $1, size $2` |
| `LOG` | `duration: 1500.2 ms  plan:\nQuery Text: …` | `duration: $1 plan: …` |
| `ERROR 40P01` | `deadlock detected` | `deadlock detected` |

The `23505` row masks the constraint name because the whole class `23` is masked (REQ-005); the
precision lost is accepted (GUD-001).

---

## 5. Acceptance Criteria

- **AC-001**: Every row of the table in section 4 is a golden test.
- **AC-002**: A privacy corpus of at least 200 messages, each seeded with a marker literal
  (`'SECRET-1'`, `424242`, `user=SECRET-2`, `"SECRET-3"` in class `22`, an e-mail address in a
  statement), yields templates in which no marker survives.
- **AC-003**: Two records that differ only in values yield the same template and pattern id.
- **AC-004**: `testing.AllocsPerRun` over the corpus reports 0 allocations per call with a
  pre-sized `dst` (CON-001).
- **AC-005**: `AppendTemplate` of a template equals the template (CON-002).
- **AC-006**: A fuzz target never panics and never produces a template over 1,024 bytes plus
  the ellipsis.

---

## 6. Test Automation Strategy

Golden tests in `template_test.go`, the privacy corpus under `testdata/template/`, an
allocation test next to `alloc_*_test.go`, and a fuzz target `FuzzAppendTemplate` registered
with the existing fuzz job (`FUZZING.md`). A benchmark reports nanoseconds per record over the
existing corpus.

---

## 7. Rationale & Context

### Why keep identifiers?

The name of the missing relation or the violated index is the most useful part of most error
messages, and it is what a DBA searches for. Identifiers are schema, not data. The two places
where PostgreSQL puts data in double quotes are handled by REQ-005.

### Why mask user and database names in `key=value` but keep the user in `password authentication failed for user "bob"`?

`key=value` lines are written once per connection, so keeping the values would make one pattern
per user and database pair, which is a cardinality problem before it is a privacy one. Failed
authentication is rare, and the role name is the fact an operator needs to act on it.

### Why mask inside statements like `pg_stat_statements`?

Operators already read `$1` shapes in `pg_stat_statements`. A server without that extension, or
one that logs with `log_min_duration_statement`, then gives the same shape from its log.

### Why a rule version in the hash?

A rule change can split or merge templates. With the version in the hash, a consumer sees new
ids after an upgrade instead of silently mixing two definitions under one id, and can treat the
change as such.

### Why not merge languages?

That needs a message catalog per PostgreSQL major and language. The severity is already
normalised, and a server's language rarely changes; a template per language is enough.

---

## 8. Dependencies & External Integrations

- `record.go` (`Record.Message`, `Record.SQLState`, `Record.Severity`).
- `spec/tool-pglogwatch-zero-alloc-parser.md` (PERF-001, PERF-004, section 1 "Out of scope").
- Standard library `crypto/sha256`, `encoding/hex`.


