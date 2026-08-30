//go:build unix

package compare

import (
	"os/exec"
	"syscall"
)

// platformPeakRSSKB reads the child's peak resident set from wait4 accounting.
//
// ru_maxrss is in kilobytes on Linux and in BYTES on macOS, which is a
// long-standing difference that silently makes macOS numbers 1024 times too
// large if you do not account for it.
func platformPeakRSSKB(cmd *exec.Cmd) int64 {
	usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	return normaliseMaxRSS(int64(usage.Maxrss))
}
