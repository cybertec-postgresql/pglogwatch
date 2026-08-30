package pglogwatch

// jsonlog framing.
//
// FMT-006 is unusually specific about this format: PostgreSQL emits exactly one
// JSON object per physical line, and the parser MUST NOT attempt multi-line
// assembly. That is a restriction worth stating in code rather than only
// obeying, because "parse JSON" instinctively suggests balancing braces.
//
// Balancing braces here would be actively harmful. A truncated line -- the
// normal result of reading a file while PostgreSQL is writing it -- has an
// unbalanced brace, so a brace-balancing framer would swallow the next record,
// and the one after that, until it found a closing brace somewhere in an
// unrelated message. One torn write would corrupt an unbounded run of records
// instead of costing a single malformed line.

// splitJSONRecord frames one jsonlog record: exactly one physical line.
func splitJSONRecord(data []byte, atEOF bool, emitTail bool) (int, []byte, error) {
	return splitLine(data, atEOF, emitTail)
}

// jsonValue is one key's value as the scanner found it: the raw bytes, and
// whether they still carry escapes.
type jsonValue struct {
	raw     []byte
	str     bool // the value was a JSON string, so raw excludes the quotes
	escaped bool // the string contains at least one backslash escape
}

// scanJSONObject walks a jsonlog object, calling visit for each key.
//
// No map, no reflection, no encoding/json (CON-004, PERF-005). The scanner
// walks the object once and hands each key straight to the field dispatcher,
// which is what makes a record cost zero allocations -- Unmarshal into a struct
// allocates for every string field, and Decoder.Token allocates per token.
//
// It is a jsonlog scanner, not a general JSON parser: PostgreSQL writes a flat
// object of strings and numbers, so nested objects and arrays are not handled.
// Meeting one means the line is not jsonlog, and reporting it as malformed is
// the right answer rather than a limitation.
func scanJSONObject(rec []byte, visit func(key []byte, val jsonValue)) error {
	i := skipJSONSpace(rec, 0)
	if i >= len(rec) || rec[i] != '{' {
		return errBadJSON
	}
	i = skipJSONSpace(rec, i+1)
	if i < len(rec) && rec[i] == '}' {
		return nil // an empty object is a valid, if unhelpful, record
	}

	for i < len(rec) {
		if rec[i] != '"' {
			return errBadJSON
		}
		key, next, ok := scanJSONString(rec, i)
		if !ok {
			return errBadJSON
		}
		i = skipJSONSpace(rec, next)
		if i >= len(rec) || rec[i] != ':' {
			return errBadJSON
		}
		i = skipJSONSpace(rec, i+1)

		val, next, ok := scanJSONValue(rec, i)
		if !ok {
			return errBadJSON
		}
		visit(key.raw, val)
		i = skipJSONSpace(rec, next)

		if i >= len(rec) {
			return errBadJSON // ran off the end without a closing brace
		}
		switch rec[i] {
		case ',':
			i = skipJSONSpace(rec, i+1)
		case '}':
			return nil
		default:
			return errBadJSON
		}
	}
	return errBadJSON
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	return i
}

// scanJSONString reads a quoted string starting at b[i], returning its content
// without the quotes and the index just past the closing quote.
//
// The escape handling is the part that has to be exactly right. A backslash
// escapes the next character whatever it is, so the scan must step over pairs
// rather than test bytes individually: a string ending in an escaped backslash
// closes normally, while a string containing an escaped quote does not close
// there. Getting either wrong shifts every following key by one and produces a
// record that looks plausible and is wrong.
func scanJSONString(b []byte, i int) (jsonValue, int, bool) {
	i++ // the opening quote
	start := i
	escaped := false
	for i < len(b) {
		switch b[i] {
		case '\\':
			escaped = true
			i += 2 // the backslash and whatever it escapes
		case '"':
			return jsonValue{raw: b[start:i], str: true, escaped: escaped}, i + 1, true
		default:
			i++
		}
	}
	return jsonValue{}, 0, false
}

