# Threshold assessment

Every PERF-0xx threshold, with its measured value, where it was measured, and —
where it is not met — the cause and the remediation plan. VAL-010 requires
exactly this and forbids the alternative, which is quietly relaxing the number
in the specification.

## Where these were measured

**Not the reference machine.** `bench/MACHINE.md` is unfilled and
`bench/RUNNER.md` explains why: the pinned runner does not exist yet (T146).
Everything below was measured on the development machine:

| item | value |
|---|---|
| CPU | AMD Ryzen 9 7940HS, 8 physical / 16 logical cores, 16 MB L3 |
| OS | windows/amd64 |
| Go | 1.26.5 |
| corpus | corpus-v1, seed 20260830, 300 000 records |
| governor / boost | **not pinned** — this is a laptop |

Boost is not disabled and the machine is not dedicated, so these figures carry
more variance than the 5 % PERF-030 gates on. **None of them may be published
as meeting a threshold** (VAL-004). They are recorded so that the pinned runner
has something to be compared against, and so that the gaps below are visible
now rather than at release.

## Throughput (PERF-020 – PERF-023, AC-015)

Corpus files of 56–154 MB, well beyond L3, measured with
`go test -bench BenchmarkCorpus` in `bench/`.

| requirement | floor | measured | verdict |
|---|---:|---:|---|
| PERF-020 csvlog, full field parse | 250 MB/s | **667 MB/s** | met, 2.7× |
| PERF-021 csvlog, severity-only scan | 800 MB/s | **660 MB/s** | **NOT MET** |
| PERF-022 stderr, full field parse | 200 MB/s | **477 MB/s** | met, 2.4× |
| PERF-023 jsonlog, full field parse | 150 MB/s | **750 MB/s** | met, 5.0× |

All four report 0 allocations per record; the 7–16 allocs/op in the raw output
are per-iteration setup (opening the file, constructing the parser) and do not
scale with record count.

### PERF-021: not met, and why

The measured 660 MB/s is essentially identical to the full-parse figure of
667 MB/s, and that is the whole explanation: **there is no severity-only scan
to measure.** `Parser.Next` extracts every field of every record, so a caller
that reads only `Record.Severity` still pays for the timestamps, the integers
and the field boundaries.

The specification describes the workload (PERF-021, and W2 of §6.4) but §4.3's
`Config` provides no way to request it. A parser cannot know which fields a
caller will read.

**Remediation.** Three options, in increasing order of cost:

1. **A `Config.Fields` mask.** The caller declares which fields it wants and
   the scanners skip the rest. This is the smallest change that makes PERF-021
   meaningful, and it directly serves pgwatch, whose entire use is
   severity-and-database counting. It adds one exported identifier, and CON-006
   currently has three of forty free.
2. **Lazy field extraction.** `Record` would expose methods rather than fields.
   This is a larger API change than PKG-006 should absorb after v1.0 and would
   lose the borrowed-slice simplicity, so it is worth considering only before
   the freeze.
3. **Accept the gap and amend PERF-021.** The full-parse figure already exceeds
   its own floor by 2.7×, and 660 MB/s of complete records may simply be worth
   more than 800 MB/s of severities. This needs a decision from the
   specification's owners, not from the implementation.

Nothing here has been done: the requirement stands unmet and is recorded as
such. Option 1 is the recommendation.

## Parallel scaling (PERF-029, AC-019)

Eight workers over eight files, measured by `TestParallelScanScales` and
`BenchmarkParallelScan`.

| working set | speedup at 8 workers | verdict |
|---|---:|---|
| 8 MB (fits in L3) | **6.61×** | met — AC-019 asks for 6× |
| 32 MB (exceeds L3) | **4.13×** | below 6× |

Scaling here is bounded by memory bandwidth, not by the sharding: single-worker
throughput is roughly 800 MB/s, so eight-fold scaling needs about 6.4 GB/s
sustained, which this laptop does not have while sharing 16 MB of L3 between
eight cores.

This is the reason PERF-029 is specified against the reference machine, and it
is why `TestParallelScanScales` asserts the threshold only when
`PGLOGWATCH_BENCH_MACHINE=1`. Elsewhere it measures, logs both numbers, and
skips. **Not yet verified**; it needs the pinned runner.

## Memory (PERF-026 – PERF-028)

