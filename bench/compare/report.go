package compare

import (
	"fmt"
	"io"
	"strings"
)

// The results table (TST-013).
//
// Every column the specification asks for is here -- tool version, corpus
// version, machine spec, format, input size, wall clock, MB/s, peak RSS and
// output size -- and one it does not: what each tool actually produced. TST-012
// requires that, and it is the difference between a comparison and a
// scoreboard.

// WriteReport renders the results as Markdown.
func WriteReport(w io.Writer, env Environment, tools []Tool, results []Result) error {
	var sb strings.Builder

	sb.WriteString("# pglogwatch comparative benchmark\n\n")
	writeEnvironment(&sb, env, tools)
	writeResults(&sb, results)
	writeRatios(&sb, results)
	writeGaps(&sb, results)

	_, err := io.WriteString(w, sb.String())
	return err
}

func writeEnvironment(sb *strings.Builder, env Environment, tools []Tool) {
	sb.WriteString("## Environment\n\n")
	sb.WriteString("| item | value |\n|---|---|\n")
	machine := env.Machine
	if machine == "" {
		machine = "(unnamed; set PGLOGWATCH_BENCH_MACHINE_NAME)"
	}
	fmt.Fprintf(sb, "| machine | %s |\n", machine)
	fmt.Fprintf(sb, "| platform | %s/%s, %d CPUs |\n", env.OS, env.Arch, env.CPUs)
	fmt.Fprintf(sb, "| Go | %s |\n", env.Go)
	fmt.Fprintf(sb, "| corpus | %s |\n", env.Corpus)
	for _, t := range tools {
		v := t.Version
		if !t.Found {
			v = "**not installed**"
		}
		fmt.Fprintf(sb, "| %s | %s |\n", t.Name, v)
	}
	sb.WriteString("\nThe machine specification is pinned in `bench/MACHINE.md` (TST-014) " +
		"and must be reproduced verbatim in any published claim.\n\n")
}

func writeResults(sb *strings.Builder, results []Result) {
	sb.WriteString("## Results\n\n")
	sb.WriteString("Median of the configured run count; peak RSS is the maximum observed " +
		"across runs (TST-011).\n\n")
	sb.WriteString("| workload | tool | input | median | MB/s | peak RSS | produces |\n")
	sb.WriteString("|---|---|---:|---:|---:|---:|---|\n")

	workloadNames := map[string]Workload{}
	for _, w := range Workloads("") {
		workloadNames[w.ID] = w
	}

	for _, r := range results {
		w := workloadNames[r.Workload]
		produces := w.Produces[r.Tool]
		if r.Skipped != "" {
			fmt.Fprintf(sb, "| %s %s | %s | | | | | _%s_ |\n",
				r.Workload, w.Name, r.Tool, r.Skipped)
			continue
		}
		fmt.Fprintf(sb, "| %s %s | %s | %s | %s s | %s | %s | %s |\n",
			r.Workload, w.Name, r.Tool,
			humanBytes(r.BytesIn),
			formatSeconds(r.MedianSec),
			formatMBps(r.MBPerSecond()),
			humanRSS(r.PeakRSSKB),
			produces)
	}
	sb.WriteString("\n")
}

// writeRatios states the thresholds explicitly, per workload.
//
// A table of raw timings leaves the reader to divide, and a threshold nobody
// evaluates is a threshold nobody meets.
func writeRatios(sb *strings.Builder, results []Result) {
	sb.WriteString("## Thresholds\n\n")
	sb.WriteString("PERF-024 requires at least 10x pgbadger. PERF-025 requires at least " +
		"parity with pgweasel, targeting 1.2x; parity blocks release, 1.2x does not.\n\n")
	sb.WriteString("| workload | vs pgbadger | PERF-024 | vs pgweasel | PERF-025 |\n")
	sb.WriteString("|---|---:|---|---:|---|\n")

	byKey := map[string]Result{}
	for _, r := range results {
		byKey[r.Workload+"/"+r.Tool] = r
	}
	for _, w := range Workloads("") {
		ours := byKey[w.ID+"/pglogwatch"]
		if ours.MedianSec <= 0 {
			fmt.Fprintf(sb, "| %s | | _not measured_ | | _not measured_ |\n", w.ID)
			continue
		}
		badger := ratio(ours, byKey[w.ID+"/pgbadger"])
		weasel := ratio(ours, byKey[w.ID+"/pgweasel"])
		fmt.Fprintf(sb, "| %s | %s | %s | %s | %s |\n",
			w.ID,
			ratioText(badger), verdict(badger, 10.0),
			ratioText(weasel), verdict(weasel, 1.0))
	}
	sb.WriteString("\n")
}

// ratio is how many times faster pglogwatch was. A negative value means the
// comparison could not be made.
func ratio(ours, theirs Result) float64 {
	if ours.MedianSec <= 0 || theirs.MedianSec <= 0 {
		return -1
	}
	return theirs.MedianSec / ours.MedianSec
}

func ratioText(v float64) string {
	if v < 0 {
		return ""
	}
	return formatMBps(v) + "x"
}

func verdict(v, threshold float64) string {
	if v < 0 {
		return "_not measured_"
	}
	if v >= threshold {
		return "met"
	}
	// Stated as a failure rather than softened. VAL-010 requires the
	// measured value, the cause and a remediation plan in the release
	// notes; this table's job is to make the shortfall impossible to miss.
	return "**NOT MET**"
}

// writeGaps lists what could not be measured, so an incomplete run cannot be
// mistaken for a complete one.
func writeGaps(sb *strings.Builder, results []Result) {
	var gaps []string
	for _, r := range results {
		if r.Skipped != "" {
			gaps = append(gaps, fmt.Sprintf("- %s / %s: %s", r.Workload, r.Tool, r.Skipped))
		}
	}
	if len(gaps) == 0 {
		return
	}
	sb.WriteString("## Not measured\n\n")
	sb.WriteString("These cells are empty above. A table with gaps is not a table " +
		"showing zeros, and a published claim drawn from it must say which " +
		"comparisons were actually made.\n\n")
	sb.WriteString(strings.Join(gaps, "\n"))
	sb.WriteString("\n")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return formatMBps(float64(n)/(1<<30)) + " GB"
	case n >= 1<<20:
		return formatMBps(float64(n)/(1<<20)) + " MB"
	case n >= 1<<10:
		return formatMBps(float64(n)/(1<<10)) + " KB"
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func humanRSS(kb int64) string {
	if kb <= 0 {
		return "_not measured_"
	}
	return humanBytes(kb * 1024)
}
