package pglogwatch

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The csvlog edge cases from §9.7. Each one is a way a naive line-and-comma
// splitter goes wrong, which is what the existing regex-based implementation
// is: a comma inside a quoted field is not a field boundary, a newline inside
// one is not a record boundary, and a doubled quote is one character.

func TestCSVEdgeDoubledQuote(t *testing.T) {
	// E1 and AC-005: one record, FlagNeedsUnquote set, and no eager
	// unescaping -- Message still holds the doubled quotes as written.
	recs, _ := parseFixture(t, "csv/quotes-newlines-commas.csv", Config{Format: FormatCSV})
	require.Len(t, recs, 4)

	r := recs[0]
	assert.Equal(t, SeverityError, r.Severity)
	assert.NotZero(t, r.Flags&FlagNeedsUnquote, "a doubled quote must set FlagNeedsUnquote")
	assert.Equal(t, `syntax error at or near ""FROM""`, string(r.Message),
		"the field must be handed over exactly as written, unescaping deferred")
	assert.Equal(t, `syntax error at or near "FROM"`,
		string(AppendUnquoted(nil, r.Message, FormatCSV)),
		"AppendUnquoted must reproduce the original text exactly (AC-005)")
}

func TestCSVEdgeEmbeddedNewline(t *testing.T) {
	// E2: a newline inside a quoted field does not end the record.
	recs, _ := parseFixture(t, "csv/quotes-newlines-commas.csv", Config{Format: FormatCSV})
	require.Len(t, recs, 4, "the three-line record must be one record, not three")

	r := recs[1]
	assert.NotZero(t, r.Flags&FlagMultiline, "an embedded newline must set FlagMultiline")
	assert.Equal(t, "statement: SELECT a, b, c\nFROM t\nWHERE a = 1", string(r.Message))
	assert.Equal(t, 2, strings.Count(string(r.Raw), "\n"), "Raw must span all three physical lines")
}

func TestCSVEdgeEmbeddedComma(t *testing.T) {
	// E3: a comma inside a quoted field is not a field boundary. If it
	// were, every field after it would shift and the record would be
	// silently wrong rather than visibly broken.
	recs, _ := parseFixture(t, "csv/quotes-newlines-commas.csv", Config{Format: FormatCSV})
	require.Len(t, recs, 4)

	r := recs[2]
	assert.Equal(t, "duration: 1.000 ms  statement: SELECT 1, 2, 3", string(r.Message))
	assert.Equal(t, "psql", string(r.ApplicationName), "columns after the comma must not shift")

	r = recs[3]
	assert.Equal(t, `invalid input syntax for type integer: ""a,b""`, string(r.Message))
	assert.Equal(t, [5]byte{'2', '2', 'P', '0', '2'}, r.SQLState)
}

func TestCSVEdgeEmptyTrailingColumns(t *testing.T) {
	// E4: a 26-column line whose leader_pid and query_id are empty. Empty
	// is not zero-with-an-error: the record parses, and both read as 0.
	recs, _ := parseFixture(t, "csv/pg14-basic.csv", Config{Format: FormatCSV})
	require.Len(t, recs, 6)

	r := recs[5]
	assert.Equal(t, "parallel worker reporting in", string(r.Message))
	assert.Zero(t, r.LeaderPID)
	assert.Zero(t, r.QueryID)
}

func TestCSVEdgeCRLF(t *testing.T) {
	// E10 and COR-006: no field may end with a carriage return.
	recs, p := parseFixture(t, "csv/crlf.csv", Config{Format: FormatCSV})
	require.Len(t, recs, 3)
	assert.Zero(t, p.Stats().Malformed)
	for i, r := range recs {
		for name, v := range map[string][]byte{
			"Message":         r.Message,
			"ApplicationName": r.ApplicationName,
			"BackendType":     r.BackendType,
		} {
			assert.False(t, bytes.HasSuffix(v, []byte("\r")),
				"record %d field %s ends with CR: %q", i, name, v)
		}
	}
	assert.Equal(t, "client backend", string(recs[0].BackendType),
		"the last column of a CRLF line must not keep its carriage return")
}

func TestCSVEdgeInvalidUTF8(t *testing.T) {
	// E11 and COR-005: bytes pass through unchanged, neither replaced nor
	// rejected. A log parser is not a place to correct the log.
	recs, p := parseFixture(t, "csv/invalid-utf8.csv", Config{Format: FormatCSV})
	require.Len(t, recs, 1)
	assert.Zero(t, p.Stats().Malformed)
	assert.Contains(t, string(recs[0].Message), "\xff\xfe")
	assert.Contains(t, string(recs[0].Message), "\x80\x81")
}

func TestCSVEdgeTruncatedTail(t *testing.T) {
	// AC-008 and FMT-009, both directions.
	t.Run("emitted by default with FlagTruncated", func(t *testing.T) {
		recs, _ := parseFixture(t, "csv/truncated-tail.csv", Config{Format: FormatCSV})
		require.Len(t, recs, 3)
		assert.NotZero(t, recs[2].Flags&FlagTruncated)
	})
	t.Run("discarded on request", func(t *testing.T) {
		recs, _ := parseFixture(t, "csv/truncated-tail.csv",
			Config{Format: FormatCSV, NoTruncatedTail: true})
		assert.Len(t, recs, 2)
	})
}

func TestCSVEdgeShortLineIsMalformed(t *testing.T) {
	// FMT-010 and AC-007: an unparseable line is counted and skipped, the
	// stream continues, and Err stays nil.
	in := "not,enough,columns\n" + string(fixture(t, "csv/pg14-basic.csv"))
	var reported [][]byte
	p := New(strings.NewReader(in), Config{
		Format: FormatCSV,
		OnMalformed: func(line []byte, err error) {
			reported = append(reported, bytes.Clone(line))
			assert.ErrorIs(t, err, ErrMalformedLine)
		},
	})
	n := 0
	for p.Next() {
		n++
	}
	require.NoError(t, p.Err(), "a malformed line must never be fatal")
	assert.Equal(t, 6, n, "every good record after the bad line must still arrive")
	assert.Equal(t, int64(1), p.Stats().Malformed)
	require.Len(t, reported, 1)
	assert.Equal(t, "not,enough,columns", string(reported[0]))
}

func TestCSVRecordOffsets(t *testing.T) {
	// IFC-006: Record.Offset is a byte offset into the stream, which is
	// what makes resumption a single Seek instead of a re-read.
	raw := fixture(t, "csv/pg14-basic.csv")
	recs, _ := parseFixture(t, "csv/pg14-basic.csv", Config{Format: FormatCSV})
	require.Len(t, recs, 6)

	assert.Zero(t, recs[0].Offset)
	for _, r := range recs {
		require.LessOrEqual(t, int(r.Offset), len(raw))
		assert.True(t, bytes.HasPrefix(raw[r.Offset:], r.Raw),
			"Raw must be exactly the bytes at Offset")
	}
}
