package pglogwatch

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonlog correctness against encoding/json (AC-002, FMT-002).
//
// CON-004 keeps encoding/json out of the package, and this test is why that
// ban is affordable: the standard library decoder is used HERE, in a test, as
// the oracle for the hand-written scanner. Every documented key is compared,
// so "we wrote our own JSON parser" is a claim with evidence rather than a
// design note.

// jsonReference mirrors PostgreSQL's jsonlog object, keyed exactly as the
// documentation spells the keys (FMT-002).
type jsonReference struct {
	Timestamp        string `json:"timestamp"`
	User             string `json:"user"`
	DBName           string `json:"dbname"`
	PID              int32  `json:"pid"`
	RemoteHost       string `json:"remote_host"`
	RemotePort       int    `json:"remote_port"`
	SessionID        string `json:"session_id"`
	LineNum          int64  `json:"line_num"`
	PS               string `json:"ps"`
	SessionStart     string `json:"session_start"`
	VXID             string `json:"vxid"`
	TXID             int64  `json:"txid"`
	ErrorSeverity    string `json:"error_severity"`
	StateCode        string `json:"state_code"`
	Message          string `json:"message"`
	Detail           string `json:"detail"`
	Hint             string `json:"hint"`
	InternalQuery    string `json:"internal_query"`
	InternalPosition int32  `json:"internal_position"`
	Context          string `json:"context"`
	Statement        string `json:"statement"`
	CursorPosition   int32  `json:"cursor_position"`
	FuncName         string `json:"func_name"`
	FileName         string `json:"file_name"`
	FileLineNum      int64  `json:"file_line_num"`
	ApplicationName  string `json:"application_name"`
	BackendType      string `json:"backend_type"`
	LeaderPID        int32  `json:"leader_pid"`
	QueryID          int64  `json:"query_id"`
}

func TestJSONMatchesEncodingJSON(t *testing.T) {
	raw := fixture(t, "json/basic.json")
	p := New(bytes.NewReader(raw), Config{Format: FormatJSON})

	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	n := 0
	var buf []byte
	for p.Next() {
		require.Less(t, n, len(lines))
		var want jsonReference
		require.NoError(t, json.Unmarshal(lines[n], &want))

		r := p.Record()
		unquoted := func(v []byte) string {
			if r.Flags&FlagNeedsUnquote == 0 {
				return string(v)
			}
			buf = AppendUnquoted(buf[:0], v, FormatJSON)
			return string(buf)
		}

		assert.Equal(t, want.User, string(r.User), "line %d: user", n)
		assert.Equal(t, want.DBName, string(r.Database), "line %d: dbname", n)
		assert.Equal(t, want.PID, r.ProcessID, "line %d: pid", n)
		assert.Equal(t, want.SessionID, string(r.SessionID), "line %d: session_id", n)
		assert.Equal(t, want.LineNum, r.SessionLineNum, "line %d: line_num", n)
		assert.Equal(t, want.PS, string(r.CommandTag), "line %d: ps", n)
		assert.Equal(t, want.VXID, string(r.VirtualXID), "line %d: vxid", n)
		assert.Equal(t, want.TXID, r.TransactionID, "line %d: txid", n)
		assert.Equal(t, want.ErrorSeverity, string(r.RawSeverity), "line %d: error_severity", n)
		assert.Equal(t, want.Message, unquoted(r.Message), "line %d: message", n)
		assert.Equal(t, want.Detail, unquoted(r.Detail), "line %d: detail", n)
		assert.Equal(t, want.Hint, unquoted(r.Hint), "line %d: hint", n)
		assert.Equal(t, want.InternalQuery, unquoted(r.InternalQuery), "line %d: internal_query", n)
		assert.Equal(t, want.Context, unquoted(r.Context), "line %d: context", n)
		assert.Equal(t, want.Statement, unquoted(r.Statement), "line %d: statement", n)
		assert.Equal(t, want.CursorPosition, r.QueryPos, "line %d: cursor_position", n)
		assert.Equal(t, want.InternalPosition, r.InternalQueryPos, "line %d: internal_position", n)
		assert.Equal(t, want.ApplicationName, string(r.ApplicationName), "line %d: application_name", n)
		assert.Equal(t, want.BackendType, string(r.BackendType), "line %d: backend_type", n)
		assert.Equal(t, want.LeaderPID, r.LeaderPID, "line %d: leader_pid", n)
		assert.Equal(t, want.QueryID, r.QueryID, "line %d: query_id", n)

		if want.StateCode != "" {
			assert.Equal(t, want.StateCode, string(r.SQLState[:]), "line %d: state_code", n)
		}
		if want.RemoteHost != "" {
			assert.Contains(t, string(r.ConnectionFrom), want.RemoteHost, "line %d: remote_host", n)
		}
		if want.FuncName != "" {
			// FMT-002 splits the source location across three keys;
			// Record joins them (§4.2).
			loc := string(r.Location)
			assert.Contains(t, loc, want.FuncName, "line %d: location func", n)
			assert.Contains(t, loc, want.FileName, "line %d: location file", n)
		}
		n++
	}
	require.NoError(t, p.Err())
	assert.Equal(t, len(lines), n)
}

func TestJSONSeverityAndTimestamp(t *testing.T) {
	recs, p := parseFixture(t, "json/basic.json", Config{Format: FormatJSON})
	require.Len(t, recs, 3)
	assert.Zero(t, p.Stats().Malformed)

	assert.Equal(t, SeverityLog, recs[0].Severity)
	assert.Equal(t, SeverityError, recs[1].Severity)
	assert.Equal(t, [5]byte{'4', '2', 'P', '0', '1'}, recs[1].SQLState)

	assert.Equal(t, 2026, recs[0].Time.Year())
	assert.Equal(t, 123000000, recs[0].Time.Nanosecond())
	assert.False(t, recs[0].SessionStart.IsZero(), "session_start must be parsed too")
}

func TestJSONOneObjectPerLine(t *testing.T) {
	// FMT-006: PostgreSQL writes exactly one object per physical line and
	// the parser must NOT attempt multi-line assembly. A pretty-printed
	// object is not something PostgreSQL produces, and trying to support it
	// would make every malformed line eat the rest of the file.
	in := "{\"error_severity\":\"LOG\",\"message\":\"one\"}\n" +
		"{\"error_severity\":\"LOG\",\n \"message\":\"two\"}\n" +
		"{\"error_severity\":\"LOG\",\"message\":\"three\"}\n"
	p := New(strings.NewReader(in), Config{Format: FormatJSON})
	var msgs []string
	for p.Next() {
		msgs = append(msgs, string(p.Record().Message))
	}
	require.NoError(t, p.Err())
	assert.Equal(t, []string{"one", "three"}, msgs,
		"the split object must be counted as malformed, not joined")
	assert.Equal(t, int64(2), p.Stats().Malformed)
}

func TestJSONDuration(t *testing.T) {
	recs, _ := parseFixture(t, "json/basic.json", Config{Format: FormatJSON})
	require.Len(t, recs, 3)
	assert.NotZero(t, recs[0].Flags&FlagHasDuration)
	assert.Equal(t, "duration: 1234.567 ms  statement: SELECT * FROM orders", string(recs[0].Message))
}
