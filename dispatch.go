package pglogwatch

// Format dispatch.
//
// This file is the seam between the format-independent machinery (buffer,
// control flow, statistics) and the three format parsers. It holds exactly
// three decisions -- which format, where a record ends, how its fields are
// found -- so that adding csv.go, stderr.go and json.go touches nothing else.
//
// The bodies below are the format-independent parts. Each format fills in its
// own case as it is implemented: csvlog in T039/T042, stderr in T060, jsonlog
// in T072/T075, and automatic detection in T085.

// ensureFormat resolves FormatAuto before the first record is read. It reports
// false if the stream ended before a format could be chosen.
func (p *Parser) ensureFormat() bool {
	if p.ready {
		return true
	}
	if p.format == FormatAuto {
		f, ok := p.detectFormat()
		if !ok {
			return false
		}
		p.format = f
	}
	// stderr needs a prefix before anything can be framed, let alone
	// parsed, so detection happens here rather than lazily per record
	// (FMT-004).
	if p.format == FormatStderr && p.prefix == nil {
		p.prefix = p.detectPrefix()
		p.detectedPrefix = p.prefix.String()
	}
	p.ready = true
	return true
}

// detectFormat inspects the buffered input and picks a destination.
//
// Interim behaviour: FMT-005's real detection -- a leading '{' means jsonlog,
// an ISO-8601 timestamp followed by a comma at a valid column boundary means
// csvlog, otherwise stderr -- arrives with detect.go in T085. Until then the
// parser assumes stderr, which is PostgreSQL's default destination and the
// safest thing to be wrong about, because its parser tolerates arbitrary lines
// rather than rejecting them.
func (p *Parser) detectFormat() (Format, bool) {
	return FormatStderr, true
}

// splitRecord finds the end of the next record for the resolved format. It has
// the shape of a bufio.SplitFunc; see splitFunc for the convention.
func (p *Parser) splitRecord(data []byte, atEOF bool) (int, []byte, error) {
	switch p.format {
	case FormatCSV:
		return splitCSVRecord(data, atEOF, p.cfg.emitTruncatedTail())
	case FormatJSON:
		// jsonlog writes exactly one object per physical line and
		// FMT-006 forbids multi-line assembly, so the line framer is
		// the whole story for this format.
		return splitLine(data, atEOF, p.cfg.emitTruncatedTail())
	default:
		// stderr records absorb continuation lines, which needs a
		// lookahead against the prefix template (T062).
		return splitLine(data, atEOF, p.cfg.emitTruncatedTail())
	}
}

// parseInto fills p.rec from one record's bytes, or returns why it could not.
func (p *Parser) parseInto(rec []byte) error {
	switch p.format {
	case FormatCSV:
		return p.parseCSVInto(rec)
	case FormatJSON:
		return p.parseUnstructured(rec) // replaced in T075
	default:
		return p.parseStderrInto(rec)
	}
}

// parseUnstructured is the skeleton's field scanner: it treats the whole
// record as its message and leaves every other field absent.
//
// It is a real fallback, not only a placeholder -- a record that no format
// parser can decompose is still a line of a log, and losing it entirely would
// break COR-001. Each format replaces this with its own scanner.
func (p *Parser) parseUnstructured(rec []byte) error {
	p.rec.Message = rec
	return nil
}

// resetFormatState clears per-stream parsing state on Reset. Each format adds
// its own state here as it is implemented.
func (p *Parser) resetFormatState() {
	p.scratch = p.scratch[:0]
	p.ready = false
	if p.cfg.LinePrefix == "" {
		// A detected prefix belongs to the stream it was detected from.
		// A configured one is the caller's and survives.
		p.prefix = nil
		p.detectedPrefix = ""
	}
}

// splitLine frames one newline-terminated line, dropping the newline and any
// carriage return before it (COR-006).
//
// emitTail decides what happens to a final line with no trailing newline:
// FMT-009 emits it with FlagTruncated by default, or discards it.
func splitLine(data []byte, atEOF bool, emitTail bool) (int, []byte, error) {
	if i := indexNewline(data); i >= 0 {
		line := trimCR(data[:i])
		if len(line) == 0 {
			// A blank line is not a record. Consume it and ask for
			// the next one rather than emitting an empty record
			// that every caller would have to filter out.
			return i + 1, nil, nil
		}
		return i + 1, line, nil
	}
	if !atEOF {
		return 0, nil, nil
	}
	if len(data) == 0 {
		return 0, nil, nil
	}
	if !emitTail {
		return len(data), nil, nil // consume it, emit nothing
	}
	return len(data), trimCR(data), nil
}
