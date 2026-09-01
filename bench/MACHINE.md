# Reference machine

TST-014 requires this specification to be pinned and reproduced verbatim in any
published benchmark claim. TST-013 requires every results table to cite it.

A throughput figure without a machine is not a measurement, it is an anecdote:
the same corpus on the same code varies by more than a factor of two between a
laptop under thermal limits and a pinned server, and PERF-029's scaling
threshold is bounded by memory bandwidth rather than by the code at all.

## Status

**Not yet provisioned.** INF-002 and SVC-002 call for a dedicated, pinned,
self-hosted runner; T146 of the implementation plan is the task that acquires
it. Until then:

- the comparative table (`task bench-compare`) runs anywhere and reports what
  it could measure, marking the rest as not measured;
- no figure produced on an unpinned machine may be published as meeting a
  PERF-0xx threshold (VAL-004);
- the scaling assertion in `TestParallelScanScales` only enforces PERF-029 when
  `PGLOGWATCH_BENCH_MACHINE=1` is set, which is set on the pinned runner and
  nowhere else. Elsewhere it measures, logs and skips.

Fill in the table below when the runner exists, and record the date. Changing
any row invalidates comparison with figures published before the change, so
treat an edit here the way you would treat a corpus version bump.

## Specification

| item | value |
|---|---|
| hostname | _to be filled in_ |
| CPU model | _to be filled in_ |
| physical cores | _to be filled in_ |
| logical cores | _to be filled in_ |
| CPU governor | `performance`, fixed |
| turbo / boost | disabled where the platform allows |
| SMT / hyper-threading | _to be filled in_ |
| RAM | at least 32 GB, so the corpus fits in page cache (INF-002) |
| filesystem | _to be filled in_ |
| kernel | _to be filled in_ |
| Go | _to be filled in_ |
| Perl (pgbadger) | _to be filled in_ (PLT-003) |
| Rust or prebuilt pgweasel | _to be filled in_ (PLT-004) |
| pgbadger version | _to be filled in_ (INF-003, pinned) |
| pgweasel version | _to be filled in_ (INF-003, pinned) |
| date pinned | _to be filled in_ |

## Why these settings

**CPU governor and boost.** A parser benchmark is short and CPU-bound, which is
exactly the shape that boost clocks flatter. Leaving boost enabled makes the
first run of a session faster than the tenth and turns a 5 % regression gate
(PERF-030) into a coin toss.

**At least 32 GB of RAM.** The corpus must fit in page cache, or the benchmark
measures the disk. INF-002 requires it for that reason, not for headroom.

**Pinned tool versions.** pgbadger and pgweasel are the baselines PERF-024 and
PERF-025 are stated against. If either moves between runs, a change in the ratio
cannot be attributed, and the threshold stops meaning anything.

**Dedicated and self-hosted.** SVC-002 says shared CI runners have too much
variance for a 5 % gate. That is the whole reason for a separate runner: the
gate is only useful if a failure means a regression rather than a noisy
neighbour.

## Reproducing a published figure

```bash
task corpus                                  # regenerate corpus-v1 from its seed
PGLOGWATCH_BENCH_MACHINE_NAME=<hostname> \
PGLOGWATCH_BENCH_MACHINE=1 \
  task bench-compare                         # measure and write bench/RESULTS.md
```

The results table names the corpus version and this file. If either differs from
the published claim, the numbers are not comparable — say so rather than
comparing them anyway (GUD-006, VAL-010).
