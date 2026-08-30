package pglogwatch

import (
	"testing"
	"time"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimestampWithZone(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // time.Time.String()
	}{
		{"UTC", "2026-08-30 10:11:12 UTC", "2026-08-30 10:11:12 +0000 UTC"},
		{"CEST", "2026-08-30 10:11:12.123 CEST", "2026-08-30 10:11:12.123 +0200 CEST"},
		{"half-hour offset (E19)", "2026-08-30 10:11:12 +05:30", "2026-08-30 10:11:12 +0530 +05:30"},
		{"compact offset", "2026-08-30 10:11:12 -0800", "2026-08-30 10:11:12 -0800 -0800"},
		{"hour-only offset", "2026-08-30 10:11:12 +03", "2026-08-30 10:11:12 +0300 +03"},
		{"India, per PostgreSQL's Default set", "2026-08-30 10:11:12 IST", "2026-08-30 10:11:12 +0530 IST"},
		{"no zone means UTC", "2026-08-30 10:11:12", "2026-08-30 10:11:12 +0000 UTC"},
		// E20: an abbreviation the table does not know falls back to UTC
		// and is NOT an error.
		{"unknown abbreviation (E20)", "2026-08-30 10:11:12 QQQ", "2026-08-30 10:11:12 +0000 UTC"},
		{"zero offset normalises to UTC", "2026-08-30 10:11:12 +00:00", "2026-08-30 10:11:12 +0000 UTC"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cache tzCache
			ts, n, ok := cache.timestamp([]byte(c.in))
			require.True(t, ok)
			assert.Equal(t, len(c.in), n)
			assert.Equal(t, c.want, ts.String())
		})
	}
}

func TestTZCacheReuse(t *testing.T) {
	var cache tzCache
	first := cache.location([]byte("CEST"))
	second := cache.location([]byte("CEST"))
	assert.Same(t, first, second, "a repeated zone must reuse the cached location")
	assert.Equal(t, 1, cache.n)

	cache.location([]byte("CET"))
	assert.Equal(t, 2, cache.n, "a log spanning a DST change holds two zones")
}

func TestTZCacheOverflowStillResolves(t *testing.T) {
	var cache tzCache
	zones := []string{"UTC", "CET", "CEST", "EET", "EEST", "MSK", "JST", "KST", "HST", "PST"}
	for _, z := range zones {
		require.NotNil(t, cache.location([]byte(z)), "zone %s", z)
	}
	assert.Equal(t, tzCacheEntries, cache.n, "cache fills and stops growing")
	// Past capacity, resolution still works, it is simply not cached.
	loc := cache.location([]byte("PST"))
	require.NotNil(t, loc)
	_, offset := time.Now().In(loc).Zone()
	assert.Equal(t, -8*3600, offset)
}

func TestParseNumericOffset(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"+00", 0, true},
		{"+05:30", 5*3600 + 30*60, true},
		{"-0800", -8 * 3600, true},
		{"+14", 14 * 3600, true},
		{"+24", 0, false},
		{"+05:60", 0, false},
		{"+0530x", 0, false},
		{"+5", 0, false},
		{"+05-30", 0, false},
	}
	for _, c := range cases {
		got, ok := parseNumericOffset([]byte(c.in))
		assert.Equal(t, c.ok, ok, "parseNumericOffset(%q) ok", c.in)
		if c.ok {
			assert.Equal(t, c.want, got, "parseNumericOffset(%q)", c.in)
		}
	}
}

func TestZoneAbbrevOffsetsAreDistinct(t *testing.T) {
	// The North American abbreviations are the ones easiest to group
	// wrongly, since each standard time shares an offset with a
	// neighbouring daylight time. Pin them individually.
	want := map[string]int{
		"EST": -5, "EDT": -4,
		"CST": -6, "CDT": -5,
		"MST": -7, "MDT": -6,
		"PST": -8, "PDT": -7,
		"AKST": -9, "AKDT": -8,
	}
	for abbrev, hours := range want {
		got, ok := zoneAbbrevOffset([]byte(abbrev))
		require.True(t, ok, "%s missing from the table", abbrev)
		assert.Equal(t, hours*3600, got, "%s", abbrev)
	}
	_, ok := zoneAbbrevOffset([]byte("QQQ"))
	assert.False(t, ok)
}

// TestAllocTZCache is the PERF-007 gate: after the first resolution, no
// timestamp costs an allocation.
func TestAllocTZCache(t *testing.T) {
	var cache tzCache
	line := []byte("2026-08-30 10:11:12.123 CEST")
	allocs.Zero(t, 100, func() {
		_, _, _ = cache.timestamp(line)
	})
}
