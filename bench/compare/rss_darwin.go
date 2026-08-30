//go:build darwin

package compare

// normaliseMaxRSS converts ru_maxrss to kilobytes. macOS reports it in bytes.
func normaliseMaxRSS(v int64) int64 { return v / 1024 }
