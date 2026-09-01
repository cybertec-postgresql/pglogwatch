//go:build purego

package pglogwatch

// Copying fallback for builds that forbid unsafe (PKG-007). It allocates where
// the unsafe.go version does not, so a purego build does not satisfy PERF-001
// on any path that uses it. That is the intended trade: correctness and
// portability first, and the allocation gates are skipped under this tag rather
// than being quietly weakened for everyone.

// unsafeBytes returns a copy of s as a []byte.
func unsafeBytes(s string) []byte { return []byte(s) }
