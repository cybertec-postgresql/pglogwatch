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

## Status

Under active development toward v1.0.0. The public API is not frozen yet.

## Licence

BSD 3-Clause. See [LICENSE](LICENSE).
