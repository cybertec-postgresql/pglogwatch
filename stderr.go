package pglogwatch

import (
	"bytes"
	"time"
)

// stderr prefix scanning.
//
// The compiled template from prefix.go is walked against the line: literal
// segments must match, escape segments consume as much as their kind says
// their value occupies. What is left over is the severity and the message.

// scanPrefix matches the template against line and returns the remainder --
// the "SEVERITY:  message" part -- filling r's prefix-derived fields.
//
// A nil r means "match only, assign nothing", which is how the %q handling in
// T059 and the detection scoring in T061 test a candidate without having to
// undo half-assigned fields.
func (t *prefixTemplate) scanPrefix(line []byte, r *Record, tz *tzCache) ([]byte, bool) {
	pos, ok := t.scanSegments(line, 0, 0, t.optionalFrom, r, tz)
	if !ok {
		return nil, false
	}
	if t.optionalFrom >= len(t.segs) {
		return line[pos:], true
	}

	// %q: everything after it is omitted by processes with no session (E5).
	// Whether this line has that part is decided by trying to match it --
	// there is no marker in the line to test.
	//
	// The match is attempted first with a nil Record, which assigns
	// nothing. A single pass that assigned as it went would leave a
	// half-filled record behind on failure, and the fields it had already
	// written -- a user name taken from the word "checkpoint" -- would be
	// wrong rather than merely absent. Undoing that would mean copying the
	// Record before every attempt, which is more work per record than
	// rescanning twenty bytes of prefix.
	if _, ok := t.scanSegments(line, pos, t.optionalFrom+1, len(t.segs), nil, tz); !ok {
		return line[pos:], true
	}
	pos, _ = t.scanSegments(line, pos, t.optionalFrom+1, len(t.segs), r, tz)
	return line[pos:], true
}

// scanSegments walks segs[from:to] against line starting at pos and returns
// the new position.
func (t *prefixTemplate) scanSegments(line []byte, pos, from, to int, r *Record, tz *tzCache) (int, bool) {
	for i := from; i < to; i++ {
		s := &t.segs[i]
		switch {
		case s.esc == 0:
			if !hasPrefix(line[pos:], s.lit) {
				return 0, false
			}
			pos += len(s.lit)
		case s.esc == 'q':
			// A marker, not a value. Handled by the caller.
		default:
			val, consumed, ok := t.scanValue(line, pos, i, to)
			if !ok {
				return 0, false
			}
			if r != nil {
				assignEscape(r, s.esc, val, tz)
			}
			pos += consumed
		}
	}
	return pos, true
}

// scanValue reads the value of the escape at segment i, returning the value
// and how many bytes of line it occupied.
func (t *prefixTemplate) scanValue(line []byte, pos, i, to int) ([]byte, int, bool) {
	s := &t.segs[i]
	b := line[pos:]

	// Right-aligned padding (%5p) puts the fill BEFORE the value, so it has
	// to be stepped over before the value's own shape can be recognised --
	// a digit scanner starting on a space finds nothing.
	lead := 0
	if s.width > 0 {
		for lead < len(b) && b[lead] == ' ' {
			lead++
		}
		b = b[lead:]
	}

	var n int
	var ok bool
	switch s.kind {
	case kindTimestamp:
		_, n, ok = scanTimestamp(b)
	case kindEpoch:
		n, ok = scanEpochLen(b)
	case kindInteger:
		n, ok = scanDigitsLen(b)
	case kindSQLState:
		n, ok = 5, len(b) >= 5
	case kindSessionID:
		n, ok = scanSessionIDLen(b)
	case kindVirtualXID:
		n, ok = scanVirtualXIDLen(b)
	default: // kindText
		n, ok = t.scanTextLen(b, i, to)
	}
	if !ok {
		return nil, 0, false
	}
	val := b[:n]
	consumed := lead + n

	if s.width != 0 {
		// Padding is whitespace the server inserted, not part of the
		// value. A free-form value delimited by the next literal has
		// already swallowed its own fill, so trim rather than assume.
		val = trimSpace(val)
	}
	if s.width < 0 {
		// Left-aligned padding (%-5p) puts the fill AFTER the value.
		// Consume up to the declared width so the next literal segment
		// lines up -- but only up to it: PostgreSQL treats the width as
		// a minimum, so a longer value is written in full and there is
		// no fill to skip.
		for pad := -s.width - n; pad > 0; pad-- {
			if pos+consumed >= len(line) || line[pos+consumed] != ' ' {
				break
			}
			consumed++
		}
	}
	return val, consumed, true
}

