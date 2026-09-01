#!/usr/bin/env bash
#
# AC-018 and AC-019 on a machine that can actually run them.
#
# Both are the acceptance criteria that a laptop cannot settle. AC-018 is
# stated against a 10 GB input and against peak RSS, which Go reads through
# ru_maxrss -- a Unix facility Windows does not provide. AC-019 needs eight
# real cores and enough memory bandwidth to feed them.
#
#   AC-018  peak RSS < 64 MiB on a 10 GB input, and < 25 % of pgbadger's
#           peak RSS on the same input (PERF-026, PERF-027)
#   AC-019  >= 6x throughput at 8 workers versus 1 worker (PERF-029)
#
# Usage:
#   ./bench/ac018-ac019.sh              # both
#   ./bench/ac018-ac019.sh ac019        # just the scaling measurement (fast)
#   SIZE_GB=2 ./bench/ac018-ac019.sh    # a smaller AC-018, for a dry run
#   DISTINCT=1 ./bench/ac018-ac019.sh   # generate the whole input, no repeats
#   SKIP_PGBADGER=1 ./bench/ac018-ac019.sh
#
# Requirements: Go 1.26+, GNU time (/usr/bin/time -v), 8+ cores, and for the
# full AC-018 about 12 GB of free disk in $WORK. pgbadger is optional; without
# it the absolute 64 MiB bound is still checked and the 25 % comparison is
# reported as not measured, which is what VAL-004 requires instead of an
# assumed result.
#
# DISTINCT=1 generates SIZE_GB of distinct records instead of repeating a
# smaller corpus. The generator holds every event in memory -- roughly 2 GB of
# RAM per GB of csvlog -- so 10 GB wants about 20 GB free on top of the disk.
# It is the stronger measurement, and the script picks it automatically when
# the machine has the memory for it.
set -euo pipefail

SIZE_GB="${SIZE_GB:-10}"
WORK="${WORK:-${TMPDIR:-/tmp}/pglogwatch-ac018}"
SKIP_PGBADGER="${SKIP_PGBADGER:-0}"
DISTINCT="${DISTINCT:-auto}"
WHAT="${1:-all}"

# Criteria that did not hold. Collected rather than exited on, so that one
# failure does not hide the other measurement.
FAILED=""

repo_root() { cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd; }
ROOT="$(repo_root)"
cd "$ROOT"

