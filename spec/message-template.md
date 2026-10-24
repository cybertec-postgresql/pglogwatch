---
title: "pglogwatch masks a record's message into a template"
version: 1.1
date_created: 2026-10-23
date_updated: 2026-10-24
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

Version 1.1 states decisions the implementation made beyond the text of version 1.0. The
requirement ids of 1.0 keep their meaning; REQ-014 to REQ-019 and GUD-002 are new.

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
| **Pattern id** | The first 8 bytes of SHA-256 over severity, SQLSTATE, rule version and template (REQ-010), printed as 16 lowercase hex characters. |
| **Rule version** | The integer `TemplateRulesVersion`, raised whenever a rule change can change a template. |

---

## 3. Requirements, Constraints & Guidelines

### 3.1 Masking

- **REQ-001**: A decimal number that stands alone as a token (integer, decimal, scientific,
  optional sign, optional unit suffix such as `ms`, `kB`, `MB`, `s`) becomes `$n`. A number
  inside a word (`pg_stat_16`, `xid8`) stays. A `_` between two digits, as PostgreSQL 16
  allows (`424_242`), belongs to the number. In a message, a sign after white space belongs
  to the number (`offset -5` gives `offset $1`); in a statement, `a -1` stays a subtraction.
- **REQ-002**: A hexadecimal, octal, binary or LSN-shaped token (`0/16B3748`, `0x1F`,
  `0o777`, `0b1010`) becomes `$n`. Inside a statement an LSN shape is not read, since `c/2` is
  a division there, and a unit after a space is not part of the number (`SELECT 1 s` keeps
  the alias).
- **REQ-003**: A single-quoted literal (`'…'`, `E'…'`, `N'…'`, `U&'…'`, `B'…'`, `X'…'`) and a
  dollar-quoted literal (`$tag$…$tag$`) become `$n`, with doubled quotes and backslash escapes
  handled. The prefixes `E'`, `N'`, `U&'`, `B'` and `X'` start a literal only inside a
  statement (REQ-009). In a message a quote right after a word is punctuation, so the French
  elision in `n'a pas pu ouvrir` is not read as an `N'…'` literal that runs to the end. Inside
  a statement a quote right after any word starts a literal, so `date'2026-01-01'` and
  `SELECT'x'` lose their text.
- **REQ-004**: In a `key=value` sequence such as `connection authorized: user=bob
  database=shop application_name=psql`, each value becomes `$n`; the keys stay. The word right
  before `=` is the key. The value runs to white space, `,`, `;`, `)` or the end, and is read
  as whole tokens, so `user=SECRET-2`, a quoted value with spaces and `write=12.345 s` each
  give one placeholder. An empty value (`application_name=`) gets a placeholder too, so records
  with and without the value share a template.
- **REQ-005**: A double-quoted token is an identifier and stays, with these exceptions where
  PostgreSQL quotes *data* with `"`:
  - when `Record.SQLState` is in class `22` (data exception) or `23` (integrity constraint
    violation), every double-quoted token becomes `$n`;
  - in every class, the first double-quoted token after a phrase of the word `for`, then only
    words, white space and double-quoted tokens, then `:` becomes `$n`. This covers
    `invalid input syntax for type integer: "abc"`, the older `input syntax for integer:` and
    `invalid input value for enum mood: "x"`. Any other token on the way ends the phrase, so
    `for user "bob"` keeps the role;
  - in every class, a token that holds a value becomes `$n` (REQ-014).

  Guillemet quotes, `»…«` as German writes them and `«…»` or `« … »` as French and Russian
  write them, are double quotes under every rule. Spaces and no-break spaces after the opening
  mark are skipped before the body is checked.
- **REQ-006**: A process id, transaction id, virtual transaction id, OID or session id written
  in the message (`process 1234`, `transaction 7890`, `relation 16384`, `PID 1234`) becomes
  `$n`. Numeric ids and the virtual transaction id `3/1234` are already literals. A word that
  holds a digit and follows an id word is an id too, so `session 5f3c1a2b.4d2` is masked. The
  id words, matched without regard to ASCII case, are `process`, `pid`, `backend`, `worker`,
  `transaction`, `subtransaction`, `xid`, `multixact`, `relation`, `database`, `tablespace`,
  `role`, `oid` and `session`.
- **REQ-007**: A file path becomes `$n`, bare or inside double quotes; a path is a value, not
  a shape. A path starts with `/` followed by a word byte or `.`, `./`, `../`, `base/`,
  `global/`, `pg_<word>/`, or a drive letter and `:\` or `:/`. A bare path starts only after a
  byte that is not a word byte, `/`, `.`, `)`, `]`, `\` or `$`, so `t/s` and `(x)/y` stay. It
  ends at white space, a quote, or a `,`, `;`, `:`, `)` or `.` that ends the input or comes
  before white space, so the punctuation of the sentence around it stays.
- **REQ-008**: Placeholders are numbered `$1`, `$2`, … from left to right over the whole
  template, as `pg_stat_statements` numbers its parameters. An existing `$n` in the message is
  masked like a literal and numbered afresh.
- **REQ-014**: A double-quoted token holds a value, and becomes `$n` in every SQLSTATE class,
  when its body is a path (REQ-007), starts with a digit, `+`, `-`, `.`, `:`, `$` or `'`, or
  holds a `'` anywhere. This covers WAL file names (`"000000010000000000000042"`), setting
  values (`parameter "work_mem" changed to "64MB"`), addresses (`"::1"`) and literals after
  `at or near` (`"'secret'"`, `"$$secret"`).