// scanJSONValue reads any value jsonlog can contain: a string, a number, or
// null. Booleans are accepted for completeness.
func scanJSONValue(b []byte, i int) (jsonValue, int, bool) {
	if i >= len(b) {
		return jsonValue{}, 0, false
	}
	switch b[i] {
	case '"':
		return scanJSONString(b, i)
	case 'n':
		if hasPrefix(b[i:], "null") {
			// Absent, not the four-letter text: returning an empty
			// value is what makes Record's zero-value convention
			// hold for a key PostgreSQL wrote as null.
			return jsonValue{}, i + 4, true
		}
	case 't':
		if hasPrefix(b[i:], "true") {
			return jsonValue{raw: b[i : i+4]}, i + 4, true
		}
	case 'f':
		if hasPrefix(b[i:], "false") {
			return jsonValue{raw: b[i : i+5]}, i + 5, true
		}
	}
	// A number. jsonlog never writes a nested object or array, so anything
	// else is not jsonlog and the caller should report it as malformed.
	start := i
	for i < len(b) {
		c := b[i]
		if isDigit(c) || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			i++
			continue
		}
		break
	}
	if i == start {
		return jsonValue{}, 0, false
	}
	return jsonValue{raw: b[start:i]}, i, true
}

// jsonKey identifies one of the keys PostgreSQL's jsonlog writer emits.
type jsonKey uint8

const (
	jkUnknown jsonKey = iota
	jkTimestamp
	jkUser
	jkDBName
	jkPID
	jkRemoteHost
	jkRemotePort
	jkSessionID
	jkLineNum
	jkPS
	jkSessionStart
	jkVXID
	jkTXID
	jkErrorSeverity
	jkStateCode
	jkMessage
	jkDetail
	jkHint
	jkInternalQuery
	jkInternalPosition
	jkContext
	jkStatement
	jkCursorPosition
	jkFuncName
	jkFileName
	jkFileLineNum
	jkApplicationName
	jkBackendType
	jkLeaderPID
	jkQueryID
)

// jsonKeyOf maps a key name to its identifier.
//
// PERF-005 forbids a map per record, and a map here would be one: 29 keys
// hashed per record, on top of the scan that already found them. A switch on
// string(key) compiles to a length dispatch followed by direct comparisons,
// with no copy of key and no hashing, and the compiler generates a better
// decision tree than a hand-written perfect hash would be worth maintaining.
//
// The key names are exactly those FMT-002 lists; an unrecognised key is
// ignored rather than reported, so a future PostgreSQL that adds one parses as
// a record missing that field rather than as a malformed line.
func jsonKeyOf(key []byte) jsonKey {
	switch string(key) {
	case "timestamp":
		return jkTimestamp
	case "user":
		return jkUser
	case "dbname":
		return jkDBName
	case "pid":
		return jkPID
	case "remote_host":
		return jkRemoteHost
	case "remote_port":
		return jkRemotePort
	case "session_id":
		return jkSessionID
	case "line_num":
		return jkLineNum
	case "ps":
		return jkPS
	case "session_start":
		return jkSessionStart
	case "vxid":
		return jkVXID
	case "txid":
		return jkTXID
	case "error_severity":
		return jkErrorSeverity
	case "state_code":
		return jkStateCode
	case "message":
		return jkMessage
	case "detail":
		return jkDetail
	case "hint":
		return jkHint
	case "internal_query":
		return jkInternalQuery
	case "internal_position":
		return jkInternalPosition
	case "context":
		return jkContext
	case "statement":
		return jkStatement
	case "cursor_position":
		return jkCursorPosition
	case "func_name":
		return jkFuncName
	case "file_name":
		return jkFileName
	case "file_line_num":
		return jkFileLineNum
	case "application_name":
		return jkApplicationName
	case "backend_type":
		return jkBackendType
	case "leader_pid":
		return jkLeaderPID
	case "query_id":
		return jkQueryID
	}
	return jkUnknown
}

