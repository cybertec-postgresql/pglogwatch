package pglogwatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonlog edge cases (E8, E9, FMT-002).

func TestJSONMissingKeysAreZeroValued(t *testing.T) {
	// E8 and FMT-002: absent keys yield zero-valued fields, not errors.
	//
	// This is the common case, not a corner one. PostgreSQL omits every key
	// that does not apply, so a checkpointer line carries five keys where a
	// failed query carries twenty. A parser that required its key set would
	// reject most of a real log.
	recs, p := parseFixture(t, "json/basic.json", Config{Format: FormatJSON})
	require.Len(t, recs, 3)
	assert.Zero(t, p.Stats().Malformed)

	ckpt := recs[2]
	assert.Equal(t, SeverityLog, ckpt.Severity)
	assert.Equal(t, "checkpoint starting: time", string(ckpt.Message))
	assert.Equal(t, int32(31338), ckpt.ProcessID)
	assert.Equal(t, "checkpointer", string(ckpt.BackendType))

	assert.Empty(t, ckpt.User)
	assert.Empty(t, ckpt.Database)
	assert.Empty(t, ckpt.SessionID)
	assert.Empty(t, ckpt.Statement)
	assert.Zero(t, ckpt.SessionLineNum)
	assert.Zero(t, ckpt.QueryID)
	assert.Equal(t, [5]byte{}, ckpt.SQLState)
	assert.True(t, ckpt.SessionStart.IsZero())
}

func TestJSONEmptyObject(t *testing.T) {
	// The limit of E8: an object with no keys at all is still a line of the
	// log and must not be an error.
	p := New(strings.NewReader("{}\n"), Config{Format: FormatJSON})
	require.True(t, p.Next())
	assert.Equal(t, SeverityUnknown, p.Record().Severity)
	assert.False(t, p.Next())
	require.NoError(t, p.Err())
}
func TestJSONEscapesAreDeferred(t *testing.T) {
	// E9 and PERF-009: backslash escapes pass through as written,
	// FlagNeedsUnquote is set, and AppendUnquoted resolves them on demand.
	recs, p := parseFixture(t, "json/escapes.json", Config{Format: FormatJSON})
	require.Len(t, recs, 2)
	assert.Zero(t, p.Stats().Malformed)

	bs := string([]byte{92})

	r := recs[0]
	assert.NotZero(t, r.Flags&FlagNeedsUnquote)
	assert.Contains(t, string(r.Message), bs+bs+"data",
		"the escape must still be in the field, unresolved")
	assert.Equal(t,
		"path C:"+bs+"data"+bs+"pg_log and a quote "+`"`+" and a tab \t",
		string(AppendUnquoted(nil, r.Message, FormatJSON)))

	r = recs[1]
	assert.NotZero(t, r.Flags&FlagNeedsUnquote)
	assert.Equal(t, "café 😀 is not valid here",
		string(AppendUnquoted(nil, r.Message, FormatJSON)),
		"a BMP escape and a surrogate pair must both resolve")
	assert.Equal(t, [5]byte{'2', '2', '0', '2', '1'}, r.SQLState)
}

func TestJSONQuoteInsideStringDoesNotEndIt(t *testing.T) {
	// The scanner must respect backslash-escaped quotes when finding the
	// end of a string. Getting this wrong shifts every following key by
	// one and produces a record that is wrong rather than malformed.
	in := `{"error_severity":"ERROR","message":"say \"hello\" now","backend_type":"client backend"}` + "\n"
	p := New(strings.NewReader(in), Config{Format: FormatJSON})
	require.True(t, p.Next())
	r := p.Record()
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, `say \"hello\" now`, string(r.Message))
	assert.Equal(t, "client backend", string(r.BackendType),
		"the key after an escaped quote must still be found")
}

func TestJSONTrailingBackslashInString(t *testing.T) {
	// A string ending in an escaped backslash: the closing quote is real,
	// and a scanner that treated the backslash as escaping it would run to
	// the end of the line.
	bs := string([]byte{92})
	in := `{"error_severity":"LOG","message":"ends with ` + bs + bs + `","pid":42}` + "\n"
	p := New(strings.NewReader(in), Config{Format: FormatJSON})
	require.True(t, p.Next())
	assert.Equal(t, int32(42), p.Record().ProcessID)
	assert.Equal(t, SeverityLog, p.Record().Severity)
}

func TestJSONMalformedLineIsCountedNotFatal(t *testing.T) {
	// FMT-010 and AC-007 for jsonlog.
	in := `{"error_severity":"LOG","message":"before"}` + "\n" +
		`not json at all` + "\n" +
		`{"error_severity":"LOG","message":"after"}` + "\n"
	var reported int
	p := New(strings.NewReader(in), Config{
		Format:      FormatJSON,
		OnMalformed: func([]byte, error) { reported++ },
	})
	var msgs []string
	for p.Next() {
		msgs = append(msgs, string(p.Record().Message))
	}
	require.NoError(t, p.Err())
	assert.Equal(t, []string{"before", "after"}, msgs)
	assert.Equal(t, int64(1), p.Stats().Malformed)
	assert.Equal(t, 1, reported)
}

func TestJSONInvalidUTF8PassesThrough(t *testing.T) {
	// COR-005 and E11 on the JSON path: PostgreSQL writes the message bytes
	// as they came from the client, so invalid UTF-8 reaches the log.
	in := "{\"error_severity\":\"LOG\",\"message\":\"bad \xff\xfe bytes\"}\n"
	p := New(strings.NewReader(in), Config{Format: FormatJSON})
	require.True(t, p.Next())
	assert.Contains(t, string(p.Record().Message), "\xff\xfe")
	assert.Zero(t, p.Stats().Malformed)
}

func TestJSONNumericAndNullValues(t *testing.T) {
	// PostgreSQL writes numbers unquoted and can write null. Both must be
	// handled without confusing the key scanner.
	in := `{"pid":31337,"line_num":7,"txid":0,"query_id":8109862653847632261,` +
		`"error_severity":"LOG","detail":null,"message":"ok"}` + "\n"
	p := New(strings.NewReader(in), Config{Format: FormatJSON})
	require.True(t, p.Next())
	r := p.Record()
	assert.Equal(t, int32(31337), r.ProcessID)
	assert.Equal(t, int64(7), r.SessionLineNum)
	assert.Equal(t, int64(8109862653847632261), r.QueryID)
	assert.Equal(t, "ok", string(r.Message))
	assert.Empty(t, r.Detail, "null must read as absent, not as the text null")
}
