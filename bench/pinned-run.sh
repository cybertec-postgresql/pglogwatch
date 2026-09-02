#!/usr/bin/env bash
# pinned-run.sh -- fix the clocks, measure, put them back.
#
# bench/MACHINE.md requires the performance governor and no boost before any
# PERF-0xx figure may be quoted (VAL-004): a parser benchmark is short and
# CPU-bound, exactly the shape boost clocks flatter, and boost gives a
# 1-worker baseline a higher clock than an 8-worker run -- which deflates the
# AC-019 ratio in a way that looks identical to poor scaling.
#
# This is for a SHARED machine. The settings are restored by a trap, so they
# come back on success, on failure, on Ctrl-C and on kill. The window in which
# they are changed is a few seconds around the benchmark itself.
#
#   bash bench/pinned-run.sh
#
# It needs sudo for the two writes and nothing else. Nothing is written to
# disk; both settings are volatile.

set -u

BOOST=/sys/devices/system/cpu/cpufreq/boost
GOV_GLOB='/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_governor'

SAVED_BOOST=""
SAVED_GOV_FILES=()
SAVED_GOV_VALUES=()
RESTORED=0

restore() {
	[ "$RESTORED" = 1 ] && return
	RESTORED=1
	echo
	echo "== restoring"
	for i in "${!SAVED_GOV_FILES[@]}"; do
		echo "${SAVED_GOV_VALUES[$i]}" | sudo tee "${SAVED_GOV_FILES[$i]}" >/dev/null 2>&1
	done
	[ -n "$SAVED_BOOST" ] && echo "$SAVED_BOOST" | sudo tee "$BOOST" >/dev/null 2>&1
	echo "   governor: $(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null)"
	echo "   boost   : $(cat "$BOOST" 2>/dev/null || echo 'n/a')"
	echo "   (expected: ${SAVED_GOV_VALUES[0]:-?} and ${SAVED_BOOST:-n/a})"
}
# Restore on every exit path there is.
trap restore EXIT INT TERM HUP

# Ask for sudo ONCE, up front. A password prompt appearing halfway through
# would otherwise block the restore behind a person who has walked away.
echo "== sudo is needed for two writes; the trap puts both back"
sudo -v || exit 1

echo "== saving current state"
while IFS= read -r f; do
	SAVED_GOV_FILES+=("$f")
	SAVED_GOV_VALUES+=("$(cat "$f")")
done < <(ls $GOV_GLOB 2>/dev/null)
if [ "${#SAVED_GOV_FILES[@]}" -eq 0 ]; then
	echo "no cpufreq governors on this machine; nothing to pin" >&2
	exit 1
fi
[ -r "$BOOST" ] && SAVED_BOOST="$(cat "$BOOST")"
echo "   governor: ${SAVED_GOV_VALUES[0]} (${#SAVED_GOV_FILES[@]} CPUs)"
echo "   boost   : ${SAVED_BOOST:-n/a}"

# Build BEFORE changing anything, so compilation does not run on a pinned box.
echo "== building (clocks still untouched)"
BIN="$(mktemp -d)/par.test"
go test -c -o "$BIN" . || exit 1

echo "== pinning"
sudo cpupower frequency-set -g performance >/dev/null || exit 1
[ -n "$SAVED_BOOST" ] && { echo 0 | sudo tee "$BOOST" >/dev/null || exit 1; }
echo "   governor: $(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor)"
echo "   boost   : $(cat "$BOOST" 2>/dev/null || echo 'n/a')"

echo
echo "== 1. ladder, one process (AC-019 is the 1 vs 8 ratio)"
GOMAXPROCS=8 "$BIN" -test.run '^$' \
	-test.bench 'BenchmarkParallelScan(1|2|4|8)$' \
	-test.benchtime=2s -test.count=5 -test.benchmem | grep -E 'Benchmark|cpu:'

echo
echo "== 2. TestParallelScanScales (the measurement of record)"
PGLOGWATCH_BENCH=1 GOMAXPROCS=8 \
	"$BIN" -test.run TestParallelScanScales -test.v -test.timeout 30m 2>&1 | tail -20

# The trap restores from here, whatever happened above.