| requirement | status |
|---|---|
| PERF-026 memory O(1) in input size | **met** — measured, see below |
| PERF-026 under 64 MiB for a 10 GB input | **met in substance** — 563 KB at 400 000 records; the 10 GB scale itself was not run |
| PERF-026 measured as peak **RSS** | **met** — 3.6–5.0 MB in the container, against a 64 MiB bound; not measurable on the development machine |
| PERF-027 peak RSS under 25 % of pgbadger's, at most 1.25× pgweasel's | **met** — 4.2 MB against pgbadger's 66.2 MB (6 %) and pgweasel's 75.1 MB (0.06×); see below |
| PERF-028 top-K aggregations O(K), not O(distinct) | **met** — flat across a 10× input in all four reports |

`TestMemoryDoesNotGrowWithInput` samples the heap on a timer while parsing, so
the figure is a peak rather than a final reading:

| format | 20 000 records | 400 000 records |
|---|---:|---:|
| csvlog | 556 KB | 557 KB |
| jsonlog | 557 KB | 558 KB |
| stderr | 559 KB | 560 KB |

Twenty times the input, within 1 KB. That is the property PERF-026's RSS bound
is a proxy for, measured directly.

The bound itself is met with three orders of magnitude to spare: 563 KB against
64 MiB. The 10 GB input in the requirement's wording was not run — it would take
minutes and 10 GB of disk — but the parser holds one buffer and one record, and
the scaling table above is what licenses the inference.

**RSS is measured in the container, not on this machine.** Go exposes a child's
peak resident set through `ru_maxrss`, which Windows does not provide, so
`bench/compare/rss_other.go` reports "not measured" rather than a zero that
would read as "used no memory". Under Linux the comparative harness reports it
directly, and it is 3.6–5.0 MB across all five workloads -- flat, including the
eight-worker one. Two independent measurements of the same property now agree:
the heap sampler says 563 KB of Go heap, the kernel says under 5 MB resident.

Two independent guards keep the O(1) property from regressing: the read buffer
is capped by `Config.MaxRecordBytes`, and `TestParserBufferGrowsThenSettles`
asserts growth stops. No report retains records except `system`, which is
bounded by `--top`.

PERF-028 is met and measured: allocation for the errors, slow, connections and
peaks reports is identical for 2 000 and 20 000 distinct records.

## Comparative (PERF-024, PERF-025, PERF-027, AC-016, AC-017)

**Measured, in a container, not on the pinned runner.** Neither baseline is
installed on the development machine, and PERF-027 is stated in peak RSS, which
Go reads through `ru_maxrss` -- a Unix facility Windows does not provide. A
throwaway Linux image with both baselines makes all three thresholds measurable
at once.

| item | value |
|---|---|
| image | `bench/Dockerfile` -- `task bench:compare-docker` |
| pgbadger | 12.0 (Debian bookworm package) |
| pgweasel | 0.1 (Rust), commit `f2abfe42ac04316bfe889a2ea7ddd658fc5f26ec` |
| pgweasel-go | last Go build, commit `231132a5d5175cfc00434a25b8f6a5772307399e` |
| corpus | corpus-v1, seed 20260830, 200 000 records, 61 MB csvlog |
| runs | 5, after 2 discarded warmups |

The container runs on the same unpinned laptop, so **these figures may not be
published as meeting a threshold** (VAL-004). They establish which thresholds
hold and which do not; the pinned runner establishes the numbers.

### Speed

| workload | pglogwatch | pgbadger | ratio | pgweasel (Rust) | ratio | pgweasel-go | ratio |
|---|---:|---:|---:|---:|---:|---:|---:|
| W1 parse and discard | 0.089 s | 11.274 s | **127.3×** | _not implemented_ | — | 0.477 s | **5.4×** |
| W2 severity histogram | 0.114 s | 11.261 s | **98.9×** | _not implemented_ | — | 0.461 s | **4.0×** |
| W3 errors report | 0.090 s | 11.255 s | **125.0×** | 0.071 s | **0.78×** | 0.379 s | **4.2×** |
| W4 top slow queries | 0.106 s | 11.263 s | **106.0×** | 0.285 s | **2.68×** | 0.560 s | **5.3×** |
| W5 parallel, 8 workers | 0.054 s | 15.591 s | **286.6×** | _not implemented_ | — | 1.252 s | **23.0×** |

**pgweasel is measured twice**, because it was a Go program until late 2025 and
is Rust now, and PERF-025 does not say which one it means. The current release
is what a user installing pgweasel gets, so it is what PERF-025 is judged
against; the Go build is reported beside it and gates nothing.

