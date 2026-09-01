package pglogwatch

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The exported surface a caller sees but no other test exercises: the
// stringers, the error identities, and the iterator's failure path. None of
// these is on the hot path, which is exactly why they were untested -- and why
// getting one wrong is a bug that reaches a user through a log message rather
// than through a benchmark.

func TestFormatString(t *testing.T) {
	// The names are the log_destination values postgresql.conf uses, which
	// is what makes an error message actionable: "csvlog", not "CSV".
	for _, tc := range []struct {
		f    Format
		want string
	}{
		{FormatAuto, "auto"},
		{FormatStderr, "stderr"},
		{FormatCSV, "csvlog"},
		{FormatJSON, "jsonlog"},
		{Format(99), "unknown"},
	} {
		assert.Equal(t, tc.want, tc.f.String())
	}
}

func TestFormatDefaultGlob(t *testing.T) {
	// FileSet uses these when Glob is empty. stderr and auto both fall back
	// to *.log: auto cannot know better before it has read anything, and
	// *.log is the pattern that finds a default installation either way.
	assert.Equal(t, "*.csv", FormatCSV.defaultGlob())
	assert.Equal(t, "*.json", FormatJSON.defaultGlob())
	assert.Equal(t, "*.log", FormatStderr.defaultGlob())
	assert.Equal(t, "*.log", FormatAuto.defaultGlob())
}

func TestSentinelErrorsReadWell(t *testing.T) {
	// Every message is prefixed with the package name, since a caller sees
	// it with no other context, and none ends in punctuation.
	for _, err := range []error{
		ErrMalformedLine, ErrRecordTooLarge, ErrBadLinePrefix, ErrNotSeekable,
		errShortRecord, errPrefixMismatch, errBadJSON, errUnterminated,
		errBadWhence, errNegativeOffset,
	} {
		msg := err.Error()
		assert.True(t, bytes.HasPrefix([]byte(msg), []byte("pglogwatch: ")),
			"%q should name the package", msg)
		assert.NotContains(t, msg, "\n")
	}
}

func TestMalformedErrorIdentities(t *testing.T) {
	// The point of parseError.Is: a caller that does not care why a line was
	// bad tests one sentinel, and every specific reason answers to it.
	for _, err := range []error{errShortRecord, errPrefixMismatch, errBadJSON, errUnterminated} {
		assert.ErrorIs(t, err, ErrMalformedLine, "%v should satisfy ErrMalformedLine", err)
	}
	assert.ErrorIs(t, ErrRecordTooLarge, ErrMalformedLine)

	// And the ones that are not bad input must not, or a caller counting
	// malformed lines would count its own configuration mistakes.
	assert.NotErrorIs(t, ErrBadLinePrefix, ErrMalformedLine)
	assert.NotErrorIs(t, ErrNotSeekable, ErrMalformedLine)
	assert.NotErrorIs(t, errBadWhence, ErrMalformedLine)
	assert.NotErrorIs(t, errNegativeOffset, ErrMalformedLine)
}

func TestAllYieldsFatalErrorLast(t *testing.T) {
	// A fatal error reaches the range loop as a final yield with a nil
	// record, which is the whole reason All can be used without a separate
	// Err() call after the loop.
	// "%z" is not a log_line_prefix escape, which New records as fatal:
	// unlike a malformed line, it is the caller's mistake and continuing
	// would silently parse the whole log against the wrong template.
	// (A trailing bare "%" is NOT this case -- that is kept as literal
	// text, on the grounds that it is far likelier to be a typo in a
	// working configuration than a reason to refuse the log.)
	p := New(bytes.NewReader(nil), Config{Format: FormatStderr, LinePrefix: "%z "})

	var got error
	var records int
	for r, err := range p.All() {
		if err != nil {
			got = err
			assert.Nil(t, r, "the error yield carries no record")
			continue
		}
		records++
	}
	require.ErrorIs(t, got, ErrBadLinePrefix)
	assert.Zero(t, records)
}

func TestAllStopsWhereTheLoopBroke(t *testing.T) {
	// Documented behaviour: breaking out leaves the parser positioned after
	// the last record consumed, so a second range continues rather than
	// restarting or yielding nothing.
	p := New(bytes.NewReader(fixture(t, "stderr/basic.log")), Config{Format: FormatStderr})

	first := 0
	for range p.All() {
		first++
		if first == 2 {
			break
		}
	}
	require.Equal(t, 2, first)

	rest := 0
	for _, err := range p.All() {
		require.NoError(t, err)
		rest++
	}
	assert.Positive(t, rest, "the second range should continue, not start over")
}

func TestJSONBooleanAndNullValues(t *testing.T) {
	// jsonlog never writes a boolean, but the scanner accepts one so that a
	// key it does not recognise cannot derail the record. null must resolve
	// to the zero value rather than to the four-letter text (E8).
	line := `{"error_severity":"LOG","message":"x","dbname":null,"leader_pid":true,"query_id":42}`
	p := New(bytes.NewReader([]byte(line+"\n")), Config{Format: FormatJSON})

	require.True(t, p.Next())
	r := p.Record()
	assert.Equal(t, SeverityLog, r.Severity)
	assert.Equal(t, []byte("x"), r.Message)
	assert.Empty(t, r.Database, "a null value is absent, not the text \"null\"")
	assert.Equal(t, int64(42), r.QueryID)
	require.NoError(t, p.Err())
}

func TestZoneAbbreviationsAcrossHemispheres(t *testing.T) {
	// One per offset family, including the two half-hour zones, because an
	// offset table is the kind of thing where a transcription slip in one
	// row is invisible until a customer in that zone reports wrong times.
	for _, tc := range []struct {
		abbrev string
		secs   int
	}{
		{"UTC", 0}, {"CET", 3600}, {"CEST", 7200}, {"MSK", 10800},
		{"IST", 5*3600 + 30*60}, {"ACST", 9*3600 + 30*60},
		{"JST", 9 * 3600}, {"NZDT", 13 * 3600},
		{"HST", -10 * 3600}, {"PST", -8 * 3600}, {"EST", -5 * 3600},
		{"BRT", -3 * 3600},
	} {
		secs, ok := zoneAbbrevOffset([]byte(tc.abbrev))
		assert.True(t, ok, "%s should be known", tc.abbrev)
		assert.Equal(t, tc.secs, secs, "%s", tc.abbrev)
	}

	// E20: an unknown abbreviation is not an error. It falls back to UTC,
	// because refusing to parse a record over a zone name would lose the
	// record, and the timestamp is right to within the offset either way.
	_, ok := zoneAbbrevOffset([]byte("XYZ"))
	assert.False(t, ok)
}
