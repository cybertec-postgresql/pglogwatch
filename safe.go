//go:build purego

package pglogwatch

// Copying fallbacks for builds that forbid unsafe (PKG-007). These allocate
// where the unsafe.go versions do not, so a purego build does not satisfy
// PERF-001 on any path that uses them. That is the intended trade: correctness
// and portability first, and the allocation gates are skipped under this tag
// rather than being quietly weakened for everyone.

// unsafeString returns a copy of b as a string.
func unsafeString(b []byte) string { return string(b) }

// unsafeBytes returns a copy of s as a []byte.
func unsafeBytes(s string) []byte { return []byte(s) }
