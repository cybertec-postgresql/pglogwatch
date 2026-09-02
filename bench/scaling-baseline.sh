#!/usr/bin/env bash
# scaling-baseline.sh -- the confounder sweep behind AC-019 / PERF-029.
#
# Run from the REPOSITORY ROOT on the Linux benchmark machine:
#
#   bash bench/scaling-baseline.sh 2>&1 | tee scaling-baseline.log
#
# It answers one question that TestParallelScanScales deliberately does not:
# how much of the ~4x at 8 workers is the code, and how much is GC, GOMAXPROCS
# and run order? On the development machine those three alone moved the same
# binary between 3.27x and 5.72x, which is why no single-sample ratio may be
# published (VAL-004).
#
# TestParallelScanScales is the measurement of record; this is the diagnosis
# around it. Tracked as issue #3. Takes roughly 10-15 minutes and writes
# nothing into the repository.

set -u

OUT="${OUT:-$(pwd)/scaling-out}"
mkdir -p "$OUT"
BIN="$OUT/par.test"

hr() { printf '\n===== %s =====\n' "$*"; }

# ---------------------------------------------------------------- machine
hr "machine"
date -Is
hostname
go version
uname -srvmo
lscpu | grep -Ei 'model name|^cpu\(s\)|thread|core|socket|numa|mhz|cache' || true
echo "--- governor / boost (a benchmark on a boosting CPU is not a measurement)"
cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null || echo "governor: unknown"
cat /sys/devices/system/cpu/cpufreq/boost 2>/dev/null && echo "  ^ 1 = boost ON" || true
cat /sys/devices/system/cpu/intel_pstate/no_turbo 2>/dev/null && echo "  ^ 0 = turbo ON" || true
echo "--- SMT sibling map (used to pick 8 DISTINCT physical cores below)"
lscpu -p=CPU,CORE,SOCKET | grep -v '^#' | head -40

# 8 logical CPUs on 8 distinct physical cores, for the pinned run.
PIN=$(lscpu -p=CPU,CORE | grep -v '^#' | sort -t, -k2,2n -u | head -8 | cut -d, -f1 | paste -sd,)
echo "pinned CPU set: ${PIN:-<none>}"

NPROC=$(nproc)
echo "nproc=$NPROC"
if [ "$NPROC" -lt 8 ]; then
  echo "FATAL: AC-019 is stated at 8 cores; this machine has $NPROC" >&2
  exit 1
fi

# ---------------------------------------------------------------- build once
hr "build"
# One binary for every run below, so nothing differs but the environment.
go test -c -o "$BIN" . || exit 1
BENCH='BenchmarkParallelScan(1|2|4|8|16)$'
PAIR='BenchmarkParallelScan(1|8)$'

# --------------------------------------------------- 1. the honest ladder
# All worker counts in ONE process, repeated and interleaved by the test
# framework, so thermal drift lands on every side equally. This is the number
# that should be quoted; the current harness takes one sample per side in
# separate runs, which is where 3.27x-vs-4.73x comes from.
hr "1. ladder, one process, count=6, -benchmem"
"$BIN" -test.run '^$' -test.bench "$BENCH" -test.benchtime=2s -test.count=6 -test.benchmem

# --------------------------------------------------- 2. confounder matrix
# Same binary, same benchmark, four environments. On the development machine
# these spanned 3.27x to 5.72x. If they span anything like that here, the
# "~4x is a property of the code" conclusion does not hold yet.
hr "2. confounder matrix (1 vs 8 workers)"
for gogc in 100 off; do
  for gmp in "$NPROC" 8; do
    echo "--- GOGC=$gogc GOMAXPROCS=$gmp"
    GOGC=$gogc GOMAXPROCS=$gmp "$BIN" -test.run '^$' -test.bench "$PAIR" \
      -test.benchtime=2s -test.count=3 -test.benchmem
  done
done

# --------------------------------------------------- 3. pinned to 8 cores
# 8 workers on 8 DISTINCT physical cores, no SMT siblings. On a 16-core
# machine this is the run that tells us whether the reported 3.99x was
# scheduling rather than code.
if [ -n "${PIN:-}" ] && command -v taskset >/dev/null; then
  hr "3. pinned to physical cores $PIN, GOMAXPROCS=8"
  for gogc in 100 off; do
    echo "--- GOGC=$gogc"
    GOGC=$gogc GOMAXPROCS=8 taskset -c "$PIN" "$BIN" -test.run '^$' \
      -test.bench "$PAIR" -test.benchtime=2s -test.count=3 -test.benchmem
  done
else
  hr "3. pinned run SKIPPED (no taskset or no sibling map)"
fi

# --------------------------------------------------- 4. achieved parallelism
# The decisive one. "Total samples = X (Y%)" is how many CPUs were actually
# BUSY. At 8 workers, 800% means all eight cores were saturated and the
# shortfall is per-core slowdown; anything less means cores sat idle. Those
# are different defects and the current harness cannot tell them apart.
hr "4. achieved CPU parallelism"
for w in 1 8; do
  GOMAXPROCS=8 "$BIN" -test.run '^$' -test.bench "BenchmarkParallelScan${w}\$" \
    -test.benchtime=3s -test.cpuprofile "$OUT/cpu$w.prof" | grep Benchmark
  echo -n "  workers=$w  "
  go tool pprof -top -nodecount=1 "$BIN" "$OUT/cpu$w.prof" 2>/dev/null |
    grep -E 'Duration|Total samples'
done
echo "profiles in $OUT (go tool pprof -top $BIN $OUT/cpu8.prof)"

# --------------------------------------------------- 5. the AC-019 test
# What bench/ac018-ac019.sh actually asserts today, plus the GOMAXPROCS the
# requirement is stated at. Expect it to log a speedup and then skip or fail.
hr "5. TestParallelScanScales (the current AC-019 assertion)"
PGLOGWATCH_BENCH=1 GOMAXPROCS=8 \
  "$BIN" -test.run TestParallelScanScales -test.v -test.timeout 30m 2>&1 | tail -20

# --------------------------------------------------- 6. stderr auto-detect
# The path bench/THRESHOLDS.md flags as untested: every parallel benchmark
# passes an explicit FormatJSON, so nothing measures the config where each
# shard re-peeks 256 KiB and re-scores 18 prefix templates. There is no
# benchmark for it yet -- this just confirms that, so we know the gap is real
# and not something I misread.
hr "6. is there any stderr/auto parallel benchmark? (expect: none)"
grep -rn 'FormatStderr\|FormatAuto\|Config{}' parallel_bench_test.go || echo "  none -- confirmed gap"

hr "done"
echo "output kept in $OUT"
