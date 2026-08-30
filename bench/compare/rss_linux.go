//go:build linux

package compare

// normaliseMaxRSS converts ru_maxrss to kilobytes. Linux already reports it in
// kilobytes.
func normaliseMaxRSS(v int64) int64 { return v }