// parseJSONInto fills a Record from one jsonlog object.
//
// Two Record fields have no single key behind them and must be assembled:
// ConnectionFrom joins remote_host and remote_port, and Location joins
// func_name, file_name and file_line_num. Both are built in the parser's
// reusable scratch buffer AFTER the scan, because JSON keys may arrive in any
// order and neither pair is guaranteed adjacent.
//
// The scratch grows a bounded number of times and is then reused, so these two
// fields stay inside PERF-001 while still satisfying COR-001 -- the alternative,
// dropping them, would lose information the log contains.
func (p *Parser) parseJSONInto(rec []byte) error {
	r := &p.rec
	p.scratch = p.scratch[:0]

	// Pieces held back until the scan finishes.
	var host, port, fn, file, fileLine []byte

	err := scanJSONObject(rec, func(key []byte, val jsonValue) {
		if val.escaped {
			r.Flags |= FlagNeedsUnquote
		}
		switch jsonKeyOf(key) {
		case jkTimestamp:
			if ts, _, ok := p.tz.timestamp(val.raw); ok {
				r.Time = ts
			}
		case jkSessionStart:
			if ts, _, ok := p.tz.timestamp(val.raw); ok {
				r.SessionStart = ts
			}
		case jkUser:
			r.User = val.raw
		case jkDBName:
			r.Database = val.raw
		case jkPID:
			r.ProcessID, _ = parseInt32(val.raw)
		case jkRemoteHost:
			host = val.raw
		case jkRemotePort:
			port = val.raw
		case jkSessionID:
			r.SessionID = val.raw
		case jkLineNum:
			r.SessionLineNum, _ = parseInt(val.raw)
		case jkPS:
			// "ps" is the process title, which is where PostgreSQL
			// puts the command tag; csvlog's column is command_tag.
			r.CommandTag = val.raw
		case jkVXID:
			r.VirtualXID = val.raw
		case jkTXID:
			r.TransactionID, _ = parseInt(val.raw)
		case jkErrorSeverity:
			r.RawSeverity = val.raw
			r.Severity = p.sev.resolve(val.raw)
		case jkStateCode:
			if len(val.raw) == 5 {
				copy(r.SQLState[:], val.raw)
			}
		case jkMessage:
			r.Message = val.raw
		case jkDetail:
			r.Detail = val.raw
		case jkHint:
			r.Hint = val.raw
		case jkInternalQuery:
			r.InternalQuery = val.raw
		case jkInternalPosition:
			r.InternalQueryPos, _ = parseInt32(val.raw)
		case jkContext:
			r.Context = val.raw
		case jkStatement:
			// csvlog carries the statement in its query column and
			// this package mirrors it into Query and Statement;
			// jsonlog must do the same or the two disagree (COR-004).
			r.Statement = val.raw
			r.Query = val.raw
			r.Flags |= FlagHasStatement
		case jkCursorPosition:
			r.QueryPos, _ = parseInt32(val.raw)
		case jkFuncName:
			fn = val.raw
		case jkFileName:
			file = val.raw
		case jkFileLineNum:
			fileLine = val.raw
		case jkApplicationName:
			r.ApplicationName = val.raw
		case jkBackendType:
			r.BackendType = val.raw
		case jkLeaderPID:
			r.LeaderPID, _ = parseInt32(val.raw)
		case jkQueryID:
			r.QueryID, _ = parseInt(val.raw)
		}
	})
	if err != nil {
		return err
	}

	// Assemble the joined fields. Offsets are recorded and sliced only at
	// the end, because appending may reallocate the scratch and invalidate
	// a slice taken from it earlier.
	fromStart := len(p.scratch)
	if len(host) > 0 {
		p.scratch = append(p.scratch, host...)
		if len(port) > 0 {
			p.scratch = append(p.scratch, ':')
			p.scratch = append(p.scratch, port...)
		}
	}
	fromEnd := len(p.scratch)

	locStart := fromEnd
	if len(fn) > 0 || len(file) > 0 {
		// csvlog writes this column as "func, file:line"; matching that
		// spelling is what lets COR-004 hold for Location.
		p.scratch = append(p.scratch, fn...)
		if len(fn) > 0 && len(file) > 0 {
			p.scratch = append(p.scratch, ", "...)
		}
		p.scratch = append(p.scratch, file...)
		if len(fileLine) > 0 {
			p.scratch = append(p.scratch, ':')
			p.scratch = append(p.scratch, fileLine...)
		}
	}
	locEnd := len(p.scratch)

	if fromEnd > fromStart {
		r.ConnectionFrom = p.scratch[fromStart:fromEnd:fromEnd]
	}
	if locEnd > locStart {
		r.Location = p.scratch[locStart:locEnd:locEnd]
	}

	p.scanRecordDuration()
	return nil
}
