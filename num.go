package pglogwatch

import "math"

// Integer parsing over []byte.
//
// strconv.Atoi(string(b)) is the obvious way to do this and is forbidden by
// PERF-006: the conversion allocates once per field, which on a csvlog record
// with six integer columns is six allocations per record. strconv.ParseInt
// would need the same conversion. These two functions are the whole reason
// this file exists.
//
// Both are strict. PostgreSQL writes integer columns as plain decimal digits,
// so leading whitespace, underscores, a leading zero run and a base prefix are
// all treated as malformed rather than silently accepted -- a field that does
// not look like an integer is far more likely to be a mis-split record than a
// creatively formatted number, and reporting it lets FMT-010 counting catch
// the mistake.

// parseUint parses a plain decimal unsigned integer. It reports false for
// empty input, for any non-digit byte, and for overflow.
func parseUint(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var v uint64
	for _, c := range b {
		d := c - '0'
		if d > 9 {
			return 0, false
		}
		// Check before multiplying: v*10+d must not wrap. The division
		// is by a constant, so the compiler turns it into a multiply.
		if v > (math.MaxUint64-uint64(d))/10 {
			return 0, false
		}
		v = v*10 + uint64(d)
	}
	return v, true
}

// parseInt parses a plain decimal signed integer, accepting an optional
// leading '-' or '+'. It reports false for empty input, for a sign with no
// digits after it, for any non-digit byte, and for overflow.
func parseInt(b []byte) (int64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	neg := false
	switch b[0] {
	case '-':
		neg = true
		b = b[1:]
	case '+':
		b = b[1:]
	}
	u, ok := parseUint(b)
	if !ok {
		return 0, false
	}
	if neg {
		if u > uint64(math.MaxInt64)+1 {
			return 0, false
		}
		// At u == 1<<63 the conversion wraps to MinInt64 and negating
		// MinInt64 wraps back to itself, which is the answer we want.
		// This is the same trick strconv.ParseInt uses.
		return -int64(u), true
	}
	if u > math.MaxInt64 {
		return 0, false
	}
	return int64(u), true
}

// parseInt32 parses a signed integer that must fit in an int32, which is the
// width Record uses for process identifiers and cursor positions.
func parseInt32(b []byte) (int32, bool) {
	v, ok := parseInt(b)
	if !ok || v < math.MinInt32 || v > math.MaxInt32 {
		return 0, false
	}
	return int32(v), true
}

// parseDigits parses exactly n decimal digits starting at b[0], which is the
// shape every fixed-layout timestamp field has. Returning the value and
// whether it was well formed lets the timestamp scanner avoid re-slicing.
func parseDigits(b []byte, n int) (int, bool) {
	if len(b) < n {
		return 0, false
	}
	v := 0
	for i := range n {
		d := b[i] - '0'
		if d > 9 {
			return 0, false
		}
		v = v*10 + int(d)
	}
	return v, true
}