// scanTextLen bounds a free-form value, which has no shape of its own: it runs
// to the next literal segment, or to whitespace when no literal follows.
func (t *prefixTemplate) scanTextLen(b []byte, i, to int) (int, bool) {
	for j := i + 1; j < to; j++ {
		if t.segs[j].esc == 'q' {
			continue
		}
		if t.segs[j].esc != 0 {
			break // an escape follows; fall through to whitespace
		}
		k := bytes.Index(b, unsafeBytes(t.segs[j].lit))
		if k < 0 {
			return 0, false
		}
		return k, true
	}
	if k := bytes.IndexByte(b, ' '); k >= 0 {
		return k, true
	}
	return len(b), true
}

func scanDigitsLen(b []byte) (int, bool) {
	n := 0
	for n < len(b) && isDigit(b[n]) {
		n++
	}
	return n, n > 0
}

// scanEpochLen bounds %n, which PostgreSQL writes as seconds.milliseconds.
func scanEpochLen(b []byte) (int, bool) {
	n, ok := scanDigitsLen(b)
	if !ok {
		return 0, false
	}
	if n < len(b) && b[n] == '.' {
		frac, ok := scanDigitsLen(b[n+1:])
		if !ok {
			return 0, false
		}
		n += 1 + frac
	}
	return n, true
}

// scanSessionIDLen bounds %c, which is two hex numbers joined by a dot: the
// backend start time and the process id.
func scanSessionIDLen(b []byte) (int, bool) {
	n := 0
	for n < len(b) && isHexDigit(b[n]) {
		n++
	}
	if n == 0 || n >= len(b) || b[n] != '.' {
		return 0, false
	}
	n++
	start := n
	for n < len(b) && isHexDigit(b[n]) {
		n++
	}
	return n, n > start
}

// scanVirtualXIDLen bounds %v, written as backendID/localXID.
func scanVirtualXIDLen(b []byte) (int, bool) {
	n, ok := scanDigitsLen(b)
	if !ok || n >= len(b) || b[n] != '/' {
		return 0, false
	}
	n++
	start := n
	for n < len(b) && isDigit(b[n]) {
		n++
	}
	return n, n > start
}

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// assignEscape stores one escape's value in the record.
func assignEscape(r *Record, esc byte, val []byte, tz *tzCache) {
	switch esc {
	case 'a':
		r.ApplicationName = val
	case 'u':
		r.User = val
	case 'd':
		r.Database = val
	case 'r', 'h':
		// %r is host and port, %h is host alone. Both describe where the
		// client connected from, and csvlog has one column for it, so
		// they share a field (COR-004).
		r.ConnectionFrom = val
	case 'b':
		r.BackendType = val
	case 'i':
		r.CommandTag = val
	case 'p':
		r.ProcessID, _ = parseInt32(val)
	case 'P':
		r.LeaderPID, _ = parseInt32(val)
	case 'l':
		r.SessionLineNum, _ = parseInt(val)
	case 'x':
		r.TransactionID, _ = parseInt(val)
	case 'Q':
		r.QueryID, _ = parseInt(val)
	case 'm', 't':
		if ts, _, ok := tz.timestamp(val); ok {
			r.Time = ts
		}
	case 's':
		if ts, _, ok := tz.timestamp(val); ok {
			r.SessionStart = ts
		}
	case 'n':
		if ts, ok := epochTime(val); ok {
			r.Time = ts
		}
	case 'e':
		copy(r.SQLState[:], val)
	case 'c':
		r.SessionID = val
	case 'v':
		r.VirtualXID = val
	}
}

// epochTime converts %n's seconds.milliseconds form to a time.Time.
func epochTime(b []byte) (time.Time, bool) {
	secs, frac, hasFrac := bytes.Cut(b, []byte{'.'})
	s, ok := parseInt(secs)
	if !ok {
		return time.Time{}, false
	}
	var nsec int64
	if hasFrac {
		scale := int64(time.Second) / 10
		for _, c := range frac {
			if !isDigit(c) || scale == 0 {
				break
			}
			nsec += int64(c-'0') * scale
			scale /= 10
		}
	}
	return time.Unix(s, nsec).UTC(), true
}

// maxLabelBytes bounds the search for the "SEVERITY:" separator.
//
// Without a bound, a line whose prefix did not match would have its first
// colon -- possibly hundreds of bytes into the message -- read as a severity
// separator, and the record would come back with a message fragment as its
// severity. The longest label this package recognises is far shorter than
// this, and so is the longest localised severity in the tables.
const maxLabelBytes = 32

// splitLabel separates the "SEVERITY:  " or "DETAIL:  " prefix of a message
// from the message itself.
//
// A label is a single word followed by a colon. Requiring the single word is
// what keeps an ordinary message containing a colon -- "duration: 1.5 ms" --
// from being mistaken for a labelled line.
func splitLabel(rest []byte) (label, msg []byte, ok bool) {
	limit := min(len(rest), maxLabelBytes)
	i := bytes.IndexByte(rest[:limit], ':')
	if i <= 0 {
		return nil, rest, false
	}
	label = rest[:i]
	if bytes.IndexByte(label, ' ') >= 0 {
		return nil, rest, false
	}
	msg = rest[i+1:]
	// PostgreSQL writes two spaces after the label.
	for len(msg) > 0 && isSpaceOrTab(msg[0]) {
		msg = msg[1:]
	}
	return label, msg, true
}

