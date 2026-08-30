package pglogwatch

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Format auto-detection (FMT-005).
//
// Detection is what makes Config{} usable, and it is the reason pgwatch can
// stop refusing to parse anything that is not csvlog (§7.7). It also has to be
// conservative: guessing csvlog for a stderr log does not produce an error, it
// produces records whose fields are all shifted, which is worse.

func TestDetectFormatFromFixtures(t *testing.T) {
	cases := []struct {
		file string
		want Format
	}{
		{"csv/pg14-basic.csv", FormatCSV},
		{"csv/pg12-basic.csv", FormatCSV},
		{"csv/quotes-newlines-commas.csv", FormatCSV},
		{"json/basic.json", FormatJSON},
		{"json/escapes.json", FormatJSON},
		{"stderr/basic.log", FormatStderr},
		{"stderr/multiline.log", FormatStderr},
		{"stderr/lc_messages_ru.log", FormatStderr},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			p := New(bytes.NewReader(fixture(t, c.file)), Config{})
			require.True(t, p.Next(), "no record parsed")
			assert.Equal(t, c.want, p.DetectedFormat())
			for p.Next() { //nolint:revive // drain
			}
			require.NoError(t, p.Err())
			assert.Zero(t, p.Stats().Malformed)
		})
	}
}

func TestDetectJSONFromLeadingBrace(t *testing.T) {
	// FMT-005: a leading '{' implies jsonlog. Nothing else PostgreSQL
	// writes starts a line with one.
	p := New(strings.NewReader(`{"error_severity":"LOG","message":"hi"}`+"\n"), Config{})
	require.True(t, p.Next())
	assert.Equal(t, FormatJSON, p.DetectedFormat())
	assert.Equal(t, "hi", string(p.Record().Message))
}

func TestDetectCSVNeedsAColumnBoundary(t *testing.T) {
	// FMT-005 does not say "starts with a timestamp" -- it says a timestamp
	// FOLLOWED BY A COMMA AT A VALID CSV COLUMN BOUNDARY. Every stderr log
	// also starts with a timestamp, so the weaker test would misidentify
	// most of the world's PostgreSQL logs.
	stderrLine := "2026-08-30 10:11:12.123 CEST [31337] LOG:  a message, with a comma\n"
	p := New(strings.NewReader(stderrLine), Config{})
	require.True(t, p.Next())
	assert.Equal(t, FormatStderr, p.DetectedFormat())
	assert.Equal(t, "a message, with a comma", string(p.Record().Message))
}

func TestDetectStderrWithCommaHeavyMessage(t *testing.T) {
	// A stderr log whose messages contain many commas must still not look
	// like csvlog.
	in := "2026-08-30 10:11:12.123 CEST [31337] LOG:  statement: SELECT a, b, c, d, e FROM t\n" +
		"2026-08-30 10:11:13.123 CEST [31337] LOG:  statement: SELECT f, g, h, i, j FROM t\n"
	p := New(strings.NewReader(in), Config{})
	require.True(t, p.Next())
	assert.Equal(t, FormatStderr, p.DetectedFormat())
	assert.Equal(t, int32(31337), p.Record().ProcessID)
}

func TestDetectIsOverridable(t *testing.T) {
	// FMT-005: explicit configuration wins. A caller who knows the format
	// must not be second-guessed, and this is also the escape hatch when
	// detection gets an unusual log wrong.
	csvIn := fixture(t, "csv/pg14-basic.csv")
	p := New(bytes.NewReader(csvIn), Config{Format: FormatStderr})
	require.True(t, p.Next())
	assert.Equal(t, FormatStderr, p.DetectedFormat(),
		"a configured format must never be re-detected")
}

func TestDetectSkipsLeadingBlankLines(t *testing.T) {
	// "The first NON-EMPTY line" (FMT-005). A log that begins with a blank
	// line -- which happens after a truncating rotation -- must still be
	// detected.
	in := "\n\n" + string(fixture(t, "json/basic.json"))
	p := New(strings.NewReader(in), Config{})
	require.True(t, p.Next())
	assert.Equal(t, FormatJSON, p.DetectedFormat())
}

func TestDetectEmptyInput(t *testing.T) {
	// Nothing to detect from, and that is not an error (E16).
	p := New(strings.NewReader(""), Config{})
	assert.False(t, p.Next())
	require.NoError(t, p.Err())
}

func TestDetectSurvivesReset(t *testing.T) {
	// A parser walking a directory may meet files in different formats;
	// each Reset must detect afresh rather than reuse the last answer.
	p := New(bytes.NewReader(fixture(t, "csv/pg14-basic.csv")), Config{})
	require.True(t, p.Next())
	require.Equal(t, FormatCSV, p.DetectedFormat())

	p.Reset(bytes.NewReader(fixture(t, "json/basic.json")))
	require.True(t, p.Next())
	assert.Equal(t, FormatJSON, p.DetectedFormat())
	assert.Equal(t, "duration: 1234.567 ms  statement: SELECT * FROM orders",
		string(p.Record().Message))
}
