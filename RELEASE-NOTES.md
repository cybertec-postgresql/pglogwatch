# pglogwatch v1.0.0

A zero-allocation PostgreSQL log parser for Go, extracted from pgwatch and
made to stand on its own.

It reads `stderr`, `csvlog` and `jsonlog` from PostgreSQL 12 through 18 and
hands back one struct per record without allocating on the heap. The root
package imports only the standard library.

## What is in it

- **Three log destinations.** `stderr` with an arbitrary `log_line_prefix`,
  auto-detected when you do not supply one; `csvlog` across every column layout
  from PostgreSQL 12 to 18; `jsonlog` from PostgreSQL 15 onward. The format
  itself is auto-detected from the first non-empty line.
- **Zero allocations per record** in steady state, on all four target
  platforms. Fields are borrowed slices into the parser's read buffer;
  `Record.Clone` is the one sanctioned allocation.
- **Sources.** A single `io.Reader`, or a `FileSet` that follows a log
  directory across rotation, detects truncation and reuse of a filename, and
  resumes from persisted byte offsets rather than line counts.
- **Parallel scanning.** `ParallelScan` shards several files across workers,
  one parser each, snapping shard boundaries to record boundaries.
- **Nested modules,** each with its own `go.mod` so none of their dependencies
  reaches a consumer of the parser: `compress` for transparent `.gz`/`.zst`/
  `.xz`/`.bz2`, `pgremote` for reading a server's log directory over a `pgx`
  connection, and `cmd/pglogwatch`, the reference CLI.

The public API is frozen at 40 exported identifiers, each documented, checked
on every build. From this tag onward it follows semantic versioning: adding an
identifier is a minor release, removing or changing one is a major.

## Performance

The §6.4 comparative benchmark, over `corpus-v1` (seed 20260830, 200 000
records, 61 MB csvlog), median of 5 runs after 2 discarded warmups.

The machine and conditions are recorded in `bench/MACHINE.md`. Reproduce them
with `task bench-compare`, which regenerates the corpus from its seed.

The comparative ratios below are reported, not gated. PERF-024 and PERF-025
were release gates until 2026-09-01, when they were amended to
measured-and-published requirements; §3.4 of the specification records the
amendment and why.

### Speed

| workload | pglogwatch | pgbadger 12.0 | vs. | pgweasel 0.1 (Rust) | vs. | pgweasel (last Go build) | vs. |
|---|---:|---:|---:|---:|---:|---:|---:|
| W1 parse and discard | 0.089 s | 11.274 s | 127.3× | *not implemented* | — | 0.477 s | 5.4× |
| W2 severity histogram | 0.114 s | 11.261 s | 98.9× | *not implemented* | — | 0.461 s | 4.0× |
| W3 errors report | 0.090 s | 11.255 s | 125.0× | 0.071 s | **0.78×** | 0.379 s | 4.2× |
| W4 top slow queries | 0.106 s | 11.263 s | 106.0× | 0.285 s | 2.68× | 0.560 s | 5.3× |
| W5 parallel, 8 workers | 0.054 s | 15.591 s | 286.6× | *not implemented* | — | 1.252 s | 23.0× |

### Peak resident memory

| workload | pglogwatch | pgbadger | pgweasel (Rust) | pgweasel-go |
|---|---:|---:|---:|---:|
| W1 | 3.8 MB | 66.3 MB | — | 73.2 MB |
| W2 | 3.6 MB | 66.2 MB | — | 72.7 MB |
| W3 | 4.2 MB | 66.2 MB | 75.1 MB | 71.7 MB |
| W4 | 5.0 MB | 66.3 MB | 105.5 MB | 72.3 MB |
| W5 | 5.2 MB | 69.1 MB | — | 73.0 MB |

Flat at 3.6–5.2 MB across every workload, including the eight-worker one, while
every baseline sits in the 66–105 MB band regardless of language or era. That
is the clearest evidence in these tables that the result is architectural: the
parser holds one buffer and one record, and the baselines hold the log.

### Throughput against the §3.3 floors

Corpus files of 56–154 MB, well beyond L3, three runs of fifteen iterations
each. The ranges are the observed spread between runs, not error bars:

