package pglogwatch

import (
	"math"
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUint(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"0", 0, true},
		{"7", 7, true},
		{"12345", 12345, true},
		{"007", 7, true}, // PostgreSQL does not pad, but leading zeros are digits
		{"18446744073709551615", math.MaxUint64, true},

		{"", 0, false},
		{"18446744073709551616", 0, false}, // one past MaxUint64
		{"99999999999999999999999", 0, false},
		{"-1", 0, false},
		{"+1", 0, false}, // the sign belongs to parseInt
		{" 1", 0, false},
		{"1 ", 0, false},
		{"1a", 0, false},
		{"0x10", 0, false},
		{"1_000", 0, false},
	}
	for _, c := range cases {
		got, ok := parseUint([]byte(c.in))
		assert.Equal(t, c.ok, ok, "parseUint(%q) ok", c.in)
		if c.ok {
			assert.Equal(t, c.want, got, "parseUint(%q)", c.in)
		}
	}
}

func TestParseInt(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"-0", 0, true},
		{"42", 42, true},
		{"-42", -42, true},
		{"+42", 42, true},
		{"9223372036854775807", math.MaxInt64, true},
		{"-9223372036854775808", math.MinInt64, true},

		{"", 0, false},
		{"-", 0, false},
		{"+", 0, false},
		{"9223372036854775808", 0, false},
		{"-9223372036854775809", 0, false},
		{"--1", 0, false},
	}
	for _, c := range cases {
		got, ok := parseInt([]byte(c.in))
		assert.Equal(t, c.ok, ok, "parseInt(%q) ok", c.in)
		if c.ok {
			assert.Equal(t, c.want, got, "parseInt(%q)", c.in)
		}
	}
}

func TestParseInt32(t *testing.T) {
	cases := []struct {
		in   string
		want int32
		ok   bool
	}{
		{"2147483647", math.MaxInt32, true},
		{"-2147483648", math.MinInt32, true},
		{"2147483648", 0, false},
		{"-2147483649", 0, false},
		{"31337", 31337, true},
	}
	for _, c := range cases {
		got, ok := parseInt32([]byte(c.in))
		assert.Equal(t, c.ok, ok, "parseInt32(%q) ok", c.in)
		if c.ok {
			assert.Equal(t, c.want, got, "parseInt32(%q)", c.in)
		}
	}
}

func TestParseDigits(t *testing.T) {
	v, ok := parseDigits([]byte("2026-08"), 4)
	require.True(t, ok)
	assert.Equal(t, 2026, v)

	// Reads exactly n digits and ignores the rest.
	v, ok = parseDigits([]byte("0830"), 2)
	require.True(t, ok)
	assert.Equal(t, 8, v)

	_, ok = parseDigits([]byte("20"), 4)
	assert.False(t, ok, "too few bytes")

	_, ok = parseDigits([]byte("20x6"), 4)
	assert.False(t, ok, "non-digit")
}

// TestAllocNum is the PERF-006 gate: these functions exist so that integer
// fields cost nothing, and a regression to strconv would show up here.
func TestAllocNum(t *testing.T) {
	pid := []byte("31337")
	xid := []byte("-9223372036854775808")
	allocs.Zero(t, 100, func() {
		_, _ = parseUint(pid)
		_, _ = parseInt(xid)
		_, _ = parseInt32(pid)
		_, _ = parseDigits(pid, 4)
	})
}
