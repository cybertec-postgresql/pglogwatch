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
| PERF-026 measured as peak **RSS** | **not measured** — the platform cannot |
| PERF-027 peak RSS under 25 % of pgbadger's, at most 1.25× pgweasel's | **not verified** — baselines not installed |
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

**RSS specifically cannot be measured here.** Go exposes a child's peak resident
set through `ru_maxrss`, which Windows does not provide, so
`bench/compare/rss_other.go` reports "not measured" rather than a zero that
would read as "used no memory". On the pinned Linux runner (T146) the
comparative harness will report it directly.

Two independent guards keep the O(1) property from regressing: the read buffer
is capped by `Config.MaxRecordBytes`, and `TestParserBufferGrowsThenSettles`
asserts growth stops. No report retains records except `system`, which is
bounded by `--top`.

PERF-028 is met and measured: allocation for the errors, slow, connections and
peaks reports is identical for 2 000 and 20 000 distinct records.

## Comparative (PERF-024, PERF-025, AC-016, AC-017)

**Not verified.** pgbadger and pgweasel are not installed on this machine, so
the ratios cannot be computed at all. `make bench-compare` runs and produces a
table with those cells marked "not installed", and
`TestAllThreeToolsAgree` skips with a message naming what is missing.

This is the largest outstanding gap in the release assessment, and it is
infrastructure rather than code: INF-003 pins the baseline versions on the
runner that T146 has not yet acquired.

## Regression gate (PERF-030)

Implemented in `.github/workflows/bench.yml` and **not yet exercised**: it
targets the pinned runner, and there is not one. The workflow includes a second
job that says so on every pull request, so an unrun gate cannot be mistaken for
a passing one.