// parseStderrInto fills a Record from one framed stderr record, which may span
// several physical lines.
//
// The first line carries the prefix, the severity and the message. Every
// following line is either a labelled continuation, which opens a new field,
// or a wrapped line, which extends whichever field is currently open.
//
// Extending a field costs nothing because the lines are contiguous in the read
// buffer: the field is simply re-sliced to end further along the same record.
// That is what keeps a multi-line record inside PERF-001 -- appending into a
// scratch buffer would allocate for every wrapped statement in the log.
//
// The whole function works in OFFSETS into rec rather than in slices. Slice
// arithmetic on nested sub-slices is how the first version of this got a panic
// out of a perfectly ordinary fixture, and COR-002 does not allow panics.
func (p *Parser) parseStderrInto(rec []byte) error {
	r := &p.rec

	fieldStart, fieldEnd := 0, 0
	open := &r.Message
	first := true

	for start, end := range physicalLines(rec) {
		line := rec[start:end]

		if first {
			first = false
			rest, matched := p.prefixOrNil().scanPrefixOrAll(line, r, &p.tz)
			if !matched {
				// Matches no prefix at all, but it is still a
				// line of the log: emit it with its text as the
				// message rather than discard it (COR-001).
				r.Message = rec
				return nil
			}
			fieldStart, fieldEnd = end-len(rest), end
			if label, msg, hasLabel := splitLabel(rest); hasLabel {
				// RawSeverity is set whether or not the label
				// resolves, so E13 -- the right log read under
				// the wrong MessagesLang -- still hands the
				// caller the original bytes.
				r.RawSeverity = label
				r.Severity = p.sev.resolve(label)
				fieldStart = end - len(msg)
				if field := continuationField(label); field != contNone {
					// Reachable only with
					// SplitContinuations, where a
					// continuation is its own record.
					open = p.continuationTarget(field)
				}
			}
			*open = rec[fieldStart:fieldEnd]
			continue
		}

		r.Flags |= FlagMultiline
		if label, msg, ok := p.labelledContinuation(line); ok {
			open = p.continuationTarget(continuationField(label))
			fieldStart = end - len(msg)
		}
		fieldEnd = end
		*open = rec[fieldStart:fieldEnd]
	}

	if len(r.Statement) > 0 {
		// Keep Query and Statement in step with the csvlog path, where
		// both come from the query column (COR-004).
		r.Query = r.Statement
		r.Flags |= FlagHasStatement
	}
	p.scanRecordDuration()
	return nil
}

// physicalLines iterates the record's lines as (start, end) offsets, with end
// excluding the newline and any carriage return before it (COR-006).
func physicalLines(rec []byte) func(func(int, int) bool) {
	return func(yield func(int, int) bool) {
		pos := 0
		for {
			end := len(rec)
			next := -1
			if i := indexNewline(rec[pos:]); i >= 0 {
				end = pos + i
				next = end + 1
			}
			e := end
			if e > pos && rec[e-1] == '\r' {
				e--
			}
			if !yield(pos, e) || next < 0 {
				return
			}
			pos = next
		}
	}
}

// labelledContinuation reports whether a continuation line carries a
// DETAIL/HINT/STATEMENT/QUERY/CONTEXT label, and returns the label and the
// text after it.
func (p *Parser) labelledContinuation(line []byte) (label, msg []byte, ok bool) {
	if len(line) == 0 || isSpaceOrTab(line[0]) {
		return nil, nil, false
	}
	rest, matched := p.prefixOrNil().scanPrefixOrAll(line, nil, &p.tz)
	if !matched {
		return nil, nil, false
	}
	label, msg, hasLabel := splitLabel(rest)
	if !hasLabel || continuationField(label) == contNone {
		return nil, nil, false
	}
	return label, msg, true
}

// continuationTarget returns the Record field a continuation label fills.
//
// The mapping to csvlog's columns is deliberate, so that the same server
// activity logged either way produces equal records (COR-004): stderr's
// STATEMENT is csvlog's query column, and stderr's QUERY -- the internal query
// of a failing PL/pgSQL statement -- is csvlog's internal_query.
func (p *Parser) continuationTarget(field contField) *[]byte {
	r := &p.rec
	switch field {
	case contDetail:
		return &r.Detail
	case contHint:
		return &r.Hint
	case contStatement:
		// csvlog puts the statement in its query column and this
		// package mirrors it into both Query and Statement, so stderr
		// must fill both too. Filling only one would make the same
		// server activity produce different records depending on which
		// destination was configured, which is exactly what COR-004
		// forbids. Query is re-pointed after the field is written; see
		// mirrorStatement.
		return &r.Statement
	case contQuery:
		return &r.InternalQuery
	case contContext:
		return &r.Context
	}
	return &r.Message
}

