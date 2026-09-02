# Threshold assessment

Every PERF-0xx threshold, with its measured value, where it was measured, and —
where it is not met — the cause and the remediation plan. VAL-010 requires
exactly this and forbids the alternative, which is quietly relaxing the number
in the specification.

## Where these were measured

There is no reference machine; see `bench/MACHINE.md`. Each figure below names
the machine it came from and whether the clocks were fixed, which is what
TST-013 and TST-014 ask for.

| item | machine A | machine B |
|---|---|---|
| CPU | Ryzen Threadripper 2950X, 16 / 32 cores | Ryzen 9 7940HS, 8 / 16 cores |
| OS | Ubuntu 24.04, Linux 6.17 | windows/amd64 |
| Go | 1.26.0 | 1.26.5 |
| corpus | corpus-v1, seed 20260830, 300 000 records | same |

Where a figure was taken with the governor at `performance` and boost disabled,
it says so. Where it was not, treat the spread as part of the result:
`bench/pinned-run.sh` fixes the clocks for the duration of a run and restores
them, and it is the difference between a number worth quoting and one that
describes an afternoon.

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
verdict about that machine on that afternoon rather than about the code. Fixing
the clocks is what closes most of that gap; `bench/pinned-run.sh` does it. Only
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

Tracked as [issue #3](https://github.com/cybertec-postgresql/pglogwatch/issues/3).

**MET when measured properly. The shortfall recorded here was the measurement,
not the code.**

| corpus | 1 worker | 8 workers | speedup | CPUs busy |
|---|---:|---:|---:|---:|
| 8 x 4 MiB (32 MB) | 111.7 ms | 16.35 ms | **6.83x** median, 6.82x fastest-run | 7.91 of 8 |
| 8 x 16 MiB (128 MB) | 451.5 ms | 65.32 ms | **6.91x** median, 6.88x fastest-run | 7.95 of 8 |

Threadripper 2950X, 16 cores, `GOMAXPROCS=8`, `performance` governor, boost
disabled, no other workload, 30 interleaved repetitions. Each side spreads
about 2 %.

Two estimates of the same ratio are given. The first divides the medians. The
second divides each side's *fastest* run, which is not a best case -- taking
the quickest 1-worker run shrinks the numerator, so it can land either side of
the median ratio -- but is the better estimate of the truth, because
interference only ever makes a run slower. They agree here to 0.15 %; where
they do not, the machine was busy and the number describes it rather than the
code.

### What the earlier 4x was

Three things, none of them `ParallelScan`.

**A single sample per side.** `TestParallelScanScales` took one
`testing.Benchmark` measurement of each side, in sequence, and divided them. On
the machines used, the same binary and the same benchmark produced anywhere
between 3.27x and 5.72x depending only on `GOGC`, `GOMAXPROCS` and whether the
two sides ran in one process or two.

**Boost clocks.** `MACHINE.md` already says a parser benchmark is the shape
boost flatters. It flatters the 1-worker side specifically: one active core
boosts, eight do not, so the baseline is measured fast and the parallel run
slow. That deflates the ratio in a way indistinguishable from poor scaling.

**A neighbouring workload.** The machine ran a periodic heavy job. Interference
only ever makes a run slower, so it widened the spread without moving the
floor: the 8-worker figure swung between 4.06 ms and 7.41 ms in one session
while the 4-worker samples, which happened to fall in a quiet window, held to
0.8 %.

Pinning the clocks and pausing the job took the same code from 3.82x to 6.83x.
Measured under those conditions, `main` at the time the issue was filed scores
**6.38x** and the branch that closes the issue scores **6.35x** -- the same
number. **The threshold was already met and had never been measured.**

The two hypotheses this file previously recorded are both wrong and are
withdrawn. It is not memory bandwidth: growing the working set makes scaling
better, not worse. Nor is it a fixed per-call cost in the code: a CPU profile
at 8 workers puts 91.5 % of samples in `Parser.Next`, with no GC, lock,
allocator or scheduler frame above 2 %, and `gctrace` shows the collector
taking about 2.5 % of wall against a flat 4 MB heap goal.

### What changed anyway

Worth about 2 % of throughput and nothing on the ratio, but kept:

- `planShards` no longer clamps parts per source to the worker count. It made
  the shape of the work depend on `--jobs`: eight 4 MiB files became 8 shards
  at `--jobs 1` and 64 at `--jobs 8`, so the two sides of the ratio did
  different amounts of per-shard work. `main`'s 6.38x is the right answer from
  the wrong measurement; the branch's 6.35x compares 32 shards against 32. The
  distinction does not matter for a threshold that is met by a wide margin. It
  matters for PERF-030's 5 % regression gate.
- Workers draw shards from a shared cursor rather than being dealt a fixed
  share, which bounds the tail at one shard. Achieved parallelism at 8 workers
  rises from about 7.0 to 7.9 of 8 CPUs.
- `Config.LinePrefix` is compiled once, before any worker starts. It was
  compiled per worker by `New`, which sets the error before assigning the
  template -- and `scanShard`'s `Reset` then cleared it, so an unparseable
  prefix was silently replaced by auto-detection. A serial `Parser` refused the
  same `Config` and read nothing.

One thing was tried and reverted: allocating every worker's read buffer as a
single slab on the parent goroutine. It is the tidier shape and it was 1.8x
slower in one configuration; the buffer is the hottest memory in the scan and a
page lands on the NUMA node that first touches it. Each worker allocates its
own.

### The correctness bug this uncovered

Raising the shard count exposed a silent loss in csvlog that had shipped in
v1.0.0. Resynchronisation asked `looksLikeCSVLine` whether a line began a
record, but that function asks whether a line **is** one -- a known column count
and a severity in column 12. The first physical line of a record whose message
contains a newline ends inside an open quote and fails it, so `resync` stepped
over the record it had just found. Under `ParallelScan` the record was lost
outright, because the previous shard had already stopped at that offset. On
`main`, 26 shards over a 1.75 MB csvlog of multi-line records lose 6 records,
identically at 1, 2, 4 and 8 workers.

The loss scales with the **shard** count, not the worker count, which is why
nothing caught it: the shard count was capped at `--jobs`, so the suite never
built enough shards for a boundary to land inside a multi-line record.

### What is still not settled

One caveat on the figure itself, worth stating rather than burying: it comes
from a 16-core machine running 8 workers, so the workers never contend for a
physical core or share an SMT sibling and the runtime has spare cores of its
own. On a machine with exactly 8 cores that is a harder test, and the number
would be lower. AC-019 asks for at least 8 cores, not exactly 8, so this
satisfies it -- but somebody re-measuring on an 8-core part should not be
surprised to see less. And the
auto-detecting path is still unmeasured: `scanShard` resolves the format per
shard, which for `FormatAuto` peeks 64 KiB and for stderr without a configured
`LinePrefix` peeks 256 KiB and scores 18 templates against 200 lines. Every
benchmark here passes an explicit `FormatJSON` and so never pays it.

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

**Measured in a container.** Neither baseline is
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
hold and which do not; a fixed-clock run establishes the numbers.

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

## Regression check (PERF-030)

Deliberately not a CI gate. A 5 % threshold on shared CI capacity fails on
variance rather than on regressions, and a job that cries wolf is a job
everybody learns to ignore -- which leaves you worse off than not running it,
because now there is a green tick attached to nothing.

It is a step before a release instead: run `task bench`, compare against the
committed `bench/baseline.txt` with `benchstat`, and look at anything beyond
5 % in ns/op or any new allocation. On a machine with moving clocks, run it
through `bench/pinned-run.sh` first or expect to chase noise.

The allocation half of PERF-030 does hold in CI, because it is exact rather
than statistical: `go test -bench . -benchmem` either reports 0 allocs/op or it
does not, and that is checked on every push.
