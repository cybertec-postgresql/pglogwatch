package pglogwatch

// Format auto-detection (FMT-005).
//
// The rule is deliberately narrow, because the cost of guessing wrong is not
// an error: a stderr log parsed as csvlog produces records whose every field is
// shifted, and a caller has no way to tell. Detection therefore looks for
// positive evidence of csvlog and jsonlog, and falls back to stderr -- which is
// both PostgreSQL's default destination and the format whose parser tolerates
// anything.

// detectFormat inspects the buffered input and picks a destination.
//
// It reports false only when the stream ends before any non-empty line is
// available, in which case there is nothing to parse either.
func (p *Parser) detectFormat() (Format, bool) {
	sample := p.buf.peek(detectPeekBytes)
	line, ok := firstNonEmptyLine(sample)
	if !ok {
		return FormatAuto, false
	}
	return classifyLine(line), true
}

// firstNonEmptyLine returns the first line with content.
//
// FMT-005 says "the first non-empty line", not "the first line". A log can
// begin with a blank one -- log_truncate_on_rotation leaves a file that way,
// and so does a torn write -- and detecting from it would classify the whole
// file as stderr.
func firstNonEmptyLine(sample []byte) ([]byte, bool) {
	for len(sample) > 0 {
		i := indexNewline(sample)
		if i < 0 {
			if line := trimCR(sample); len(line) > 0 {
				return line, true
			}
			return nil, false
		}
		if line := trimCR(sample[:i]); len(line) > 0 {
			return line, true
		}
		sample = sample[i+1:]
	}
	return nil, false
}

// classifyLine applies FMT-005's test to one line.
func classifyLine(line []byte) Format {
	if line[0] == '{' {
		// jsonlog. No other destination can begin a line with a brace:
		// csvlog begins with a timestamp, and stderr begins with
		// whatever log_line_prefix starts with, which cannot be '{'
		// without the prefix also being unparseable.
		return FormatJSON
	}
	if looksLikeCSVLine(line) {
		return FormatCSV
	}
	return FormatStderr
}

// looksLikeCSVLine reports whether a line is a csvlog record.
//
// FMT-005 requires an ISO-8601 timestamp FOLLOWED BY A COMMA AT A VALID CSV
// COLUMN BOUNDARY -- not merely a leading timestamp. The distinction is the
// whole test: every stderr log written with %m or %t also starts with a
// timestamp, so the weaker rule would classify most of the world's PostgreSQL
// logs as csvlog and hand back records with every field shifted.
//
// "At a valid column boundary" is checked by counting the record's columns and
// requiring at least the 23 that every supported layout has (FMT-001). A
// stderr line contains commas only inside its message, which never yields 23
// of them at column boundaries.
func looksLikeCSVLine(line []byte) bool {
	_, n, ok := scanTimestamp(line)
	if !ok {
		return false
	}
	if n >= len(line) || line[n] != ',' {
		return false
	}
	var fields [maxCSVColumns][]byte
	columns, _, err := splitCSVFields(line, &fields)
	if err != nil {
		return false
	}
	if !csvLayoutIsKnown(columns) {
		return false
	}
	// One more piece of evidence, cheap and decisive: column 12 of a csvlog
	// record is the severity. A stderr line that somehow reached 23 columns
	// will not have a severity sitting in that exact position.
	sev := fields[csvErrorSeverity]
	return len(sev) > 0 && len(sev) <= 32
}
