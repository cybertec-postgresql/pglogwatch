//go:build unix && !linux && !darwin

package compare

// normaliseMaxRSS converts ru_maxrss to kilobytes. The BSDs follow Linux here.
func normaliseMaxRSS(v int64) int64 { return v }