The rewrite paid off for them: Rust is 5.3× faster than the Go build on W3 and
2.0× on W4. It is also why three cells are empty -- the Rust `stats` subcommand
is not implemented yet, and the Go build's is.

**PERF-024 (≥ 10× `pgbadger -j 1`) is met in every workload**, by 99–269×. The
margin is large because the comparison is not equal and does not claim to be:
pgbadger has no parse-only mode and builds a complete in-memory report in every
row, which `-o /dev/null` discards without skipping. `bench/PGBADGER.md` sets
that out. The specification's own rationale (§7.5) calls the 10× threshold
conservative by roughly two orders of magnitude, and this is what that looks
like measured.

**PERF-025 (≥ parity with pgweasel) cannot be assessed on three of five
workloads**: pgweasel 0.1's `stats` subcommand is not implemented. It prints an
error, writes nothing, and exits 0 -- which the harness originally timed and
reported as pglogwatch losing by 7×. It now refuses the cell instead.

Against the Go build, which does implement `stats`, all five workloads measure
and pglogwatch leads by **4.0× to 23.0×**. That does not satisfy PERF-025 --
the threshold is about the tool that exists today, not the one that did -- but
it is what the three empty cells would otherwise leave unsaid.

### PERF-025 W3: not met, at 0.78×

pgweasel produces its error report in 0.071 s against pglogwatch's 0.090 s --
866 MB/s to 678 MB/s. This is a real measurement of two tools doing comparable
work, and it is recorded as unmet rather than explained away.

This gap is new in the Rust rewrite: the Go build takes 0.379 s on the same
workload, which pglogwatch beats by 4.2×. Whatever changed between the two is
worth reading before profiling anything here.

What the timing does not show is the other half of the trade. pgweasel used
**75.1 MB** of resident memory to pglogwatch's **4.2 MB**, and emitted 8.5 MB of
raw matching lines where pglogwatch emits an aggregated histogram with top
messages. It is buying that 25 % with roughly nineteen times the memory and a
different, cheaper output.

**Remediation.** The gap is small enough to be within reach and the cause is not
yet established -- it may be the top-K normalisation, or output formatting, both
of which pgweasel is not doing. Profile W3 before changing anything. PERF-025
targets 1.2×, so parity alone would not close it.

### Memory (PERF-027)

Peak RSS, maximum across runs, now that a platform reports it:

| workload | pglogwatch | pgbadger | pgweasel (Rust) | pgweasel-go |
|---|---:|---:|---:|---:|
| W1 | 3.8 MB | 66.3 MB | — | 73.2 MB |
| W2 | 3.6 MB | 66.2 MB | — | 72.7 MB |
| W3 | 4.2 MB | 66.2 MB | 75.1 MB | 71.7 MB |
| W4 | 5.0 MB | 66.3 MB | 105.5 MB | 72.3 MB |
| W5 | 5.2 MB | 69.1 MB | — | 73.0 MB |

**PERF-027 is met with a wide margin.** The requirement is under 25 % of
pgbadger's and at most 1.25× pgweasel's; the measurement is 6 % of pgbadger's
and 0.06× pgweasel's. Every baseline holds the log in memory and pglogwatch
does not, which is the whole design, and this is the first measurement that
shows it against something rather than against itself.

pglogwatch's RSS is flat at 3.6–5.2 MB across every workload, including the
eight-worker one. Every baseline sits in the 66–105 MB band regardless of
language or era: pgbadger 66–69 MB, pgweasel-go 71–73 MB, Rust pgweasel
75–105 MB. The rewrite made pgweasel two to five times faster without making it
smaller, which is the clearest evidence in this table that the memory result is
architectural rather than an artefact of the language.

## Regression gate (PERF-030)

Not run in CI, and no longer attempted there. PERF-030's 5 % gate is only
meaningful on the pinned runner (INF-002, SVC-002), and there is not one; on a
shared runner the gate fails on variance rather than on regressions. The
benchmark workflows have been removed from GitHub Actions rather than left to
queue forever against a runner that never arrives, since a job stuck in
`queued` reads as "not finished" when it means "never ran".

The gate is therefore a manual step: run `task bench` on the machine described
in `bench/MACHINE.md` and compare against the committed `bench/baseline.txt`
with `benchstat`. Until that is done for a given change, the change has NOT
been checked for a performance regression and no PERF-0xx threshold may be
reported as met (VAL-004).
