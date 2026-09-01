package pglogwatch

import "bytes"

// csvlog record framing.
//
// A csvlog record ends at a newline that is NOT inside a quoted field, and
// PostgreSQL will happily write a newline inside one: any message containing a
// multi-line statement produces such a record. Splitting on every newline --
// which is what pgwatch's current implementation does -- turns one error with
// a three-line statement into three records, two of which are then counted as
// unparseable.
//
// The scan alternates between two states and uses bytes.IndexByte for both, so
// it stays SIMD-fast rather than walking byte by byte:
//
//	outside quotes: find the next '"' or '\n', whichever comes first
//	inside quotes:  find the next '"', and check whether it is doubled

// Performance notes for this path (GUD-001, GUD-002, GUD-003).
//
// Measured on the fixtures at 1 MB, AMD Ryzen 9 7940HS / windows/amd64 / Go
// 1.26.5: full parse 651 MB/s, severity-only 643 MB/s, framing alone 630 MB/s,
// all 0 allocs/op. PERF-020's floor is 250 MB/s. That the three figures are
// within 3 % of each other is the finding worth keeping: framing dominates, and
// field extraction is nearly free once the record boundary is known. It is also
// why PERF-021's separate severity-only floor of 800 MB/s is not met -- there
// is no cheaper mode to be in. bench/THRESHOLDS.md records that in full.
//
// Bounds-check elimination (GUD-001). Ten checks survive in this file, and all
// ten are per-record or per-field rather than per-byte, because every per-byte
// scan here is a bytes.IndexByte or bytes.Count call -- assembly with its own
// bounds established once. Rewriting the field walk to carry tail slices, which
// is what removes a per-iteration check elsewhere, would trade a check
// performed a dozen times per record for pointer arithmetic performed a dozen
// times per record. Verified with -gcflags=-d=ssa/check_bce/debug=1.
//
// Escape analysis (GUD-003). Nothing on this path reaches the heap: `data` and
// `rec` leak only into the returned sub-slices, which is the borrowing contract
// rather than an allocation, and splitCSVFields's `out` parameter is reported
// as "does not escape". The note on parseCSVInto explains what would break
// that.

// splitCSVRecord frames one csvlog record. It follows the splitFunc contract.
//
// The scan is two SIMD passes per candidate record rather than a walk through
// the quoting state machine:
//
//  1. bytes.IndexByte finds the next newline over the whole remaining buffer;
//  2. bytes.Count counts the quotes before it.
//
// A newline is inside a quoted field exactly when an ODD number of quotes
// precedes it. That works because every quote toggles the state and an escaped
// quote is two of them, which toggles twice and so preserves parity -- the
// doubled-quote case needs no special handling at all here.
//
// The obvious alternative, alternating IndexByte calls between quote and
// newline as the state changes, issues about twenty short calls per record on
// a typical csvlog line with ten quoted columns. This issues two long ones,
// which is roughly twice as fast on the fixtures and scales with the SIMD
// width rather than with the column count.
func splitCSVRecord(data []byte, atEOF bool, emitTail bool) (int, []byte, error) {
	search := 0
	quotes := 0
	for {
		nl := indexNewline(data[search:])
		if nl < 0 {
			break
		}
		end := search + nl
		quotes += bytes.Count(data[search:end], quoteBytes)
		if quotes%2 == 0 {
			line := trimCR(data[:end])
			if len(line) == 0 {
				// A blank line between records is not a record;
				// consume it and ask again.
				return end + 1, nil, nil
			}
			return end + 1, line, nil
		}
		// The newline was inside a quoted field, so the record
		// continues past it.
		search = end + 1
	}

	if !atEOF {
		return 0, nil, nil
	}
	// End of input, with or without a complete record.
	line := trimCR(data)
	if len(line) == 0 || !emitTail {
		return len(data), nil, nil
	}
	return len(data), line, nil
}

// quoteBytes is the separator bytes.Count needs. Declared once at package
// level so the count in the hot loop does not build a one-byte slice per call.
var quoteBytes = []byte{'"'}

// maxCSVColumns bounds the per-record column array. PostgreSQL 18 writes 26;
// the extra room means a future version that appends a column parses as a
// record with unknown trailing fields rather than as a malformed line.
const maxCSVColumns = 32

