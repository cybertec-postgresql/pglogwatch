package main

// Message normalisation for grouping.
//
// "relation \"orders_2026_08\" does not exist" and
// "relation \"orders_2026_09\" does not exist" are the same error. Grouping
// verbatim produces one group per occurrence for any message that names a
// value -- which is most of them -- and a "top 10 errors" report where every
// row has a count of 1 answers nothing.
//
// This is deliberately NOT query fingerprinting, which the specification puts
// out of scope beyond an optional helper. It replaces the three things
// PostgreSQL varies within an otherwise fixed message: quoted identifiers,
// numbers, and quoted literals.

// normalizeMessage appends a grouping key for msg to dst.
func normalizeMessage(dst, msg []byte) []byte {
	inQuote := byte(0)
	prevDigit := false

	for i := 0; i < len(msg); i++ {
		c := msg[i]

		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
				dst = append(dst, '?')
			}
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
			continue
		}
		if c >= '0' && c <= '9' {
			if !prevDigit {
				dst = append(dst, '?')
				prevDigit = true
			}
			continue
		}
		prevDigit = false
		dst = append(dst, c)
	}
	if inQuote != 0 {
		// An unterminated quote: the rest of the message was one value.
		dst = append(dst, '?')
	}
	return dst
}
