# How pgbadger is run, and why

TST-012 requires the comparative benchmark to document the pgbadger
configuration used and to state explicitly what each tool produced. This file
is that documentation. It exists because the honest answer to "is pglogwatch
faster than pgbadger?" is "at what?", and a table of seconds does not carry
that question.

## The configuration

```
pgbadger -j 1 -o /dev/null -f csv <corpus file>
```

for the single-process workloads (W1–W4), and

```
pgbadger -J 8 -o /dev/null -f csv <corpus files>
```

for the parallel workload (W5).

| flag | why |
|---|---|
| `-j 1` | PERF-024 is stated against `pgbadger -j 1`. W5 uses `-J 8` instead, which is pgbadger's cross-**file** parallelism and the right comparison for a multi-file workload. |
| `-o /dev/null` | The nearest available equivalent to "do the work, keep nothing". |
| `-f csv` | Tells pgbadger the format outright, so no part of the measurement is format detection. pglogwatch is given the same file and detects it, which if anything counts against pglogwatch. |

## What this comparison is not

**pgbadger has no parse-only mode.** It builds a complete report in memory —
per-query aggregation, hourly histograms, the data behind every chart — and
then renders it. `-o /dev/null` discards the *output*; it does not skip the
*work*. Every pgbadger figure in the results table therefore includes report
construction that pglogwatch's `bench` subcommand is not doing.

That is not a flaw in the measurement. It is the closest comparison the two
tools admit, and PERF-024's 10× threshold was written knowing it: the
specification's own rationale (§7.5) calls the threshold "conservative by
roughly two orders of magnitude… to catch catastrophic regressions, not to
represent the expected result". A large ratio against pgbadger says pglogwatch
parses quickly *and* that pgbadger is doing more. Both halves are true, and
quoting the number without the second half would be misleading.

**A fairer comparison does not exist within pgbadger's interface.** There is no
flag that means "parse and stop". The alternatives were considered and
rejected:

- restricting pgbadger's report sections (`--disable-*`) reduces the work but by
  an amount that varies per section and is not documented, so the result would
  be unreproducible;
- comparing pglogwatch's `errors` report against pgbadger's error section would
  be closer in spirit, but pgbadger still builds every other section to produce
  it. W3 does exactly this comparison, and the `produces` column says so.

**jsonlog is not compared at all.** pgbadger cannot read it. A jsonlog row would
compare pglogwatch against pgweasel while appearing to compare three tools, so
every workload uses csvlog — the one format all three support.

## What to do with the numbers

Quote them with the `produces` column attached, cite the corpus version and
`bench/MACHINE.md` (TST-013, TST-014, GUD-006), and do not present a pgbadger
row as though it were the same job. If a threshold is not met, VAL-010 requires
the release notes to give the measured value, the cause and the remediation
plan — not a quietly relaxed threshold.
