# Machines

A throughput figure without a machine is not a measurement, it is an anecdote:
the same corpus on the same code varies by more than a factor of two between a
thermally limited box and one with its clocks fixed. TST-013 requires every
results table to cite the machine it came from, and TST-014 requires that
machine to be recorded here.

That is the whole requirement. There is no reference machine and no dedicated
runner: measure on what you have, fix the clocks for the duration if you can,
and say which machine and which conditions produced the number. Different
machines are expected. Unstated ones are not.

## Measured on

| | A | B |
|---|---|---|
| CPU | AMD Ryzen Threadripper 2950X | AMD Ryzen 9 7940HS |
| cores / threads | 16 / 32 | 8 / 16 |
| OS | Ubuntu 24.04, Linux 6.17 | Windows 11 |
| Go | 1.26.0 | 1.26.5 |
| role | the figures published in `THRESHOLDS.md` | development, correctness only |

Machine A's numbers were taken with the governor at `performance`, boost
disabled and no other workload running. Both settings were restored afterwards;
`bench/pinned-run.sh` does that automatically, including on Ctrl-C, which is
what makes this safe to run on a machine somebody else also uses.

Peak RSS (PERF-026, PERF-027) is measured through `ru_maxrss` and so is
Linux-only. Machine B cannot produce those figures at all.

## Repeating a published figure

```bash
task corpus                  # regenerate corpus-v1 from its seed
bash bench/pinned-run.sh     # fixes clocks, measures, restores
```

For the comparative table against pgbadger and pgweasel:

```bash
task bench-compare           # writes bench/RESULTS.md
```

If your machine differs from the ones above -- and it will -- the numbers will
differ too. Say so rather than comparing them anyway (GUD-006, VAL-010). What
should reproduce is the *shape*: near-linear scaling to 8 workers, zero
allocations per record in steady state, and flat memory in input size.

## Why fixing the clocks matters

A parser benchmark is short and CPU-bound, which is exactly the shape that
boost clocks flatter. It flatters one side of a scaling ratio more than the
other: a single active core boosts, eight do not, so the 1-worker baseline is
measured fast and the 8-worker run slow. That deflates the ratio in a way
indistinguishable from poor parallel scaling, and it is what made AC-019 look
unmet for months. See `THRESHOLDS.md`.

A neighbouring workload does the same thing less predictably. Interference only
ever makes a run slower, so the *minimum* of several runs is the best estimate
of the uncontended time, and a large gap between the median and the minimum
means the machine was busy rather than the code slow.
