package pglogwatch

import (
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanTimestampShapes(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		consumed int
		zone     string
		nsec     int
	}{
		{"%m with abbreviation", "2026-08-30 10:11:12.123 CEST", 28, "CEST", 123000000},
		{"%t without fraction", "2026-08-30 10:11:12 UTC", 23, "UTC", 0},
		{"no zone at all", "2026-08-30 10:11:12", 19, "", 0},
		{"numeric offset", "2026-08-30 10:11:12.5 +05:30", 28, "+05:30", 500000000},
		{"compact offset", "2026-08-30 10:11:12 -0800", 25, "-0800", 0},
		{"ISO T separator", "2026-08-30T10:11:12", 19, "", 0},
		{"nine fractional digits", "2026-08-30 10:11:12.123456789", 29, "", 123456789},
		{"trailing content stops the scan", "2026-08-30 10:11:12.123 CEST,rest", 28, "CEST", 123000000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, n, ok := scanTimestamp([]byte(c.in))
			require.True(t, ok)
			assert.Equal(t, c.consumed, n, "consumed")
			assert.Equal(t, c.zone, string(p.zone), "zone")
			assert.Equal(t, c.nsec, p.nsec, "nsec")
			assert.Equal(t, 2026, p.year)
			assert.Equal(t, 8, p.month)
			assert.Equal(t, 30, p.day)
			assert.Equal(t, 10, p.hour)
			assert.Equal(t, 11, p.min)
			assert.Equal(t, 12, p.sec)
		})
	}
}

func TestScanTimestampRejects(t *testing.T) {
	// The calendar fields are range-checked rather than handed to
	// time.Date, which would normalise month 13 into next January and turn
	// a corrupt field into a plausible timestamp.
	bad := []string{
		"",
		"2026-08-30",
		"2026-13-30 10:11:12",
		"2026-08-32 10:11:12",
		"2026-08-30 24:11:12",
		"2026-08-30 10:60:12",
		"2026-08-30 10:11:61",
		"2026-00-30 10:11:12",
		"2026-08-00 10:11:12",
		"2026/08/30 10:11:12",
		"2026-08-30 10-11-12",
		"2026-08-30 10:11:12.",
		"20x6-08-30 10:11:12",
		"a log line that is long enough",
	}
	for _, s := range bad {
		_, _, ok := scanTimestamp([]byte(s))
		assert.False(t, ok, "scanTimestamp(%q) should fail", s)
	}
}

func TestScanTimestampLeapSecond(t *testing.T) {
	p, _, ok := scanTimestamp([]byte("2016-12-31 23:59:60 UTC"))
	require.True(t, ok)
	assert.Equal(t, 60, p.sec)
}

func TestScanZone(t *testing.T) {
	assert.Equal(t, "CEST", string(scanZone([]byte("CEST rest"))))
	assert.Equal(t, "+05:30", string(scanZone([]byte("+05:30"))))
	assert.Equal(t, "-0800", string(scanZone([]byte("-0800 x"))))
	assert.Nil(t, scanZone([]byte("")))
	assert.Nil(t, scanZone([]byte("-")))
	assert.Nil(t, scanZone([]byte("123")))
}

// TestAllocTimestamp is the PERF-007 gate for the scanner half: no allocation,
// and in particular no time.Parse.
func TestAllocTimestamp(t *testing.T) {
	line := []byte("2026-08-30 10:11:12.123 CEST")
	allocs.Zero(t, 100, func() {
		_, _, _ = scanTimestamp(line)
	})
}
