package main

import (
	"runtime"
	"time"

	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["bench"] = command{
		summary: "parse and discard; report MB/s, ns/record, allocations and peak RSS",
		flags:   noFlags,
		run:     runBench,
	}
}

// runBench is workload W1 of §6.4: parse everything, keep nothing, and report
// what it cost.
//
// This is the subcommand the comparative benchmark runs, so what it measures
// has to be exactly the parsing and nothing else. It touches each record's
// severity so a parser cannot be optimised into skipping field extraction,
// and it accumulates nothing, so the figures are the parser's rather than a
// report's.
func runBench(o *options) error {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	var records, bytesIn int64
	var severities [16]int64
	var malformed, truncated, grows int64

	start := time.Now()
	err := o.eachRecordStats(func(r *pglogwatch.Record) {
		records++
		severities[r.Severity]++
	}, func(s pglogwatch.Stats) {
		bytesIn += s.Bytes
		malformed += s.Malformed
		truncated += s.Truncated
		grows += s.BufferGrows
	})
	elapsed := time.Since(start)
	if err != nil {
		return err
	}

	runtime.ReadMemStats(&after)

	mbps := 0.0
	if elapsed > 0 {
		mbps = float64(bytesIn) / (1 << 20) / elapsed.Seconds()
	}
	nsPerRecord := int64(0)
	if records > 0 {
		nsPerRecord = elapsed.Nanoseconds() / records
	}
	// HeapAlloc rather than Sys: Sys includes what the runtime reserved from
	// the operating system and never returned, which measures Go's allocator
	// rather than this program. PERF-026's bound is on what is actually
	// held, and TotalAlloc says how much churn produced it.
	allocs := int64(after.Mallocs - before.Mallocs)

	if o.jsonOut {
		j := newJSONWriter(o.stdout)
		j.begin()
		j.strS("report", "bench")
		j.numAlways("records", records)
		j.numAlways("bytes", bytesIn)
		j.numAlways("elapsed_ns", elapsed.Nanoseconds())
		j.numAlways("ns_per_record", nsPerRecord)
		j.numAlways("allocations", allocs)
		j.numAlways("heap_bytes", int64(after.HeapAlloc)) //nolint:gosec // memory sizes fit
		j.numAlways("malformed", malformed)
		j.numAlways("truncated", truncated)
		j.numAlways("buffer_grows", grows)
		j.key("mb_per_second")
		j.w.WriteString(formatFloat(mbps)) //nolint:errcheck // bufio defers errors
		j.end()
		return j.flush()
	}

	t := newTable(o.stdout, "measure", "value")
	t.add("records", itoa(records))
	t.add("bytes", itoa(bytesIn))
	t.add("elapsed", elapsed.String())
	t.add("MB/s", formatFloat(mbps))
	t.add("ns/record", itoa(nsPerRecord))
	t.add("allocations", itoa(allocs))
	t.add("heap bytes", itoa(int64(after.HeapAlloc))) //nolint:gosec // memory sizes fit
	t.add("malformed", itoa(malformed))
	t.add("truncated", itoa(truncated))
	t.add("buffer grows", itoa(grows))
	t.flush()
	return nil
}

func formatFloat(v float64) string {
	// Two decimals: enough to compare two runs, few enough not to imply
	// precision the measurement does not have.
	const shift = 100
	whole := int64(v)
	frac := int64((v-float64(whole))*shift + 0.5)
	if frac >= shift {
		whole++
		frac -= shift
	}
	s := itoa(whole) + "."
	if frac < 10 {
		s += "0"
	}
	return s + itoa(frac)
}
