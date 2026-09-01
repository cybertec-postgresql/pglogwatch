# pglogwatch

A zero-allocation PostgreSQL log parser for Go. It reads `stderr`, `csvlog` and
`jsonlog` output from PostgreSQL 12–18 and hands back one struct per log record
without allocating on the heap. Log watching — following a log directory across
rotation — is built on top of that parser, not the other way round.

```go
p := pglogwatch.New(f, pglogwatch.Config{}) // format auto-detected
for p.Next() {
    r := p.Record()
    if r.Severity >= pglogwatch.SeverityError {
        errCount++
    }
}
if err := p.Err(); err != nil {
    return err
}
```

That loop performs zero heap allocations per record once the read buffer has
grown to fit the largest record seen so far.

## Install

```bash
go get github.com/cybertec-postgresql/pglogwatch
```

The reference CLI is a separate module, so installing it pulls nothing into
your own build:

```bash
go install github.com/cybertec-postgresql/pglogwatch/cmd/pglogwatch@latest
```

## Why

- **No dependencies.** The root package imports only the standard library.
  Compression, remote reads over a PostgreSQL connection and the CLI live in
  nested modules, so embedding the parser pulls in nothing.
- **No allocations.** Steady-state parsing performs zero heap allocations per
  record. String-valued fields are borrowed slices into the parser's own read
  buffer; `Clone()` is the single sanctioned way to retain one.
- **All three destinations.** `stderr` with an arbitrary `log_line_prefix`
  (auto-detected when you do not supply it), `csvlog` across every column
  layout from PostgreSQL 12 to 18, and `jsonlog` from PostgreSQL 15 onward.

## The borrowed-slice contract

This is the one thing to understand before using the package.

Every `[]byte` field of a `Record` aliases the parser's internal read buffer.
It is valid until the next call to `Next`, and **retaining one past that call
is a bug**. It is the same contract `bufio.Scanner.Bytes` makes and the same
one `encoding/csv` makes in `ReuseRecord` mode, and it is what makes
zero-allocation parsing possible: returning strings would force one copy per
field per record.

So this is wrong,

```go
var msgs [][]byte
for p.Next() {
    msgs = append(msgs, p.Record().Message) // every entry aliases one buffer
}
```

and so is this, for a different reason — it reintroduces the per-record
allocation the package exists to avoid:

```go
for p.Next() {
    m := string(p.Record().Message) // allocates
}
```

This is right:

```go
var owned []*pglogwatch.OwnedRecord
for p.Next() {
    owned = append(owned, p.Record().Clone())
}
```

`Clone` is the only allocation the package makes on your behalf, and it is
always because you asked for it.

## Examples

**Retaining one record.** `Clone` where the condition fires, not in the loop
body, so the cost is proportional to what you keep rather than to what you
read:

```go
var worst *pglogwatch.OwnedRecord
for p.Next() {
    r := p.Record()
    if r.Duration > threshold {
        worst = r.Clone() // r itself is invalid after the next Next()
    }
}
```

**Iterator form.** Equivalent to the loop above, still borrowed, still
zero-allocation per record; a fatal error arrives as a final yield with a nil
record, so one `err` check covers both the error and the end of input:

```go
for r, err := range p.All() {
    if err != nil {
        return err
    }
    _ = r.Message
}
```

**An explicit stderr prefix,** when you would rather state it than have it
detected:

```go
p := pglogwatch.New(f, pglogwatch.Config{
    Format:     pglogwatch.FormatStderr,
    LinePrefix: "%m [%p] %q%u@%d ",
})
```

**Deferred unquoting.** Quoted fields keep their escapes; the parser sets a
flag and never rewrites bytes, so a caller that only wants severities never
pays for unescaping:

```go
var buf []byte // reused across records by the caller
for p.Next() {
    r := p.Record()
    msg := r.Message
    if r.Flags&pglogwatch.FlagNeedsUnquote != 0 {
        buf = pglogwatch.AppendUnquoted(buf[:0], r.Message, p.DetectedFormat())
        msg = buf
    }
    use(msg)
}
```

**Following a log directory** across rotation, resuming from stored byte
offsets:

```go
fs := &pglogwatch.FileSet{
    Dir:    "/var/lib/postgresql/data/log",
    Follow: true,
}
r, err := fs.Open(ctx)
if err != nil {
    return err
}
defer r.Close()

p := pglogwatch.New(r, pglogwatch.Config{})
```

**Several files at once.** `ParallelScan` gives each worker its own parser, so
the borrowing contract still holds — the `Record` passed to the callback
belongs to the calling worker and is valid until the callback returns. It does
**not** preserve order, and the callback is shared, so it must be safe to call
concurrently:

```go
err := pglogwatch.ParallelScan(ctx, srcs, pglogwatch.Config{}, 8,
    func(worker int, r *pglogwatch.Record) error {
        counts[worker][r.Severity]++
        return nil
    })
```

## Concurrency

A `Parser` is not safe for concurrent use, and neither is the `Record` it hands
out. That is not an oversight: a parser hands out slices of one reusable
buffer, and sharing that buffer between goroutines would mean copying — giving
up the property the package exists for. Give each goroutine its own parser, or
use `ParallelScan`, which does exactly that.

