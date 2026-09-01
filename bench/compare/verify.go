package compare

import (
	"fmt"
	"strings"
)

// Threshold verification (PERF-024, PERF-025, AC-016, AC-017).
//
// The results table already prints an outcome per workload, but a CI job that
// decided by grepping for a string in a Markdown file would break the day
// someone reworded the table. This turns the same judgement into a value.
//
// Neither ratio blocks a release any more. PERF-024 and PERF-025 were amended
// on 2026-09-01 to be measured-and-published rather than gating, because a
// comparative ratio moves when a third-party tool changes -- pgweasel's Rust
// rewrite made it 5.3x faster than its own Go predecessor, and the old wording
// turned that into a release blocker for this project -- and because three of
// the five workloads cannot be compared at all, pgweasel 0.1 having no `stats`
// subcommand.
//
// So the outcomes are still computed, still reported, and still distinguish a
// miss from a workload that could not be measured. What changed is Blocking,
// which now returns nothing from these two requirements. The distinction is
// worth keeping rather than deleting the code: an unmeasurable workload and a
// measured loss are different facts, and the release notes have to say which
// is which.

// Severity of a threshold outcome.
type Severity int

const (
	// Pass means the ratio reached its figure.
	Pass Severity = iota
	// Miss means it was measured and fell short. Reported, not blocking.
	Miss
	// Short means the 1.2x target was missed while parity was reached.
	Short
	// Unmeasured means no comparison could be made, usually because a
	// baseline is not installed or does not implement the workload. VAL-004
	// still forbids reporting such a workload as met, which is why this is
	// a distinct value rather than an absence.
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
			// PERF-025 names two figures: parity, and a 1.2x
			// target. Both are reported; neither blocks.
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
// Since the 2026-09-01 amendment, none of them can: PERF-024 and PERF-025 are
// measured and published rather than gating, and they are the only
// requirements this package evaluates. The function is kept, returning nothing,
// because the caller's shape is right and a future gating requirement -- one
// measured against this code rather than against somebody else's -- would go
// here.
//
// It is deliberately not "return nil". Filtering an empty set from real
// findings makes the reason visible at the call site and in the tests, where a
// bare nil would read as an oversight.
func Blocking(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if blocks(f) {
			out = append(out, f)
		}
	}
	return out
}

// blocks reports whether one finding is a release blocker.
//
// Nothing PERF-024 or PERF-025 can produce is. A miss is a real measurement
// worth publishing, and an unmeasured workload is a fact about the baseline --
// pgweasel 0.1 does not implement `stats` -- rather than about this code.
func blocks(f Finding) bool {
	switch f.Requirement {
	case "PERF-024", "PERF-025":
		return false
	}
	return f.Severity == Miss || f.Severity == Unmeasured
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