say() { printf '\n=== %s\n' "$*"; }
die() { printf 'ac018-ac019: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- prerequisites

command -v go >/dev/null || die "go is not installed"
GO_VER="$(go env GOVERSION)"
say "go $GO_VER on $(uname -srm), $(nproc) cores"

CORES="$(nproc)"
if [ "$CORES" -lt 8 ]; then
	die "AC-019 is stated for 8 cores; this machine has $CORES"
fi

# GNU time, not the shell builtin. Only GNU time reports the maximum resident
# set size, which is the quantity AC-018 is stated in.
#
# `type -P` rather than `command -v`, because `time` is a shell keyword and
# `command -v time` reports the keyword rather than the binary -- which would
# pick something that does not understand -v.
GTIME=""
for c in /usr/bin/time /usr/local/bin/gtime "$(type -P gtime || true)" "$(type -P time || true)"; do
	[ -n "$c" ] || continue
	if [ -x "$c" ] && "$c" -v true >/dev/null 2>&1; then
		GTIME="$c"
		break
	fi
done

# peak_rss_kb CMD... -> prints the maximum resident set size in kilobytes.
peak_rss_kb() {
	local log
	log="$(mktemp)"
	"$GTIME" -v "$@" >/dev/null 2>"$log" || { cat "$log" >&2; rm -f "$log"; return 1; }
	awk '/Maximum resident set size/ { print $NF }' "$log"
	rm -f "$log"
}

# ------------------------------------------------------------------------ AC-019

run_ac019() {
	say "AC-019: parallel scaling to 8 workers (PERF-029)"
	# TestParallelScanScales always measures and logs both figures; it
	# asserts the threshold only when told it is on a machine worth
	# asserting against, which is what this variable means.
	#
	# The failure is recorded rather than propagated, so that a machine
	# which misses AC-019 still goes on to measure AC-018. Running one
	# verification is not a reason to skip the other, and the summary at
	# the end carries the exit status.
	if PGLOGWATCH_BENCH_MACHINE=1 \
		PGLOGWATCH_BENCH_MACHINE_NAME="$(hostname)" \
		go test -run TestParallelScanScales -v -timeout 30m . 2>&1 | tail -20; then
		printf '\nAC-019: MET\n'
	else
		printf '\nAC-019: NOT MET (the speedup is logged above)\n'
		FAILED="${FAILED} AC-019"
	fi
}

# ------------------------------------------------------------------------ AC-018

run_ac018() {
	say "AC-018: peak RSS on a ${SIZE_GB} GB input (PERF-026, PERF-027)"
	[ -n "$GTIME" ] || die "GNU time not found; install it (apt install time) -- \
peak RSS is the quantity AC-018 is stated in and the shell builtin cannot report it"

	mkdir -p "$WORK"
	local seed="$WORK/seed.csv" big="$WORK/big.csv"
	local want=$((SIZE_GB * 1024 * 1024 * 1024))

	# Two ways to reach SIZE_GB, and the difference is worth being explicit
	# about. Generating the whole thing gives distinct records, which is
	# what the requirement literally says; repeating a smaller corpus gives
	# the same input SIZE over the same streaming path, which is what
	# PERF-026 actually bounds. The first needs the generator to hold every
	# event in memory -- roughly 2 GB per GB of csvlog -- so it is chosen
	# only when the machine has the headroom.
	local mem_kb avail_gb records reps=0
	mem_kb="$(awk '/MemAvailable/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)"
	avail_gb=$((mem_kb / 1024 / 1024))
	records=$((SIZE_GB * 3200000)) # ~321 bytes per csvlog record
	if [ "$DISTINCT" = "auto" ]; then
		if [ "$avail_gb" -ge $((SIZE_GB * 2 + 4)) ]; then
			DISTINCT=1
		else
			DISTINCT=0
		fi
		say "available memory ${avail_gb} GB -> DISTINCT=$DISTINCT (override to force)"
	fi

	if [ "$DISTINCT" = "1" ]; then
		if [ ! -s "$big" ]; then
			say "generating ${records} distinct records (~${SIZE_GB} GB of csvlog)"
			# -only pg14: without it the generator also writes the
			# stderr, jsonlog and two other csvlog renderings, which
			# at this scale is four times the disk for input nothing
			# here reads.
			(cd bench && go run ./cmd/corpus -dir "$WORK/corpus" \
				-manifest "$WORK/corpus.manifest" \
				-records "$records" -only pg14 >/dev/null)
			mv "$WORK/corpus/postgresql-pg14.csv" "$big"
		fi
	else
		if [ ! -s "$seed" ]; then
			say "generating the seed corpus (2 000 000 records)"
			(cd bench && go run ./cmd/corpus -dir "$WORK/corpus" \
				-manifest "$WORK/corpus.manifest" \
				-records 2000000 -only pg14 >/dev/null)
			cp "$WORK/corpus/postgresql-pg14.csv" "$seed"
		fi
		local seed_size
		seed_size="$(stat -c %s "$seed" 2>/dev/null || stat -f %z "$seed")"
		reps=$(((want + seed_size - 1) / seed_size))
		if [ ! -s "$big" ] || [ "$(stat -c %s "$big" 2>/dev/null || stat -f %z "$big")" -lt "$want" ]; then
			say "building a ${SIZE_GB} GB input: ${reps} copies of a ${seed_size}-byte corpus"
			: >"$big"
			for _ in $(seq "$reps"); do cat "$seed" >>"$big"; done
		fi
	fi
	local big_size
	big_size="$(stat -c %s "$big" 2>/dev/null || stat -f %z "$big")"
	printf 'input: %s bytes (%.2f GB)\n' "$big_size" "$(echo "$big_size" | awk '{print $1/1073741824}')"

	say "building pglogwatch"
	(cd cmd/pglogwatch && go build -o "$WORK/pglogwatch" .)

	say "measuring pglogwatch"
	local ours
	ours="$(peak_rss_kb "$WORK/pglogwatch" bench --jobs 1 "$big")"
	printf 'pglogwatch peak RSS: %s kB (%.1f MiB)\n' "$ours" "$(echo "$ours" | awk '{print $1/1024}')"

	local theirs=""
	if [ "$SKIP_PGBADGER" != "1" ] && command -v pgbadger >/dev/null 2>&1; then
		say "measuring pgbadger -- this parses ${SIZE_GB} GB at roughly 5 MB/s, so allow around $((SIZE_GB * 200 / 60)) minutes"
		# The invocation bench/PGBADGER.md documents, so the comparison
		# matches the one in the results table.
		theirs="$(peak_rss_kb pgbadger -j 1 -o /dev/null -f csv "$big")"
		printf 'pgbadger peak RSS:   %s kB (%.1f MiB)\n' "$theirs" "$(echo "$theirs" | awk '{print $1/1024}')"
	else
		say "pgbadger not measured (not installed, or SKIP_PGBADGER=1)"
	fi

	# ---- verdicts

	say "AC-018 verdict"
	local bound=$((64 * 1024)) # 64 MiB in kB
	if [ "$ours" -lt "$bound" ]; then
		printf 'PERF-026  peak RSS %s kB < 64 MiB on %s GB: MET\n' "$ours" "$SIZE_GB"
	else
		printf 'PERF-026  peak RSS %s kB >= 64 MiB on %s GB: NOT MET\n' "$ours" "$SIZE_GB"
		FAILED="${FAILED} PERF-026"
	fi

	if [ -n "$theirs" ]; then
		local pct
		pct="$(awk -v a="$ours" -v b="$theirs" 'BEGIN{printf "%.1f", 100*a/b}')"
		if awk -v p="$pct" 'BEGIN{exit !(p < 25)}'; then
			printf 'PERF-027  %s%% of pgbadger (< 25%%): MET\n' "$pct"
		else
			printf 'PERF-027  %s%% of pgbadger (< 25%%): NOT MET\n' "$pct"
			FAILED="${FAILED} PERF-027"
		fi
	else
		printf 'PERF-027  not measured -- VAL-004 does not allow it to be assumed met\n'
	fi

	if [ "$DISTINCT" = "1" ]; then
		printf '\nInput was %s distinct records. This is the measurement AC-018\n' "$records"
		printf 'states literally.\n'
	else
		printf '\nInput was %s copies of a 2 000 000-record corpus, not %s GB of\n' "$reps" "$SIZE_GB"
		printf 'distinct records. AC-018 measures memory against input SIZE and the\n'
		printf 'parser streams, so the path is the same -- but say so when quoting\n'
		printf 'this, or re-run with DISTINCT=1 on a machine with the memory.\n'
	fi
	printf '\nWorking files are in %s; remove them when done.\n' "$WORK"
}

case "$WHAT" in
ac018) run_ac018 ;;
ac019) run_ac019 ;;
all)
	run_ac019
	run_ac018
	;;
*) die "usage: $0 [all|ac018|ac019]" ;;
esac

say "summary"
if [ -n "$FAILED" ]; then
	printf 'NOT MET:%s\n' "$FAILED"
	printf 'Record the measured value in bench/THRESHOLDS.md rather than relaxing\n'
	printf 'the threshold: VAL-010 requires the first and forbids the second.\n'
	exit 1
fi
printf 'Everything measured on this machine held.\n'
printf 'Copy the figures into bench/THRESHOLDS.md and fill in bench/MACHINE.md,\n'
printf 'which is still empty, and cite the machine with any published number.\n'