// scanRecordDuration fills Duration from the message, shared by every format
// so that a duration is found wherever PostgreSQL wrote it.
//
// Split in two so this half stays inlineable: it is called once per record on
// every format, and the branch that matters -- "is duration parsing even on"
// -- is one boolean. Leaving the scan in the same function pushed the inline
// cost to 90 against a budget of 80, so every record paid a call to decide
// nothing.
func (p *Parser) scanRecordDuration() {
	if p.cfg.NoDuration {
		return
	}
	p.setRecordDuration()
}

func (p *Parser) setRecordDuration() {
	if d, ok := scanDuration(p.rec.Message); ok {
		p.rec.Duration = d
		p.rec.Flags |= FlagHasDuration
	}
}

// prefixOrNil returns the compiled template, which may be nil when none has
// been configured or detected yet.
func (p *Parser) prefixOrNil() *prefixTemplate { return p.prefix }

// scanPrefixOrAll is scanPrefix with a nil receiver meaning "no prefix
// configured", in which case the whole line is the remainder.
func (t *prefixTemplate) scanPrefixOrAll(line []byte, r *Record, tz *tzCache) ([]byte, bool) {
	if t == nil {
		return line, true
	}
	return t.scanPrefix(line, r, tz)
}

// contField names the Record field a continuation label populates.
type contField uint8

const (
	contNone contField = iota
	contDetail
	contHint
	contStatement
	contQuery
	contContext
)

// continuationField maps a PostgreSQL continuation label to the field it
// belongs in.
//
// These are not severities. PostgreSQL writes them in the same position, which
// is why a parser that treats the token before the colon as a severity ends up
// counting DETAIL and HINT as log events (FMT-006).
//
// The labels are NOT localised by lc_messages -- PostgreSQL emits them in
// English regardless -- so unlike severities they need no locale table.
func continuationField(label []byte) contField {
	switch string(label) {
	case "DETAIL":
		return contDetail
	case "HINT":
		return contHint
	case "STATEMENT":
		return contStatement
	case "QUERY":
		return contQuery
	case "CONTEXT":
		return contContext
	}
	return contNone
}

// splitStderrRecord frames one stderr record, absorbing continuation lines.
//
// stderr has no record delimiter: a record ends where the next one begins. The
// framer therefore has to look ahead at the following line and decide whether
// it starts a new record, which is why this needs the compiled prefix while
// csvlog's framer needs nothing.
func (p *Parser) splitStderrRecord(data []byte, atEOF bool) (int, []byte, error) {
	nl := indexNewline(data)
	if nl < 0 {
		if !atEOF {
			return 0, nil, nil
		}
		line := trimCR(data)
		if len(line) == 0 || !p.cfg.emitTruncatedTail() {
			return len(data), nil, nil
		}
		return len(data), line, nil
	}
	if len(trimCR(data[:nl])) == 0 {
		return nl + 1, nil, nil // a blank line is not a record
	}

	recEnd := nl
	pos := nl + 1
	for {
		if pos >= len(data) {
			if !atEOF {
				return 0, nil, nil // need the next line to decide
			}
			break
		}
		i := indexNewline(data[pos:])
		lineEnd := len(data)
		if i >= 0 {
			lineEnd = pos + i
		} else if !atEOF {
			return 0, nil, nil
		}
		if !p.isContinuationLine(trimCR(data[pos:lineEnd])) {
			break
		}
		recEnd = lineEnd
		if i < 0 {
			pos = len(data)
			break
		}
		pos = lineEnd + 1
	}
	return pos, trimCR(data[:recEnd]), nil
}

// isContinuationLine reports whether a line belongs to the record above it.
func (p *Parser) isContinuationLine(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	// E7: PostgreSQL indents the continuation lines of a wrapped statement
	// instead of repeating the prefix. Nothing else in a log starts with
	// whitespace, so this test is both cheap and unambiguous.
	if isSpaceOrTab(line[0]) {
		return true
	}
	if p.prefix == nil {
		return false
	}
	rest, ok := p.prefix.scanPrefix(line, nil, &p.tz)
	if !ok {
		return true // no prefix: part of the record above
	}
	label, _, hasLabel := splitLabel(rest)
	if !hasLabel {
		return true
	}
	if continuationField(label) != contNone {
		return !p.cfg.SplitContinuations
	}
	// A line that carries the prefix and a label that is not a
	// continuation starts a new record -- even when the label is a
	// severity this parser does not recognise. That is what keeps E13, a
	// log read under the wrong MessagesLang, from collapsing into one
	// enormous record.
	return false
}
