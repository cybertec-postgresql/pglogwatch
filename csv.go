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
