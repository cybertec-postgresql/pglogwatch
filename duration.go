package pglogwatch

import "time"

// Statement duration extraction.
//
// PostgreSQL reports a statement's duration in the message text rather than in
// a column of its own, in every destination:
//
//	duration: 1234.567 ms  statement: SELECT * FROM orders
//	duration: 0.043 ms  bind <unnamed>: SELECT $1
//	duration: 12.000 ms
//
// Record.Duration exists so that the CLI's slow subcommand, and anyone else
// looking for slow statements, does not have to re-scan every message.

const durationPrefix = "duration: "

// scanDuration extracts a duration from the front of a log message.
//
// It only looks at the START of the message, after leading spaces. PostgreSQL
// always puts it there, and searching the whole message would cost a scan over
// every message body in the log -- on the severity-histogram workload that is
// most of the file, for a field that workload never reads. A message that
// mentions a duration further along is left alone.
func scanDuration(msg []byte) (time.Duration, bool) {
	i := 0
	for i < len(msg) && isSpaceOrTab(msg[i]) {
		i++
	}
	b := msg[i:]
	if !hasPrefix(b, durationPrefix) {
		return 0, false
	}
	b = b[len(durationPrefix):]

	// Milliseconds, with an optional fractional part. Accumulated directly
	// in nanoseconds so there is no float involved: 1 ms is 1e6 ns, so the
	// first fractional digit is worth 1e5 ns.
	var ns int64
	n := 0
	for n < len(b) && isDigit(b[n]) {
		ns = ns*10 + int64(b[n]-'0')
		n++
	}
	if n == 0 {
		return 0, false
	}
	ns *= int64(time.Millisecond)

	if n < len(b) && b[n] == '.' {
		n++
		scale := int64(time.Millisecond) / 10
		for n < len(b) && isDigit(b[n]) {
			if scale > 0 {
				ns += int64(b[n]-'0') * scale
				scale /= 10
			}
			n++
		}
	}

	// The unit must actually be there. Without this check, a message
	// beginning "duration: 5 rows" would be read as five milliseconds.
	if !hasPrefix(b[n:], " ms") {
		return 0, false
	}
	return time.Duration(ns), true
}
