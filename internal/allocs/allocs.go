// Package allocs is the shared allocation gate for pglogwatch's tests.
//
// PERF-001 requires steady-state parsing to perform zero heap allocations per
// record, and AC-011 requires that to be demonstrated with
// testing.AllocsPerRun over the corpus. Wrapping it here rather than calling
// it directly in each test keeps three easy mistakes out of every gate:
// forgetting to warm up before measuring, reporting a float difference as an
// unreadable message, and running the gate under build configurations where
// allocation counts are not meaningful.
package allocs

import "testing"

// Zero asserts that f performs no heap allocations per run.
//
// f is called once before measurement so that one-time setup -- buffer growth,
// timezone resolution, prefix compilation -- is excluded, which is exactly the
// "after warm-up" wording in PERF-001's definition of steady state.
func Zero(t testing.TB, runs int, f func()) {
	t.Helper()
	if !Meaningful {
		t.Skip("allocation counts are not meaningful in this build")
	}
	f() // warm up: grow buffers and fill caches before counting
	if n := testing.AllocsPerRun(runs, f); n != 0 {
		t.Errorf("got %.1f allocations per run, want 0", n)
	}
}

// AtMost asserts that f performs no more than want allocations per run. Use it
// only where the specification sanctions allocation, such as Record.Clone,
// which PERF-003 caps at two.
func AtMost(t testing.TB, runs int, want float64, f func()) {
	t.Helper()
	if !Meaningful {
		t.Skip("allocation counts are not meaningful in this build")
	}
	f()
	if n := testing.AllocsPerRun(runs, f); n > want {
		t.Errorf("got %.1f allocations per run, want at most %.0f", n, want)
	}
}
