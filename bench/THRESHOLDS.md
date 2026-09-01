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

| requirement | floor | first run | re-measured | verdict |
|---|---:|---:|---:|---|
| PERF-020 csvlog, full field parse | 250 MB/s | 667 MB/s | **607–756 MB/s** | met |
| PERF-021 csvlog, severity-only scan | 800 MB/s | 660 MB/s | **585–598 MB/s** | **NOT MET** |
| PERF-022 stderr, full field parse | 200 MB/s | 477 MB/s | **381–384 MB/s** | met |
| PERF-023 jsonlog, full field parse | 150 MB/s | 750 MB/s | **597–610 MB/s** | met |

The second column is the original measurement; the third is the same benchmark
re-run at release (three runs of fifteen iterations each, the range being the
spread between runs). **They do not agree**, and the disagreement is the most
useful number in this file: stderr moved by 20 %, jsonlog by 20 %, and csvlog's
own three runs spanned 25 % between themselves.

Nothing changed in those paths between the two measurements that could account
for it. What changed is the machine's state -- thermal headroom, page cache,
whatever else was running. A threshold verdict drawn from either column is a
verdict about this laptop on that afternoon, which is exactly what VAL-004
refuses to accept and what INF-002's pinned runner exists to fix. Only
PERF-021's verdict is safe from it, because the gap there is structural rather
than marginal: there is no severity-only scan, so no amount of quiet machine
would close a 200 MB/s deficit.

All four report 0 allocations per record; the 7–16 allocs/op in the raw output
are per-iteration setup (opening the file, constructing the parser) and do not
scale with record count.

### PERF-021: not met, and why

The severity-only figure sits inside the full-parse figure's own run-to-run
range in both measurements -- 660 against 667, then 585-598 against 607-756 --
and that is the whole explanation: **there is no severity-only scan to
measure.** `Parser.Next` extracts every field of every record, so a caller
that reads only `Record.Severity` still pays for the timestamps, the integers
and the field boundaries.

The specification describes the workload (PERF-021, and W2 of §6.4) but §4.3's
`Config` provides no way to request it. A parser cannot know which fields a
caller will read.

**Remediation.** Three options, in increasing order of cost:

1. **A `Config.Fields` mask.** The caller declares which fields it wants and
   the scanners skip the rest. This is the smallest change that makes PERF-021
   meaningful, and it directly serves pgwatch, whose entire use is
   severity-and-database counting.

   It needs API room it no longer has. An earlier revision of this file said
   CON-006 had three of forty identifiers free; the T165 freeze review counted
   the surface at exactly 40, so there are none. A `Fields` type plus its
   values would take the count over the cap, and the cap is a release
   condition (VAL-007) rather than a preference. Either the mask reuses
   `Flags`, or the enumeration constants come out of the budget -- 22 of the
   40 are severity, format and flag values, which are one design decision
   each rather than 22, and `api_test.go` records why they are counted the way
   they are.
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

**NOT MET, and the cause is in this code rather than in the machine.** An
earlier revision of this file blamed memory bandwidth on a laptop. Three
machines say otherwise.

| where | physical / logical cores | speedup at 8 workers |
|---|---:|---:|
| development laptop (Ryzen 9 7940HS) | 8 / 16 | 4.07–4.33× |
| server (Ryzen 7 5700G) | 8 / 16 | 3.69× |
| server (Threadripper 2950X) | **16 / 32** | **3.99×** |

The last row is the decisive one. It runs eight workers on a machine with
eight further cores left idle, so no worker contends with another for a
physical core or shares an SMT sibling — and it lands in the same band as the
two machines that have exactly eight. Doubling the hardware available to the
same eight workers changed nothing. That is what "the ceiling is in the code"
means, measured rather than inferred.

An earlier reading of the middle row called that machine 16-core and treated
its 3.69× as the surprising result. It has 8 physical cores and 16 threads,
the same parallel width as the laptop, so it was never the wider machine it
looked like. The Threadripper is.

A second experiment agrees: holding everything else constant and growing the
working set makes scaling **better**, which is the opposite of what a
bandwidth ceiling produces.

| total working set | speedup at 8 workers |
|---|---:|
| 1.6 MB | 2.85× |
| 6.4 MB | 3.91× |
| 32 MB | 3.94× |
| 128 MB | 4.30× |

That curve is the signature of a fixed per-call cost that does not
parallelise: small workloads are dominated by it, large ones amortise it, and
none of them reaches linear.

### The benchmark also compares unequal work

`planShards` clamps the number of parts per source to the worker count:

```go
parts := int(size / minShardBytes)
parts = min(max(parts, 1), workers)
```