// splitCSVFields fills out with borrowed slices, one per column, and returns
// how many columns the record had.
//
// Quoted fields are returned WITHOUT their surrounding quotes but WITH any
// doubled quotes left as written: PERF-009 defers unescaping to the caller, so
// the parser signals it with FlagNeedsUnquote and never rewrites the bytes.
// That is what keeps a record costing zero allocations for a caller who only
// wants the severity.
func splitCSVFields(rec []byte, out *[maxCSVColumns][]byte) (int, Flags, error) {
	var flags Flags
	n := 0
	i := 0
	for {
		var val []byte
		if i < len(rec) && rec[i] == '"' {
			i++
			start := i
			for {
				q := bytes.IndexByte(rec[i:], '"')
				if q < 0 {
					// The framer only hands over records
					// whose quotes balance, so an
					// unterminated field here means the
					// record was truncated by the size cap
					// or by end of input.
					return 0, 0, errUnterminated
				}
				j := i + q
				if j+1 < len(rec) && rec[j+1] == '"' {
					flags |= FlagNeedsUnquote
					i = j + 2
					continue
				}
				val = rec[start:j]
				i = j + 1
				break
			}
			// Skip anything between the closing quote and the
			// field separator. PostgreSQL never writes such bytes;
			// tolerating them keeps one corrupt field from
			// cascading into a wrong column count for the rest of
			// the record.
			if c := bytes.IndexByte(rec[i:], ','); c >= 0 {
				i += c
			} else {
				i = len(rec)
			}
		} else if c := bytes.IndexByte(rec[i:], ','); c >= 0 {
			val = rec[i : i+c]
			i += c
		} else {
			val = rec[i:]
			i = len(rec)
		}

		if n < len(out) {
			out[n] = val
		}
		n++

		if i >= len(rec) {
			return n, flags, nil
		}
		i++ // the separator; a trailing one yields a final empty column
	}
}

// parseCSVInto fills a Record from a framed csvlog record.
//
// The mapping is positional and covers every column PostgreSQL writes, not the
// prefix a severity counter needs: COR-001 requires the record to be lossless,
// and a caller who wants the query text should not have to reparse the line.
//
// Escape analysis (GUD-003): rec is a slice of the parser's read buffer and
// every field assigned below is a sub-slice of it, so nothing here allocates
// and nothing outlives the buffer. The one value that would escape is
// &p.csvFields -- but splitCSVFields only writes through the pointer and never
// stores it, which -gcflags=-m confirms as "out does not escape". Changing
// splitCSVFields to retain that pointer, or to return a slice instead of
// filling one, would move the column array to the heap and cost one
// allocation per record.
func (p *Parser) parseCSVInto(rec []byte) error {
	n, flags, err := splitCSVFields(rec, &p.csvFields)
	if err != nil {
		return err
	}
	if !csvLayoutIsKnown(n) {
		return errShortRecord
	}
	f := &p.csvFields
	r := &p.rec
	r.Flags |= flags
	if bytes.IndexByte(rec, '\n') >= 0 {
		// A newline survived framing, so it was inside a quoted field
		// and this record spanned several physical lines (E2).
		r.Flags |= FlagMultiline
	}

	// Severity is resolved through the configured locale table, so a
	// German or Russian server yields SeverityError just as an English one
	// does, while RawSeverity keeps the original bytes (FMT-007, AC-006).
	r.RawSeverity = f[csvErrorSeverity]
	r.Severity = p.sev.resolve(f[csvErrorSeverity])

	// Timestamps go through the parser's zone cache rather than
	// time.Parse, and integers through parseInt over bytes rather than
	// strconv (PERF-006, PERF-007).
	if ts, _, ok := p.tz.timestamp(f[csvLogTime]); ok {
		r.Time = ts
	}
	if ts, _, ok := p.tz.timestamp(f[csvSessionStartTime]); ok {
		r.SessionStart = ts
	}
	r.ProcessID, _ = parseInt32(f[csvProcessID])
	r.SessionLineNum, _ = parseInt(f[csvSessionLineNum])
	r.TransactionID, _ = parseInt(f[csvTransactionID])
	r.QueryPos, _ = parseInt32(f[csvQueryPos])
	r.InternalQueryPos, _ = parseInt32(f[csvInternalQueryPos])

	r.User = f[csvUserName]
	r.Database = f[csvDatabaseName]
	r.ConnectionFrom = f[csvConnectionFrom]
	r.SessionID = f[csvSessionID]
	r.CommandTag = f[csvCommandTag]
	r.VirtualXID = f[csvVirtualTransactionID]
	r.Message = f[csvMessage]
	r.Detail = f[csvDetail]
	r.Hint = f[csvHint]
	r.InternalQuery = f[csvInternalQuery]
	r.Context = f[csvContext]
	r.Query = f[csvQuery]
	r.Location = f[csvLocation]
	r.ApplicationName = f[csvApplicationName]

	// csvlog has no separate statement column: the statement a record is
	// about is its query column, which is what stderr writes as STATEMENT:.
	if len(r.Query) > 0 {
		r.Statement = r.Query
		r.Flags |= FlagHasStatement
	}

	// The five-character SQLSTATE is copied rather than borrowed, because
	// Record models it as an array. Anything not five bytes is treated as
	// absent instead of as a short code, since a truncated SQLSTATE would
	// silently compare equal to a real one on its first characters.
	if sql := f[csvSQLStateCode]; len(sql) == 5 {
		copy(r.SQLState[:], sql)
	}

	p.scanRecordDuration()

	if csvHasBackendType(n) {
		r.BackendType = f[csvBackendType]
	}
	if csvHasParallelColumns(n) {
		r.LeaderPID, _ = parseInt32(f[csvLeaderPID])
		r.QueryID, _ = parseInt(f[csvQueryID])
	}
	return nil
}
