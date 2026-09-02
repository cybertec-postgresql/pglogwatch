#!/usr/bin/env bash
# pinned-ab.sh -- the same benchmark on two refs, under one set of clocks.
#
#   bash bench/pinned-ab.sh [baseline-ref]     (default: main)
#
# A speedup measured before a change and again after it, on a machine whose
# clocks and neighbours moved in between, attributes nothing. This builds both
# binaries first, pins once, runs them INTERLEAVED so any remaining drift lands
# on both, and restores. Same trap as pinned-run.sh: EXIT, INT, TERM, HUP.
#
# Only the benchmarks are compared, not TestParallelScanScales, because the
# baseline's version of that test measures something different by construction.

set -u

BASE_REF="${1:-main}"
BOOST=/sys/devices/system/cpu/cpufreq/boost
GOV_GLOB='/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_governor'

SAVED_BOOST=""
SAVED_GOV_FILES=()
SAVED_GOV_VALUES=()
RESTORED=0
START_REF=""

restore() {
	[ "$RESTORED" = 1 ] && return
	RESTORED=1
	# Nothing was pinned, so say nothing. Printing "restoring" after an exit
	# that never touched the clocks reads, on a shared machine, as though it
	# had -- and the next person to see it has no way to tell.
	if [ "${#SAVED_GOV_FILES[@]}" -eq 0 ]; then
		[ -n "$START_REF" ] && git checkout -q "$START_REF" 2>/dev/null
		return
	fi
	echo
	echo "== restoring"
	for i in "${!SAVED_GOV_FILES[@]}"; do
		echo "${SAVED_GOV_VALUES[$i]}" | sudo tee "${SAVED_GOV_FILES[$i]}" >/dev/null 2>&1
	done
	[ -n "$SAVED_BOOST" ] && echo "$SAVED_BOOST" | sudo tee "$BOOST" >/dev/null 2>&1
	echo "   governor: $(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null)"
	echo "   boost   : $(cat "$BOOST" 2>/dev/null || echo 'n/a')"
	# Leaving somebody on a detached HEAD is its own kind of damage.
	if [ -n "$START_REF" ]; then
		git checkout -q "$START_REF" 2>/dev/null && echo "   git     : back on $START_REF"
	fi
}
trap restore EXIT INT TERM HUP

if [ -n "$(git status --porcelain)" ]; then
	echo "working tree is dirty; commit or stash first" >&2
	exit 1
fi
START_REF="$(git rev-parse --abbrev-ref HEAD)"
[ "$START_REF" = "HEAD" ] && START_REF="$(git rev-parse HEAD)"

echo "== sudo is needed for two writes; the trap puts both back"
sudo -v || exit 1

echo "== saving current state"
while IFS= read -r f; do
	SAVED_GOV_FILES+=("$f")
	SAVED_GOV_VALUES+=("$(cat "$f")")
done < <(ls $GOV_GLOB 2>/dev/null)
[ "${#SAVED_GOV_FILES[@]}" -eq 0 ] && { echo "no cpufreq governors here" >&2; exit 1; }
[ -r "$BOOST" ] && SAVED_BOOST="$(cat "$BOOST")"
echo "   governor: ${SAVED_GOV_VALUES[0]} (${#SAVED_GOV_FILES[@]} CPUs)  boost: ${SAVED_BOOST:-n/a}"

# Build both BEFORE pinning, so no compilation runs on a pinned box.
TMP="$(mktemp -d)"
echo "== building $START_REF"
go test -c -o "$TMP/after.test" . || exit 1
echo "== building $BASE_REF"
git checkout -q "$BASE_REF" || exit 1
go test -c -o "$TMP/before.test" . || exit 1
git checkout -q "$START_REF" || exit 1

echo "== pinning"
sudo cpupower frequency-set -g performance >/dev/null || exit 1
[ -n "$SAVED_BOOST" ] && { echo 0 | sudo tee "$BOOST" >/dev/null || exit 1; }

BENCH='BenchmarkParallelScan(1|8)$'
for round in 1 2 3; do
	for side in before after; do
		label=$([ "$side" = before ] && echo "$BASE_REF" || echo "$START_REF")
		echo
		echo "== round $round: $label"
		GOMAXPROCS=8 "$TMP/$side.test" -test.run '^$' -test.bench "$BENCH" \
			-test.benchtime=2s -test.count=1 -test.benchmem | grep Benchmark
	done
done