- **REQ-015**: Every run of white space in the whole message, comments inside a statement
  included, becomes one space, and a run at the start or the end is dropped. So
  `12.345 ms  statement:` gives `$1 statement:`.
- **REQ-016**: A placeholder never joins the text next to it into a different token when the
  template is read again (CON-002). A placeholder written right after a run of copied words and
  `$` bytes replaces that run, since `abc$1` would read back as one word and `$$1` as a dollar
  quote. A copied word that starts with a digit right after a placeholder joins it, since `$1`
  followed by `0abc` would read back as `$10`.

### 3.2 Statements and plans

- **REQ-009**: When the message contains `statement: `, `execute <name>: `, `parse <name>: `,
  `bind <name>: ` or `fastpath function call: `, the part after the marker is a statement. The
  template keeps the marker and masks the statement as follows:
  - every literal of REQ-001 to REQ-003 becomes `$n`;
  - an `IN (…)` or `VALUES (…), (…)` list of only placeholders collapses to `IN (...)` and
    `VALUES (...)` (REQ-017);
  - runs of white space collapse to one space (REQ-015), and `--` comments and nested
    `/* */` comments are removed;
  - keywords keep their case as written; identifiers stay.
  A prepared statement name (`execute S_1:`) becomes `$n`, `<unnamed>` included. pgJDBC runs a
  query as the unnamed statement a few times before it prepares it as `S_n`, and both runs
  share one template. `execute fetch from S_1/C_2:` masks the statement and portal name as one
  value. A name that holds white space or `:` is not recognised.
- **REQ-017**: A list collapses when every element is one literal. `NOT IN (1, 2)` collapses,
  since the `IN` keyword starts the list; `= ANY(ARRAY[1, 2])` does not. A word, a cast, an
  operator or a nested parenthesis keeps the list as written, and a `VALUES` list collapses
  only when every tuple qualifies. A collapsed list gives back its placeholder numbers, so the
  numbering after it stays contiguous, as in `pg_stat_statements`.
- **REQ-018**: A statement marker counts only where PostgreSQL writes one: at the start of the
  message, or after white space that follows a placeholder, such as the masked duration. A
  `key=value` value does not count as that placeholder. The marker words match across any run
  of white space, and the `:` must end the message or come before white space. So
  `could not parse error: 42` is not read as SQL, and `$1statement:`, which a literal glued to
  a word becomes, does not read back as a marker.
- **REQ-019**: Inside a statement, the `key=value` rule (REQ-004), the id-word rule (REQ-006),
  the quoted-data rules (REQ-005, REQ-014) and the bare-path rule (REQ-007) are off. There
  `a=b` is a comparison, `relation` is a column name, and a double-quoted token is an
  identifier that stays, also in SQLSTATE class `22` or `23`.
- **REQ-010**: The pattern id is the first 8 bytes, in lowercase hex, of SHA-256 over
  `severity`, `0x00`, `sqlstate` (five bytes, or empty), `0x00`, the decimal rule version,
  `0x00`, and the template. The severity is `Record.Severity.String()`, so `SeverityUnknown`
  hashes as an empty name. A zero `Record.SQLState` hashes as no bytes, not five zero bytes.
  `PatternID` returns the 8 bytes as `[8]byte`, so the id can be a map key; a caller prints it
  with `encoding/hex`.
- **REQ-011**: A template longer than 1,024 bytes is cut at the last UTF-8 boundary at or
  before 1,024 bytes and ends with `…`. The 1,024 bytes do not count the three-byte ellipsis,
  so a template is at most 1,027 bytes. The cut never splits a placeholder. A placeholder it
  would split is dropped whole, so `$12` never reads back as `$1`. White space and punctuation before the
  cut are dropped, so the ellipsis joins the last word; after a placeholder the template ends
  with ` …`, since `user=$1…` would read back as one value. A template that already ends with
  `…` is not cut again. The cut happens before the hash, so long statements that share their
  first kilobyte share a pattern. Masking stops once the template passes the limit, so the
  rest of a long message is not read. The exception is an `IN` or `VALUES` list of only
  literals that starts within the limit. It is read to its end, since it may still collapse.
- **REQ-012**: When the message contains `plan:` after a duration (`auto_explain` output), the
  template ends at `plan:` followed by ` …`. The plan text is never part of a template.
  `plan:` counts in the places REQ-018 allows a statement marker, except the start of the
  message.

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
  `AppendTemplate` never needs more than 1,100 bytes of spare capacity in `dst`, whatever the
  length of the message. `PatternID` hashes in a stack buffer; only a template longer than
  1,027 bytes, which `AppendTemplate` never makes, may cost a heap allocation.
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
- **GUD-002**: `AppendTemplate` reads `Record.Message` as the parser gives it. A caller that
  wants one template per event across log formats unquotes a message flagged
  `FlagNeedsUnquote` with `AppendUnquoted` first, since `""abc""` in csvlog and `\"abc\"` in
  jsonlog give templates that differ from `"abc"`.

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
  statement), yields templates in which no marker survives. The corpus writes the e-mail
  address as `{email}` and the test fills it in, because `TestFixturesCarryNoRealIdentifiers`
  forbids real e-mail addresses in `testdata`.
- **AC-003**: Two records that differ only in values yield the same template and pattern id.
- **AC-004**: `testing.AllocsPerRun` over the corpus reports 0 allocations per call with a
  pre-sized `dst` (CON-001).
- **AC-005**: `AppendTemplate` of a template equals the template (CON-002).
- **AC-006**: A fuzz target never panics and never produces a template over 1,024 bytes plus
  the ellipsis (1,027 bytes).

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


