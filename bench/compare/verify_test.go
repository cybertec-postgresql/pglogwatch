package compare

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// result is a measured cell; skipped is one the harness could not fill.
func result(workload, tool string, sec float64) Result {
	return Result{Workload: workload, Tool: tool, MedianSec: sec, BytesIn: 61 << 20}
}

func skipped(workload, tool, why string) Result {
	return Result{Workload: workload, Tool: tool, Skipped: why}
}

func findingFor(t *testing.T, findings []Finding, req, workload string) Finding {
	t.Helper()
	for _, f := range findings {
		if f.Requirement == req && f.Workload == workload {
			return f
		}
	}
	t.Fatalf("no %s finding for %s", req, workload)
	return Finding{}
}

func TestVerifyClassifiesEachOutcome(t *testing.T) {
	findings := Verify([]Result{
		// W1: comfortably ahead of both.
		result("W1", "pglogwatch", 0.089),
		result("W1", "pgbadger", 11.274),
		result("W1", "pgweasel", 0.500),
		// W3: measured, and slower than pgweasel. This is the real
		// W3 measurement, at 0.78x.
		result("W3", "pglogwatch", 0.090),
		result("W3", "pgbadger", 11.255),
		result("W3", "pgweasel", 0.071),
		// W4: ahead of pgweasel but short of the 1.2x target.
		result("W4", "pglogwatch", 0.100),
		result("W4", "pgweasel", 0.110),
		// W2: pgweasel 0.1 has no stats subcommand, so there is
		// nothing to compare against.
		result("W2", "pglogwatch", 0.114),
		skipped("W2", "pgweasel", "stats subcommand not implemented"),
	})

	assert.Equal(t, Pass, findingFor(t, findings, "PERF-024", "W1").Severity)
	assert.Equal(t, Miss, findingFor(t, findings, "PERF-025", "W3").Severity)
	assert.Equal(t, Short, findingFor(t, findings, "PERF-025", "W4").Severity)
	assert.Equal(t, Unmeasured, findingFor(t, findings, "PERF-025", "W2").Severity)

	// The ratio itself must be right, since it is what gets published.
	assert.InDelta(t, 0.79, findingFor(t, findings, "PERF-025", "W3").Measured, 0.01)
}

// TestComparativeRatiosDoNotBlock pins the 2026-09-01 amendment.
//
// PERF-024 and PERF-025 used to gate a release: a measured loss blocked, and so
// did a workload nobody could measure. Both now report only. If this test ever
// fails, someone has restored a release gate on a number that moves when a
// third-party tool changes -- which is the thing the amendment removed.
func TestComparativeRatiosDoNotBlock(t *testing.T) {
	findings := Verify([]Result{
		result("W3", "pglogwatch", 0.090),
		result("W3", "pgweasel", 0.071), // a measured loss
		result("W2", "pglogwatch", 0.114),
		skipped("W2", "pgweasel", "stats subcommand not implemented"),
	})

	// The outcomes are still computed and still distinguish the two cases.
	require.Equal(t, Miss, findingFor(t, findings, "PERF-025", "W3").Severity)
	require.Equal(t, Unmeasured, findingFor(t, findings, "PERF-025", "W2").Severity)

	// And neither blocks.
	assert.Empty(t, Blocking(findings),
		"PERF-024 and PERF-025 are measured and published, not gating")
}
