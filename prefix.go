package pglogwatch

import "strings"

// log_line_prefix compilation (PAT-002, FMT-003).
//
// A prefix is compiled once into a slice of segments and then scanned
// linearly. The alternative -- building a regular expression from the prefix,
// which is what pgwatch does today -- costs a submatch slice per line and
// rules out zero-allocation parsing entirely.
//
// A segment is either literal text or one escape. Scanning walks the segments
// against the line; how far each escape's value extends is decided by the
// escape's own kind, not by looking for the next literal, because several
// escapes contain the characters that separate them:
//
//	%t   "2026-08-30 10:11:12 CEST"   contains three spaces
//	%r   "10.0.0.5(52344)"            contains punctuation
//
// A prefix of "%t " with a value-then-delimiter scan would stop at the first
// space and read the date alone as the timestamp, then try to parse the time
// as a severity. Every escape therefore declares how to find its own end.

// escapeKind says how an escape's value delimits itself.
type escapeKind uint8

const (
	// kindText has no self-delimiting shape, so it runs to the next
	// literal segment, or to whitespace when no literal follows.
	kindText escapeKind = iota
	kindInteger
	kindTimestamp // %m, %t, %s
	kindEpoch     // %n: seconds.milliseconds since the epoch
	kindSQLState  // %e: exactly five characters
	kindSessionID // %c: hex.hex
	kindVirtualXID
)

// prefixSegment is one piece of a compiled prefix: literal text, or an escape.
type prefixSegment struct {
	lit   string     // literal text; empty for an escape
	esc   byte       // the escape letter; 0 for a literal
	kind  escapeKind // how to find the escape's end
	width int        // padding width; negative means left-aligned
}

// prefixTemplate is a compiled log_line_prefix.
type prefixTemplate struct {
	src  string
	segs []prefixSegment

	// optionalFrom is the index of the first segment that %q makes
	// conditional, or len(segs) when the prefix has no %q. Everything from
	// there is omitted by processes with no session (E5).
	optionalFrom int
}

// escapeKindOf maps an escape letter to how its value ends, and reports
// whether the letter is an escape this package knows.
//
// An unknown escape is rejected at compile time rather than skipped. Skipping
// would misalign every segment after it, and the resulting record would be
// confidently wrong -- a user name holding a severity, a message holding half
// a prefix -- rather than visibly incomplete.
func escapeKindOf(c byte) (escapeKind, bool) {
	switch c {
	case 'a', 'u', 'd', 'r', 'h', 'b', 'i':
		return kindText, true
	case 'p', 'P', 'l', 'x', 'Q':
		return kindInteger, true
	case 'm', 't', 's':
		return kindTimestamp, true
	case 'n':
		return kindEpoch, true
	case 'e':
		return kindSQLState, true
	case 'c':
		return kindSessionID, true
	case 'v':
		return kindVirtualXID, true
	}
	return 0, false
}

// compilePrefix compiles a log_line_prefix into a template.
//
// It is called once per parser, so clarity beats speed here; the scanning side
// is where the hot path lives.
func compilePrefix(format string) (*prefixTemplate, error) {
	t := &prefixTemplate{src: format}
	var lit strings.Builder

	flushLiteral := func() {
		if lit.Len() > 0 {
			t.segs = append(t.segs, prefixSegment{lit: lit.String()})
			lit.Reset()
		}
	}

	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			lit.WriteByte(format[i])
			continue
		}
		i++
		if i >= len(format) {
			// A trailing '%' is not an escape. Keep it as literal
			// text rather than failing: it is far more likely to be
			// a typo in a working configuration than a reason to
			// refuse to parse the log.
			lit.WriteByte('%')
			break
		}

		// Padding: an optional minus for left alignment, then digits.
		width := 0
		neg := false
		if format[i] == '-' {
			neg = true
			i++
		}
		start := i
		for i < len(format) && isDigit(format[i]) {
			width = width*10 + int(format[i]-'0')
			i++
		}
		if i >= len(format) {
			return nil, ErrBadLinePrefix
		}
		if i == start && neg {
			// "%-x" with no digits is not a padding form.
			return nil, ErrBadLinePrefix
		}
		if neg {
			width = -width
		}

		switch c := format[i]; c {
		case '%':
			lit.WriteByte('%')
		case 'q':
			// Not a value: a marker saying that everything after it
			// is omitted by non-session processes.
			flushLiteral()
			t.optionalFrom = len(t.segs)
			t.segs = append(t.segs, prefixSegment{esc: 'q'})
		default:
			kind, ok := escapeKindOf(c)
			if !ok {
				return nil, ErrBadLinePrefix
			}
			flushLiteral()
			t.segs = append(t.segs, prefixSegment{esc: c, kind: kind, width: width})
		}
	}
	flushLiteral()

	if t.optionalFrom == 0 && !t.hasQ() {
		t.optionalFrom = len(t.segs)
	}
	return t, nil
}

func (t *prefixTemplate) hasQ() bool {
	for _, s := range t.segs {
		if s.esc == 'q' {
			return true
		}
	}
	return false
}

// String returns the log_line_prefix this template was compiled from, which is
// what Parser.DetectedPrefix reports.
func (t *prefixTemplate) String() string {
	if t == nil {
		return ""
	}
	return t.src
}
