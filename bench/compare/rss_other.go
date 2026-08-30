//go:build !unix

package compare

import "os/exec"

// platformPeakRSSKB reports 0 where the platform cannot supply a peak resident
// set for a finished child -- Windows, notably, where Go's ProcessState carries
// no rusage.
//
// The results table renders 0 as "not measured" rather than as a number, since
// reporting zero would read as "used no memory", which is worse than an
// admitted gap. PERF-026 and PERF-027 are stated against the reference machine
// of §6, which is not Windows.
func platformPeakRSSKB(*exec.Cmd) int64 { return 0 }