## Nested modules

Each has its own `go.mod`, so none of their dependencies reaches a consumer of
the parser:

| module | what it adds |
|---|---|
| `pglogwatch/compress` | transparent `.gz`, `.zst`, `.xz` and `.bz2` reading |
| `pglogwatch/pgremote` | reading a server's log directory over a `pgx` connection |
| `pglogwatch/cmd/pglogwatch` | the reference CLI |

## CLI

```bash
pglogwatch errors      /var/log/postgresql/*.log   # WARNING and above, with a histogram
pglogwatch slow        --min-duration 500ms *.csv  # slowest statements
pglogwatch stats       *.json                      # errors, connections, checkpoints, temp files
pglogwatch connections *.log                       # by database, user, application, client
pglogwatch locks       *.log                       # lock waits, deadlocks, recovery conflicts
pglogwatch peaks       *.log                       # busiest time buckets
pglogwatch system      *.log                       # server lifecycle events
pglogwatch grep -C 3 'deadlock' *.log              # record-aware search
pglogwatch parse       *.log                       # every record as NDJSON
pglogwatch bench       *.log                       # parse and discard; MB/s, ns/record, allocs
```

With no paths it reads standard input. `--output json` emits NDJSON on stdout
and every diagnostic on stderr. Exit codes are `0` success, `1` usage or I/O
error, `2` no input matched.

## Performance

**Throughput** over the generated corpus (files of 56–154 MB, well beyond L3),
three runs of fifteen iterations. The ranges are the spread between runs:

| format | workload | floor (§3.3) | measured |
|---|---|---:|---:|
| csvlog | full field parse | 250 MB/s | 607–756 MB/s |
| csvlog | severity only | 800 MB/s | **585–598 MB/s** |
| stderr | full field parse | 200 MB/s | 381–384 MB/s |
| jsonlog | full field parse | 150 MB/s | 597–610 MB/s |

**Against other tools,** the §6.4 workloads over `corpus-v1` (200 000 records,
61 MB csvlog), median of 5 runs after 2 warmups:

| workload | pglogwatch | pgbadger 12.0 | vs. | pgweasel 0.1 | vs. |
|---|---:|---:|---:|---:|---:|
| W1 parse and discard | 0.089 s | 11.274 s | 127× | *not implemented* | — |
| W2 severity histogram | 0.114 s | 11.261 s | 99× | *not implemented* | — |
| W3 errors report | 0.090 s | 11.255 s | 125× | 0.071 s | **0.78×** |
| W4 top slow queries | 0.106 s | 11.263 s | 106× | 0.285 s | 2.7× |
| W5 parallel, 8 workers | 0.054 s | 15.591 s | 287× | *not implemented* | — |

Peak resident memory is flat at 3.6–5.2 MB across every workload, including the
eight-worker one, against 66–69 MB for pgbadger and 75–105 MB for pgweasel.

### Read this before quoting any of the above

- **These are not reference-machine numbers.** They were measured on an
  unpinned developer laptop (AMD Ryzen 9 7940HS, windows/amd64, Go 1.26.5,
  boost enabled, machine not dedicated). `bench/MACHINE.md` is the pinned
  specification and is not yet filled in, because the benchmark runner it
  describes does not exist. The 25 % spread on the csvlog row above is what
  that costs: one workload moving further between runs than the 5 % a
  regression gate would need to resolve.
- **Two thresholds are not met**, and are printed in bold above rather than
  omitted. csvlog severity-only scanning stays under 600 MB/s against a floor
  of 800, because there is no severity-only mode to measure — `Next` extracts
  every field, so a caller reading only `Severity` still pays for the rest.
  pgweasel is 1.3× faster on the errors report. `bench/THRESHOLDS.md` gives the
  measured value, the cause and the remediation for both.
- **The pgbadger comparison is not like-for-like and does not claim to be.**
  pgbadger has no parse-only mode and builds a complete in-memory report on
  every row, which `-o /dev/null` discards without skipping. `bench/PGBADGER.md`
  documents the configuration used and what each tool actually produced.
- **Reproduce it with `task bench-compare`**, which regenerates the corpus from
  its seed and re-runs every workload. Any figure published anywhere must cite
  the corpus version and the machine, which is what that target prints.

## Requirements

Go 1.26 or later. Builds with `CGO_ENABLED=0` on `linux/amd64`, `linux/arm64`,
`darwin/arm64` and `windows/amd64`. A `purego` build tag selects a copying
fallback for the one `unsafe` conversion, at the cost of the zero-allocation
guarantee on paths that use it.

## Status

The public API is frozen for v1.0.0: 40 exported identifiers, each with a doc
comment, checked on every build. From the tag onward it follows semantic
versioning — adding an identifier is a minor release, removing or changing one
is a major (PKG-006).

Two performance thresholds are unmet at the freeze and are recorded rather
than relaxed; see the note under Performance and `bench/THRESHOLDS.md`.

## Licence

BSD 3-Clause. See [LICENSE](LICENSE).
