package compare

import (
	"fmt"
	"strings"
)

// Threshold verification (PERF-024, PERF-025, AC-016, AC-017).
//
// The results table already prints "met" or "NOT MET" per workload, but a CI
// job that decided by grepping for a string in a Markdown file would break the
// day someone reworded the table. This turns the same judgement into a value
// with an exit code behind it.
//
// The distinction PERF-025 draws is the interesting part and is preserved here:
// parity with pgweasel BLOCKS a release, while the 1.2x target does not. A
// verifier that treated them the same would either block releases the
// specification allows or wave through ones it does not.

// Severity of a threshold outcome.
type Severity int

const (
	// Pass means the threshold was met.
	Pass Severity = iota
	// Miss means it was measured and not met. This blocks a release.
	Miss
	// Short means a target was missed that does not block a release --
	// PERF-025's 1.2x, specifically.
	Short
	// Unmeasured means no comparison could be made, usually because a
	// baseline is not installed. It blocks a release CLAIM, since VAL-004
	// does not allow a threshold to be assumed met.
	Unmeasured
)

func (s Severity) String() string {
	switch s {
	case Pass:
		return "met"
	case Miss:
		return "NOT MET"
	case Short:
		return "below target"
	default:
		return "not measured"
	}
}

// Finding is one threshold's outcome for one workload.
type Finding struct {
	Requirement string
	Workload    string
	Baseline    string
	Threshold   float64
	Measured    float64
	Severity    Severity
	Detail      string
}

func (f Finding) String() string {
	if f.Severity == Unmeasured {
		return fmt.Sprintf("%s %s vs %s: %s (%s)",
			f.Requirement, f.Workload, f.Baseline, f.Severity, f.Detail)
	}
	return fmt.Sprintf("%s %s vs %s: %.2fx against a %.1fx threshold: %s",
		f.Requirement, f.Workload, f.Baseline, f.Measured, f.Threshold, f.Severity)
}

// Verify evaluates PERF-024 and PERF-025 over a set of results.
func Verify(results []Result) []Finding {
	byKey := map[string]Result{}
	for _, r := range results {
		byKey[r.Workload+"/"+r.Tool] = r
	}

	var findings []Finding
	for _, w := range Workloads("") {
		ours := byKey[w.ID+"/pglogwatch"]

		findings = append(findings,
			evaluate("PERF-024", w.ID, "pgbadger", 10.0, 0, ours, byKey[w.ID+"/pgbadger"]),
			// PERF-025: 1.0x is the release gate, 1.2x the stated
			// target. Missing the target is reported and does not
			// block.
			evaluate("PERF-025", w.ID, "pgweasel", 1.0, 1.2, ours, byKey[w.ID+"/pgweasel"]))
	}
	return findings
}

func evaluate(req, workload, baseline string, gate, target float64, ours, theirs Result) Finding {
	f := Finding{Requirement: req, Workload: workload, Baseline: baseline, Threshold: gate}

	switch {
	case ours.MedianSec <= 0:
		f.Severity, f.Detail = Unmeasured, "pglogwatch not measured: "+reason(ours)
		return f
	case theirs.MedianSec <= 0:
		f.Severity, f.Detail = Unmeasured, baseline+" not measured: "+reason(theirs)
		return f
	}

	f.Measured = theirs.MedianSec / ours.MedianSec
	switch {
	case f.Measured < gate:
		f.Severity = Miss
	case target > 0 && f.Measured < target:
		f.Severity = Short
	default:
		f.Severity = Pass
	}
	return f
}

func reason(r Result) string {
	if r.Skipped != "" {
		return r.Skipped
	}
	return "no timing recorded"
}

// Blocking reports whether any finding prevents a release.
//
// A missed gate blocks, and so does an unmeasured one: VAL-004 does not allow a
// threshold to be assumed met, and "we could not check" is not "it passed". A
// missed 1.2x target does not block, which is what PERF-025 says.
func Blocking(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Severity == Miss || f.Severity == Unmeasured {
			out = append(out, f)
		}
	}
	return out
}

// Summarise renders findings for a CI log.
func Summarise(findings []Finding) string {
	var sb strings.Builder
	counts := map[Severity]int{}
	for _, f := range findings {
		counts[f.Severity]++
		fmt.Fprintf(&sb, "  %s\n", f)
	}
	fmt.Fprintf(&sb, "\n%d met, %d not met, %d below target, %d not measured\n",
		counts[Pass], counts[Miss], counts[Short], counts[Unmeasured])
	if n := len(Blocking(findings)); n > 0 {
		fmt.Fprintf(&sb,
			"\n%d finding(s) block a release claim. VAL-010 requires the release\n"+
				"notes to state the measured value, the cause and the remediation\n"+
				"plan for each -- not a relaxed threshold.\n", n)
	}
	return sb.String()
}
