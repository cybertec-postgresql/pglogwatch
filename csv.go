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

// splitCSVRecord frames one csvlog record. It follows the splitFunc contract.
func splitCSVRecord(data []byte, atEOF bool, emitTail bool) (int, []byte, error) {
	i := 0
	inQuotes := false
	for i < len(data) {
		if !inQuotes {
			rest := data[i:]
			nl := bytes.IndexByte(rest, '\n')
			qt := bytes.IndexByte(rest, '"')
			if nl >= 0 && (qt < 0 || nl < qt) {
				end := i + nl
				line := trimCR(data[:end])
				if len(line) == 0 {
					// A blank line between records is not a
					// record; consume it and ask again.
					return end + 1, nil, nil
				}
				return end + 1, line, nil
			}
			if qt < 0 {
				break // no quote and no newline: need more data
			}
			i += qt + 1
			inQuotes = true
			continue
		}

		qt := bytes.IndexByte(data[i:], '"')
		if qt < 0 {
			break // unterminated field so far: need more data
		}
		q := i + qt
		if q+1 >= len(data) {
			// The next byte decides whether this quote closes the
			// field or is the first half of a doubled quote, and
			// that byte has not been read yet.
			if !atEOF {
				return 0, nil, nil
			}
			i = q + 1
			inQuotes = false
			continue
		}
		if data[q+1] == '"' {
			i = q + 2 // an escaped quote; still inside the field
			continue
		}
		i = q + 1
		inQuotes = false
	}

	if !atEOF {
		return 0, nil, nil
	}
	// End of input inside or after an unterminated record.
	line := trimCR(data)
	if len(line) == 0 {
		return len(data), nil, nil
	}
	if !emitTail {
		return len(data), nil, nil
	}
	return len(data), line, nil
}

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

	r.RawSeverity = f[csvErrorSeverity]
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

	if csvHasBackendType(n) {
		r.BackendType = f[csvBackendType]
	}
	if csvHasParallelColumns(n) {
		r.LeaderPID, _ = parseInt32(f[csvLeaderPID])
		r.QueryID, _ = parseInt(f[csvQueryID])
	}
	return nil
}
