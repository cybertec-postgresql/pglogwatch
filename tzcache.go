package pglogwatch

import "time"

// Timezone resolution, cached per parser.
//
// PERF-007 requires each distinct zone string to be resolved once and reused,
// and forbids time.LoadLocation on the hot path. LoadLocation would not help
// anyway: PostgreSQL prints a zone *abbreviation* ("CEST"), and the tzdata
// database is keyed by location name ("Europe/Vienna"). Abbreviations are
// therefore resolved from the table below, and numeric offsets are parsed
// directly.
//
// A log realistically contains one or two distinct zones -- two only when it
// spans a daylight-saving transition -- so a small fixed-size cache with a
// linear scan beats a map on both speed and allocation.

const (
	tzCacheEntries = 8
	tzKeyMax       = 12 // longer than any abbreviation or "+HH:MM" offset
)

// tzCache maps zone tokens to locations. Its zero value is ready to use, and
// it holds no state that survives a Reset beyond the resolved locations, which
// stay valid for any stream.
type tzCache struct {
	keys [tzCacheEntries][tzKeyMax]byte
	lens [tzCacheEntries]uint8
	locs [tzCacheEntries]*time.Location
	n    int
}

// location resolves a zone token to a *time.Location, caching the result.
//
// An unrecognised abbreviation yields time.UTC and is NOT an error: E20
// requires the record to be emitted and Stats.Malformed left alone, because a
// zone this table has not heard of is a gap in the table, not a corrupt log.
func (c *tzCache) location(zone []byte) *time.Location {
	if len(zone) == 0 {
		return time.UTC
	}
	for i := range c.n {
		if int(c.lens[i]) == len(zone) && string(c.keys[i][:c.lens[i]]) == string(zone) {
			return c.locs[i]
		}
	}
	loc := resolveZone(zone)
	if len(zone) <= tzKeyMax && c.n < tzCacheEntries {
		copy(c.keys[c.n][:], zone)
		c.lens[c.n] = uint8(len(zone))
		c.locs[c.n] = loc
		c.n++
	}
	return loc
}

// resolveZone turns a zone token into a location without consulting the cache.
func resolveZone(zone []byte) *time.Location {
	if zone[0] == '+' || zone[0] == '-' {
		if secs, ok := parseNumericOffset(zone); ok {
			if secs == 0 {
				return time.UTC
			}
			return time.FixedZone(string(zone), secs)
		}
		return time.UTC
	}
	if secs, ok := zoneAbbrevOffset(zone); ok {
		if secs == 0 {
			return time.UTC
		}
		return time.FixedZone(string(zone), secs)
	}
	return time.UTC
}

// parseNumericOffset parses "+HH", "+HHMM" or "+HH:MM" into seconds east of
// UTC.
func parseNumericOffset(z []byte) (int, bool) {
	neg := z[0] == '-'
	d := z[1:]
	var hh, mm int
	var ok bool
	switch len(d) {
	case 2: // +HH
		hh, ok = parseDigits(d, 2)
	case 4: // +HHMM
		if hh, ok = parseDigits(d, 2); ok {
			mm, ok = parseDigits(d[2:], 2)
		}
	case 5: // +HH:MM
		if d[2] != ':' {
			return 0, false
		}
		if hh, ok = parseDigits(d, 2); ok {
			mm, ok = parseDigits(d[3:], 2)
		}
	default:
		return 0, false
	}
	if !ok || hh > 23 || mm > 59 {
		return 0, false
	}
	secs := hh*3600 + mm*60
	if neg {
		secs = -secs
	}
	return secs, true
}

// zoneAbbrevOffset maps a PostgreSQL zone abbreviation to seconds east of UTC.
//
// Several abbreviations are ambiguous across the world -- CST is US Central,
// China and Cuba; IST is India, Ireland and Israel. Where they conflict this
// table follows PostgreSQL's own Default abbreviation set
// (src/timezone/tznames/Default), so that pglogwatch and the server that wrote
// the log agree on what the log says.
//
// An abbreviation missing from this table resolves to UTC (E20). Adding one is
// a one-line change and cannot break an existing caller.
func zoneAbbrevOffset(z []byte) (int, bool) {
	const (
		h = 3600
		m = 60
	)
	switch string(z) {
	case "UTC", "UCT", "GMT", "UT", "Z", "ZULU", "WET":
		return 0, true

	// Europe, Africa and western Asia.
	case "CET", "MET", "WEST", "BST", "WAT":
		return 1 * h, true
	case "CEST", "MEST", "EET", "CAT", "SAST":
		return 2 * h, true
	case "EEST", "MSK", "EAT":
		return 3 * h, true
	case "MSD":
		return 4 * h, true

	// The Americas.
	case "HST":
		return -10 * h, true
	case "AKST":
		return -9 * h, true
	case "AKDT", "PST":
		return -8 * h, true
	case "PDT", "MST":
		return -7 * h, true
	case "MDT", "CST":
		return -6 * h, true
	case "CDT", "EST":
		return -5 * h, true
	case "EDT", "CLT":
		return -4 * h, true
	case "ART", "BRT", "UYT", "CLST":
		return -3 * h, true

	// South, east and south-east Asia, and Oceania.
	case "PKT":
		return 5 * h, true
	case "IST":
		// India, per PostgreSQL's Default set. Ireland and Israel also
		// use IST; a server in either writes a log this table reads as
		// India. Resolving that needs the server's log_timezone, which
		// the library deliberately never reads (CON-005).
		return 5*h + 30*m, true
	case "ICT", "WIB":
		return 7 * h, true
	case "HKT", "SGT", "AWST":
		return 8 * h, true
	case "JST", "KST":
		return 9 * h, true
	case "ACST":
		return 9*h + 30*m, true
	case "AEST":
		return 10 * h, true
	case "ACDT":
		return 10*h + 30*m, true
	case "AEDT":
		return 11 * h, true
	case "NZST":
		return 12 * h, true
	case "NZDT":
		return 13 * h, true
	}
	return 0, false
}

// timestamp scans a timestamp from the front of b and assembles a time.Time,
// resolving the zone through the cache. It returns the value, how many bytes
// it consumed, and whether the scan succeeded.
func (c *tzCache) timestamp(b []byte) (time.Time, int, bool) {
	p, n, ok := scanTimestamp(b)
	if !ok {
		return time.Time{}, 0, false
	}
	loc := c.location(p.zone)
	return time.Date(p.year, time.Month(p.month), p.day, p.hour, p.min, p.sec, p.nsec, loc), n, true
}