| requirement | floor | measured | |
|---|---:|---:|---|
| PERF-020 csvlog, full field parse | 250 MB/s | 607–756 MB/s | met, 2.4–3.0× |
| PERF-021 csvlog, severity only | 800 MB/s | 585–598 MB/s | **not met** |
| PERF-022 stderr, full field parse | 200 MB/s | 381–384 MB/s | met, 1.9× |
| PERF-023 jsonlog, full field parse | 150 MB/s | 597–610 MB/s | met, 4.0× |

The 25 % spread on the csvlog row is worth reading twice. It is one workload on
one machine, and it moves further between runs than the 5 % PERF-030 asks
about. Fixing the clocks removes most of it: `bench/pinned-run.sh` sets the
governor to `performance`, disables boost, measures, and puts both back.

### What the comparison is and is not

pgbadger has no parse-only mode. It builds a complete in-memory report on every
row, and `-o /dev/null` discards that without skipping it, so W1–W4 compare
pglogwatch against a tool doing strictly more work. The ratios are large
because the comparison is unequal, and the specification's own rationale (§7.5)
calls the 10× threshold conservative by roughly two orders of magnitude.
`bench/PGBADGER.md` documents the configuration used and states what each tool
actually produced.

pgweasel is measured twice because it was a Go program until late 2025 and is
Rust now, and PERF-025 does not say which one it means. The current release is
what a user installing pgweasel gets, so it is what the threshold is judged
against; the Go build is reported beside it and gates nothing.

## The comparative ratios are reported, not gated

PERF-024 and PERF-025 required pglogwatch to beat pgbadger by 10× and to reach
parity with pgweasel, and failing either blocked a release. As of 2026-09-01
they are measured-and-published requirements instead. The amendment is recorded
in §3.4 of the specification; the reasoning is that a gate on a comparative
ratio rests on two things this project does not control.

**A third party's roadmap.** pgweasel was a Go program until late 2025 and is
Rust now, and the rewrite made it 5.3× faster than its own predecessor on W3.
Under the old wording, someone else's improvement became a release blocker
here, with nothing about pglogwatch having changed.

**Measurements that cannot be taken.** Three of the five workloads above cannot
be compared against pgweasel 0.1 at all: its `stats` subcommand is not
implemented, so it prints an error, writes nothing and exits 0. A requirement
unmeasurable on 60 % of its own workloads can only be gated by blocking
indefinitely or by assuming a result, and VAL-004 forbids the second.

What did not change is the obligation to look. Both ratios are still measured,
still published above, and the workloads with no comparison are named rather
than dropped. The one measured loss is stated plainly: pglogwatch takes 0.090 s
on W3 against pgweasel's 0.071 s, 678 MB/s to 866 MB/s. What that timing does
not show is the rest of the trade — pgweasel used 75.1 MB of resident memory to
pglogwatch's 4.2 MB and emitted 8.5 MB of raw matching lines where pglogwatch
emits an aggregated histogram. It is buying 25 % with roughly nineteen times
the memory and a cheaper output. Worth profiling, not worth blocking on.

This amendment does **not** extend to PERF-020..PERF-023 or PERF-026..PERF-030.
Those are properties of this code measured against itself rather than against a
moving target, and they remain release conditions — which is why PERF-021 below
is still unmet.

## What is measured but not claimed

Per VAL-010, each of these gives the measured value, the cause, and the
remediation. None has been relaxed in the specification.

### PERF-021 — csvlog severity-only throughput: under 600 MB/s against a 800 MB/s floor

The measured figure sits inside the full-parse figure's own run-to-run range,
and that is the whole explanation: **there is no severity-only scan to
measure.**
`Parser.Next` extracts every field of every record, so a caller reading only
`Record.Severity` still pays for the timestamps, the integers and the field
boundaries. The specification describes the workload but §4.3's `Config`
provides no way to request it.

**Remediation.** A `Config.Fields` mask is the smallest change that makes the
requirement meaningful, and it directly serves pgwatch, whose entire use is
severity-and-database counting. It needs one exported identifier and the API
budget is now full, so it has to reuse `Flags` or come with a decision about
how CON-006 counts enumeration values. `bench/THRESHOLDS.md` gives two further
options, including amending the requirement — which needs the specification's
owners, not the implementation.

### PERF-029 / AC-019 — parallel scaling: 6.61× within L3, 4.13× outside it