So over the same eight 4 MiB inputs, a **1-worker run gets 8 shards and an
8-worker run gets 64**. The parallel side pays eight times the per-shard setup
-- a `Parser.Reset`, a seek, and a resync to the next record boundary -- and
the ratio between them is not a pure measure of parallelism. Holding the shard
count equal (sources small enough that `parts` is always 1) moves the figure
from 4.32× to 4.75× on the laptop: worth about 10 %, and still short of
PERF-029's 6×.

Measured with the shard count held equal, the efficiency curve is:

| workers | speedup | efficiency |
|---:|---:|---:|
| 2 | 1.94× | 0.97 |
| 4 | 3.14× | 0.79 |
| 8 | 4.75× | 0.59 |

**Remediation.** Two separate pieces of work, in order.

1. **Decouple the shard count from the worker count.** `--jobs 1` and
   `--jobs 8` should divide the input the same way and differ only in how many
   goroutines consume it. This is a correctness-of-measurement issue as much
   as a performance one, and it is worth about 10 %.
2. **Find the fixed per-call cost.** The working-set curve says it exists and
   the shard experiment says it is not all per-shard setup. Profile
   `ParallelScan` at 2, 4 and 8 workers on a large input before changing
   anything else.

Note also that `scanShard` calls `ensureFormat` per shard, which for
`FormatAuto` **or** `FormatStderr` peeks up to `detectPeekBytes` (256 KiB) from
the head of the source. These measurements use an explicit `FormatJSON` and so
avoid it entirely; a stderr workload with auto-detected prefixes pays it 64
times where the serial run pays it 8, and has not been measured.

## Memory (PERF-026 – PERF-028)

| requirement | status |
|---|---|
| PERF-026 memory O(1) in input size | **met** — measured, see below |
| PERF-026 under 64 MiB for a 10 GB input | **met, measured at that scale** — 4188 kB peak RSS on a 10.17 GB input |
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
64 MiB.

**The 10 GB input in the requirement's wording has now been run**, on a Linux
machine, via `bench/ac018-ac019.sh`:

| item | value |
|---|---|
| input | 10 919 424 343 bytes (10.17 GB) of csvlog |
| composition | 17 copies of a 2 000 000-record corpus |
| peak RSS | **4188 kB (4.1 MiB)** against a 64 MiB bound |

That is 6.4 % of the bound, and it agrees with the 4.2 MB the comparative
harness measured on a 61 MB input — which is the point of running it. Two
measurements 167 times apart in input size and within 5 % of each other in
memory is PERF-026's O(1) property observed rather than inferred from the heap
sampler alone.

The input is a repeated corpus rather than 10 GB of distinct records, because
the generator holds every event in memory and 33 million of them would need
tens of gigabytes of RAM. PERF-026 bounds memory against input SIZE and the
parser streams, so the path is the same; the distinction is recorded here
rather than left for a reader to assume.

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
and pglogwatch leads by **4.0× to 23.0×**. That is not a substitute for the
current release -- the figure that matters is the tool a user installs today --
but it is what the three empty cells would otherwise leave unsaid.

### PERF-025 W3: 0.78×, reported rather than gated

> **PERF-024 and PERF-025 stopped being release gates on 2026-09-01.** §3.4 of
> the specification records the amendment: a gate on a comparative ratio
> depends on a third party's roadmap and on measurements that cannot always be
> taken, and both showed up here — pgweasel's rewrite turned its own
> improvement into a blocker for this project, and three of five workloads
> cannot be compared at all. The ratios must still be measured and published,
> which is what the rest of this section does. `bench/compare` still computes
> every outcome and still separates a measured loss from an absent baseline;
> only `Blocking` changed.


pgweasel produces its error report in 0.071 s against pglogwatch's 0.090 s --
866 MB/s to 678 MB/s. This is a real measurement of two tools doing comparable
work, and it is recorded rather than explained away.

This gap is new in the Rust rewrite: the Go build takes 0.379 s on the same
workload, which pglogwatch beats by 4.2×. Whatever changed between the two is
worth reading before profiling anything here.

What the timing does not show is the other half of the trade. pgweasel used
**75.1 MB** of resident memory to pglogwatch's **4.2 MB**, and emitted 8.5 MB of
raw matching lines where pglogwatch emits an aggregated histogram with top
messages. It is buying that 25 % with roughly nineteen times the memory and a
different, cheaper output.

**Follow-up, not remediation.** The gap is small enough to be within reach and
the cause is not established -- it may be the top-K normalisation, or output
formatting, neither of which pgweasel is doing. Profile W3 before changing
anything. Since the amendment this is work worth doing rather than work a
release waits on.

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
