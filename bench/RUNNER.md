# Provisioning the benchmark runner

INF-002, INF-003 and SVC-002 call for a dedicated, pinned, self-hosted runner.
This file is the procedure. **The runner does not exist yet** — nothing in this
repository can create it, because it is hardware and an organisation account,
not code.

Until it exists, `bench/MACHINE.md` stays unfilled, both benchmarks stay manual
(they were removed from GitHub Actions rather than queue forever against a runner
that does not exist), and no PERF-0xx threshold may be reported as met (VAL-004).

## What is needed

A machine, not a cloud instance with burstable CPU. SVC-002's whole argument is
that shared capacity has more variance than the 5 % gate PERF-030 asks for, and
a burstable instance is shared capacity with a friendlier name.

| requirement | value | why |
|---|---|---|
| CPU | at least 8 physical cores | PERF-029 and AC-019 are stated at 8 |
| RAM | at least 32 GB | INF-002: the corpus must fit in page cache, or the benchmark measures the disk |
| storage | local NVMe | a network volume adds variance the gate cannot absorb |
| OS | Linux | `ru_maxrss` is available there; peak RSS is unmeasurable on Windows, and PERF-026/PERF-027 are stated in RSS |
| dedication | no other workload | one noisy neighbour is a false regression |

## Setup

```bash
# 1. Fix the clocks. Boost makes the first run of a session faster than the
#    tenth, which turns a 5 % gate into a coin toss.
sudo cpupower frequency-set -g performance
echo 1 | sudo tee /sys/devices/system/cpu/intel_pstate/no_turbo   # Intel
echo 0 | sudo tee /sys/devices/system/cpu/cpufreq/boost           # AMD

# 2. Pin the baselines (INF-003). Record the exact versions in MACHINE.md.
#    Do NOT install from a rolling channel: a baseline that moves between runs
#    makes a change in the ratio unattributable.
sudo apt-get install -y "pgbadger=<pinned>"
cargo install --locked --version <pinned> pgweasel   # or a prebuilt binary

# 3. Toolchains.
#    Go for pglogwatch, Perl for pgbadger (PLT-003), Rust only if pgweasel is
#    built from source (PLT-004).

# 4. Register the runner with the labels the workflows select on.
./config.sh --url https://github.com/cybertec-postgresql/pglogwatch \
            --labels self-hosted,pglogwatch-bench

# 5. Mark it as the reference machine. TestParallelScanScales enforces
#    PERF-029 only where this is set, and skips with its measurement
#    everywhere else.
echo 'PGLOGWATCH_BENCH_MACHINE=1' >> .env
echo "PGLOGWATCH_BENCH_MACHINE_NAME=$(hostname)" >> .env
```

## Then

1. Fill in every row of `bench/MACHINE.md` and record the date.
2. Run `task corpus && task bench-compare` once by hand and check the results
   table names the machine and the corpus version.
3. Commit `bench/baseline.txt` from a clean run so the PERF-030 gate has
   something to compare against. Until that file exists the gate creates it and
   passes, which is correct for the first run and misleading if it happens
   twice — so check it in deliberately.
4. Re-run `TestAllThreeToolsAgree`, which skips everywhere else. That is the
   first time AC-010 is actually verified rather than half-verified against the
   generated corpus alone.

## Keeping it honest

- Any change to this machine invalidates comparison with figures published
  before the change. Treat editing `MACHINE.md` the way you would treat bumping
  the corpus version.
- Do not run anything else on it, including the ordinary CI jobs. They are
  cheap and shared runners are fine for them.
- If a threshold is not met, VAL-010 requires the measured value, the cause and
  a remediation plan in the release notes. Re-running until it passes is the
  thing that rule exists to prevent.