Eight workers over eight files reach 6.61× on an 8 MB working set, which meets
AC-019's 6×, and 4.13× on a 32 MB one, which does not. Scaling here is bounded
by memory bandwidth rather than by the sharding: single-worker throughput is
roughly 800 MB/s, so eight-fold scaling needs about 6.4 GB/s sustained, which
this laptop does not have while sharing 16 MB of L3 between eight cores.

**Remediation.** This is why PERF-029 is specified against the reference
machine. It needs the runner below, not a code change.

### PERF-030 / AC-020 — a release step, not a CI gate

A 5 % threshold on shared CI capacity fails on variance rather than on
regressions, and a job that reports noise as a regression is a job everybody
learns to ignore. That is worse than not running it: a green tick then attaches
to nothing. The benchmark workflows were removed for that reason.

PERF-030 is now a step before a release: run `task bench`, compare against
`bench/baseline.txt` with `benchstat`, and look at anything past 5 % in ns/op
or any new allocation. Where the clocks move, run it through
`bench/pinned-run.sh` first. `bench/baseline.txt` is captured the same way.

The allocation half of the check does run on every push, because `0 allocs/op`
is exact rather than statistical.

### The table above is not reference-machine data

`bench/MACHINE.md` is the pinned specification and is unfilled, because the
runner does not exist. Everything published here was measured on a development
laptop: AMD Ryzen 9 7940HS, 8 physical / 16 logical cores, 16 MB L3,
windows/amd64, Go 1.26.5, with the comparative runs in a Linux container on the
same machine. Boost is not disabled and the machine is not dedicated, so these
figures carry more variance than the 5 % PERF-030 gates on.

They are published because a release with no numbers is worse than a release
with honest ones, and because they establish which thresholds hold and which do
not. They are not evidence that any absolute threshold is met; PERF-020,
PERF-022 and PERF-023 clear their floors by margins far wider than the observed
25 % spread, which is why they are reported as met, but a 5 % judgement cannot
be drawn from this machine.

## Verification

`COMPLIANCE.md` lists every AC-001..AC-025 and VAL-001..VAL-010 with its
status. In summary: 22 of 25 acceptance criteria and 8 of 10 validation
criteria are met and verified. What remains is PERF-021, which is unmet and
explained above; AC-020 and PERF-029/030, blocked on a pinned benchmark runner;
and AC-018 and AC-019, whose properties hold but whose exact stated conditions
were not reproduced.

- **Zero allocation** is gated in CI on `linux/amd64`, `linux/arm64`,
  `darwin/arm64` and `windows/amd64`, since escape analysis differs by
  architecture.
- **The dependency graph** is checked in both the default and `purego` builds.
- **Cross-builds** run for all four platforms under `CGO_ENABLED=0`, and the
  test files are type-checked per platform as well as the packages.
- **Fuzzing** accumulated 30 000 020 executions across three targets with no
  crashers (`FUZZING.md`).
- **Coverage** is 92.0 % for the root package and 81.8 % overall; skipping the
  golden-file tests entirely leaves 91.6 %, so the floor does not rest on them.

## Upgrading pgwatch

pgwatch's `internal/reaper` no longer parses logs. It resolves the server's
GUCs, decides local versus remote, counts severities and emits the envelope;
pglogwatch does the rest, and 602 lines of parser were deleted.

Two things change for a pgwatch operator:

- **`log_destination = 'stderr'` and `'jsonlog'` now work.** The
  `log_destination must contain 'csvlog'` hard error is gone. The
  `logging_collector is not enabled` error is retained, since without the
  collector there are no files to read.
- **Restart resumption uses byte offsets** rather than line counts, so a
  restart mid-file performs one `Seek` instead of re-reading the file line by
  line.

The `server_log_event_counts` measurement schema is byte-identical — the same
lowercase `<severity>` and `<severity>_total` int64 columns — so dashboards and
sinks need no change. `pgwatch/internal/reaper/MIGRATION.md` has the detail.

## Requirements

Go 1.26 or later. Builds with `CGO_ENABLED=0` on `linux/amd64`, `linux/arm64`,
`darwin/arm64` and `windows/amd64`. A `purego` build tag selects a copying
fallback for the one `unsafe` conversion, at the cost of the zero-allocation
guarantee on paths that use it.

BSD 3-Clause.
