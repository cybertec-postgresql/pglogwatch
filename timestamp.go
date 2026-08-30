package pglogwatch

// Timestamp scanning.
//
// PERF-007 forbids time.Parse on the hot path. time.Parse walks a layout
// string byte by byte alongside the value, resolves the zone through
// time.LoadLocation, and allocates. PostgreSQL's timestamp format is fixed, so
// a scanner that knows the layout in advance is both faster and simpler.
//
// Every destination writes the same shape:
//
//	2026-08-30 10:00:00.123 CEST     csvlog log_time, jsonlog timestamp, %m
//	2026-08-30 10:00:00 CEST         %t
//	2026-08-30 10:00:00.123 +05:30   log_timezone set to a numeric offset
//
// This file recovers the calendar fields and the zone token; tzcache.go turns
// the zone token into a *time.Location and assembles the time.Time, because
// that is the part worth caching.

// tsParts is a decomposed timestamp. zone is a borrowed slice: either a zone
// abbreviation ("CEST") or a numeric offset ("+05:30"), empty if absent.
type tsParts struct {
	year, month, day int
	hour, min, sec   int
	nsec             int
	zone             []byte
}

// minTimestampLen is len("2006-01-02 15:04:05").
const minTimestampLen = 19

// scanTimestamp reads a PostgreSQL log timestamp from the front of b and
// returns the decomposed fields plus how many bytes it consumed.
//
// It is deliberately greedy about the zone and strict about everything else:
// the calendar part has exactly one shape, so anything else is a mis-split
// field rather than an unusual timestamp.
func scanTimestamp(b []byte) (tsParts, int, bool) {
	var p tsParts
	if len(b) < minTimestampLen {
		return p, 0, false
	}
	// The separators are checked explicitly rather than skipped, because
	// this function doubles as the csvlog format detector's test for "does
	// this line start with a timestamp" (FMT-005).
	if b[4] != '-' || b[7] != '-' || b[13] != ':' || b[16] != ':' {
		return p, 0, false
	}
	if b[10] != ' ' && b[10] != 'T' {
		return p, 0, false
	}

	var ok bool
	if p.year, ok = parseDigits(b[0:], 4); !ok {
		return p, 0, false
	}
	if p.month, ok = parseDigits(b[5:], 2); !ok {
		return p, 0, false
	}
	if p.day, ok = parseDigits(b[8:], 2); !ok {
		return p, 0, false
	}
	if p.hour, ok = parseDigits(b[11:], 2); !ok {
		return p, 0, false
	}
	if p.min, ok = parseDigits(b[14:], 2); !ok {
		return p, 0, false
	}
	if p.sec, ok = parseDigits(b[17:], 2); !ok {
		return p, 0, false
	}
	// Range-check rather than trusting the digits. time.Date would happily
	// normalise month 13 into the next January, turning a corrupt field
	// into a plausible timestamp.
	if p.month < 1 || p.month > 12 || p.day < 1 || p.day > 31 ||
		p.hour > 23 || p.min > 59 || p.sec > 60 { // 60: leap second
		return p, 0, false
	}

	n := minTimestampLen

	// Fractional seconds: PostgreSQL writes three digits, but accept one
	// to nine so a log written by something else still parses.
	if n < len(b) && b[n] == '.' {
		n++
		start := n
		scale := 100000000
		for n < len(b) && b[n] >= '0' && b[n] <= '9' {
			if n-start < 9 {
				p.nsec += int(b[n]-'0') * scale
				scale /= 10
			}
			n++
		}
		if n == start {
			return p, 0, false // a '.' with no digits after it
		}
	}

	// Zone, if any, after exactly one space.
	if n+1 < len(b) && b[n] == ' ' {
		if z := scanZone(b[n+1:]); len(z) > 0 {
			p.zone = z
			n += 1 + len(z)
		}
	}
	return p, n, true
}

// scanZone returns the zone token at the front of b, or nil if there is none.
//
// Two shapes occur: an alphabetic abbreviation as PostgreSQL prints it for a
// named log_timezone ("UTC", "CEST", "MSK"), and a numeric offset for a
// log_timezone that has no abbreviation ("+05:30", "-0800", "+03").
func scanZone(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	if b[0] == '+' || b[0] == '-' {
		n := 1
		for n < len(b) && (isDigit(b[n]) || b[n] == ':') {
			n++
		}
		if n < 2 { // a lone sign is not an offset
			return nil
		}
		return b[:n]
	}
	n := 0
	for n < len(b) && isZoneLetter(b[n]) {
		n++
	}
	if n == 0 {
		return nil
	}
	return b[:n]
}

func isDigit(c byte) bool { return c-'0' <= 9 }

// isZoneLetter reports whether c can appear in a zone abbreviation.
// PostgreSQL abbreviations are uppercase letters; lowercase is accepted so a
// hand-edited or third-party log still parses.
func isZoneLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
