package pglogwatch

import "unicode/utf8"

// AppendUnquoted appends src to dst with its source-level escaping removed,
// and returns the extended slice.
//
// Unescaping is deferred rather than done during parsing (PERF-009, PAT-003):
// the parser cannot know whether a caller counting severities will ever look
// at a message, so it sets [FlagNeedsUnquote] and leaves the work -- and the
// allocation -- to whoever actually needs the text. Pass a buffer you own and
// reuse it:
//
//	var buf []byte
//	for p.Next() {
//	    r := p.Record()
//	    msg := r.Message
//	    if r.Flags&pglogwatch.FlagNeedsUnquote != 0 {
//	        buf = pglogwatch.AppendUnquoted(buf[:0], r.Message, p.DetectedFormat())
//	        msg = buf
//	    }
//	    use(msg)
//	}
//
// The format argument is not in the specification's signature and is not
// optional in practice: csvlog escapes a quote by doubling it and leaves
// backslashes alone, while jsonlog uses backslash escapes and cannot contain a
// bare quote at all. A single function that guessed would corrupt every
// Windows path and every regular expression logged by a csvlog server, so the
// caller states which grammar applies. FormatStderr and FormatAuto copy src
// unchanged, since stderr output carries no escaping.
func AppendUnquoted(dst, src []byte, format Format) []byte {
	switch format {
	case FormatCSV:
		return appendUnquotedCSV(dst, src)
	case FormatJSON:
		return appendUnquotedJSON(dst, src)
	default:
		return append(dst, src...)
	}
}

// appendUnquotedCSV collapses each doubled quote into one.
//
// PostgreSQL's CSV writer escapes nothing else: a backslash, a newline and a
// comma inside a quoted field are all written literally.
func appendUnquotedCSV(dst, src []byte) []byte {
	for i := 0; i < len(src); i++ {
		c := src[i]
		dst = append(dst, c)
		if c == '"' && i+1 < len(src) && src[i+1] == '"' {
			i++
		}
	}
	return dst
}

// appendUnquotedJSON resolves JSON string escapes.
//
// An invalid escape is copied through verbatim rather than dropped or replaced.
// COR-005 requires invalid UTF-8 to survive unchanged, and the same reasoning
// applies here: this function's job is to undo PostgreSQL's escaping, not to
// judge the log's contents.
func appendUnquotedJSON(dst, src []byte) []byte {
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c != '\\' || i+1 >= len(src) {
			dst = append(dst, c)
			continue
		}
		i++
		switch src[i] {
		case '"':
			dst = append(dst, '"')
		case '\\':
			dst = append(dst, '\\')
		case '/':
			dst = append(dst, '/')
		case 'b':
			dst = append(dst, '\b')
		case 'f':
			dst = append(dst, '\f')
		case 'n':
			dst = append(dst, '\n')
		case 'r':
			dst = append(dst, '\r')
		case 't':
			dst = append(dst, '\t')
		case 'u':
			r, n, ok := decodeUnicodeEscape(src[i-1:])
			if !ok {
				dst = append(dst, '\\', src[i])
				continue
			}
			dst = utf8.AppendRune(dst, r)
			i += n - 2 // the loop's i++ accounts for one more
		default:
			// Not an escape PostgreSQL emits. Keep both bytes so the
			// original text is recoverable.
			dst = append(dst, '\\', src[i])
		}
	}
	return dst
}

// decodeUnicodeEscape decodes a \uXXXX sequence at the front of b, joining a
// surrogate pair when one follows, and reports how many bytes it consumed.
func decodeUnicodeEscape(b []byte) (rune, int, bool) {
	hi, ok := hex4(b)
	if !ok {
		return 0, 0, false
	}
	// A high surrogate is only half a character; PostgreSQL's JSON writer
	// emits pairs for anything outside the basic multilingual plane.
	if hi >= 0xD800 && hi <= 0xDBFF && len(b) >= 12 && b[6] == '\\' && b[7] == 'u' {
		if lo, ok := hex4(b[6:]); ok && lo >= 0xDC00 && lo <= 0xDFFF {
			return rune(0x10000 + (hi-0xD800)<<10 + (lo - 0xDC00)), 12, true
		}
	}
	// A lone surrogate is not a valid rune. utf8.AppendRune would replace
	// it with U+FFFD, so return the replacement explicitly rather than
	// letting it happen silently.
	if hi >= 0xD800 && hi <= 0xDFFF {
		return utf8.RuneError, 6, true
	}
	return rune(hi), 6, true
}

// hex4 decodes the four hex digits of a \uXXXX sequence at b[2:6].
func hex4(b []byte) (uint32, bool) {
	if len(b) < 6 || b[0] != '\\' || b[1] != 'u' {
		return 0, false
	}
	var v uint32
	for _, c := range b[2:6] {
		var d uint32
		switch {
		case c >= '0' && c <= '9':
			d = uint32(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint32(c-'A') + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	return v, true
}
