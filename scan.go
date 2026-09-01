package pglogwatch

// Byte scanning primitives.
//
// PERF-004 forbids regexp anywhere the parser can reach. The existing pgwatch
// implementation splits a csvlog line with a regex built from non-greedy
// groups and optional quote handling, which is close to the slowest available
// way to do it, and still only covers the first 12 of 23 to 26 columns. Every
// scan in this package is a hand-written walk driven by bytes.IndexByte, which
// compiles to an assembly routine using SIMD on every platform this module
// targets.
//
// The helpers are small and side-effect free so the inliner will take them
// (GUD-002); anything that stops being inlined shows up as a regression in the
// benchmarks rather than as a silent slowdown.

// trimCR removes a single trailing carriage return.
//
// COR-006 requires that a CRLF log never leaks a '\r' into a field value. The
// framer strips it from the end of each physical line, which is the only place
// PostgreSQL can put one, so a '\r' inside a message is left alone -- it is
// data, not a line ending.
func trimCR(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return b[:n-1]
	}
	return b
}

// trimSpace removes ASCII spaces and tabs from both ends.
//
// Deliberately not bytes.TrimSpace, which also trims vertical tab, form feed
// and newline. A newline inside a field is significant here: it is what makes
// a record multi-line, and silently eating it would hide E2.
func trimSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	j := len(b)
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

// hasPrefix reports whether b starts with s.
//
// Taking the prefix as a string rather than a []byte lets callers pass a
// literal without a conversion, and the comparison itself compiles to a length
// check plus memequal with no copy.
func hasPrefix(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}

// isSpaceOrTab reports whether c is an ASCII space or tab. Continuation-line
// detection in the stderr parser turns on this test (E7).
func isSpaceOrTab(c byte) bool { return c == ' ' || c == '\t' }
