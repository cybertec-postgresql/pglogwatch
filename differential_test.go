package pglogwatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// COR-004: csvlog and jsonlog describing the same server activity must yield
// equal Record values for every shared field.
//
// This is the test that keeps three independently written parsers honest.
// Each format has its own unit tests, and all of them can pass while the three
// disagree about what a field means -- whether ConnectionFrom includes the
// port, whether Statement or Query holds a statement, whether an absent field
// is nil or empty. Those disagreements surface as a consumer getting different
// answers from the same server depending on a GUC, which is the worst kind of
// bug to debug because nothing is broken anywhere.

// event describes one log event in all three destinations.
type event struct {
	name   string
	csv    string
	json   string
	stderr string
}

const differentialPrefix = "%m [%p] %q%u@%d "

func differentialEvents() []event {
	return []event{{
		name: "a slow statement",
		csv: `2026-08-30 10:11:12.123 CEST,"app_user","appdb",31337,"10.0.0.5:52344",` +
			`68b2c4a0.7a69,7,"SELECT",2026-08-30 10:10:00 CEST,3/15,0,LOG,00000,` +
			`"duration: 1234.567 ms  statement: SELECT 1",,,,,,,,,"psql","client backend",,0`,
		json: `{"timestamp":"2026-08-30 10:11:12.123 CEST","user":"app_user","dbname":"appdb",` +
			`"pid":31337,"remote_host":"10.0.0.5","remote_port":52344,"session_id":"68b2c4a0.7a69",` +
			`"line_num":7,"ps":"SELECT","session_start":"2026-08-30 10:10:00 CEST","vxid":"3/15",` +
			`"txid":0,"error_severity":"LOG","state_code":"00000",` +
			`"message":"duration: 1234.567 ms  statement: SELECT 1",` +
			`"application_name":"psql","backend_type":"client backend"}`,
		stderr: "2026-08-30 10:11:12.123 CEST [31337] app_user@appdb LOG:  " +
			"duration: 1234.567 ms  statement: SELECT 1",
	}, {
		name: "an error with a statement",
		csv: `2026-08-30 10:11:13.001 CEST,"app_user","appdb",31337,"10.0.0.5:52344",` +
			`68b2c4a0.7a69,8,"SELECT",2026-08-30 10:10:00 CEST,3/15,0,ERROR,42P01,` +
			`"boom",,,,,,"SELECT * FROM nope;",,,"psql","client backend",,0`,
		json: `{"timestamp":"2026-08-30 10:11:13.001 CEST","user":"app_user","dbname":"appdb",` +
			`"pid":31337,"session_id":"68b2c4a0.7a69","line_num":8,"ps":"SELECT",` +
			`"session_start":"2026-08-30 10:10:00 CEST","vxid":"3/15","txid":0,` +
			`"error_severity":"ERROR",` +
			`"state_code":"42P01","message":"boom","statement":"SELECT * FROM nope;",` +
			`"application_name":"psql","backend_type":"client backend"}`,
		stderr: "2026-08-30 10:11:13.001 CEST [31337] app_user@appdb ERROR:  boom\n" +
			"2026-08-30 10:11:13.001 CEST [31337] app_user@appdb STATEMENT:  SELECT * FROM nope;",
	}, {
		name: "a background process event",
		csv: `2026-08-30 10:11:14.500 CEST,,,31338,,68b2c4a1.7a6a,1,,2026-08-30 10:00:00 CEST,,0,` +
			`LOG,00000,"checkpoint starting: time",,,,,,,,,,"checkpointer",,0`,
		json: `{"timestamp":"2026-08-30 10:11:14.500 CEST","pid":31338,` +
			`"session_id":"68b2c4a1.7a6a","line_num":1,` +
			`"session_start":"2026-08-30 10:00:00 CEST",` +
			`"error_severity":"LOG","state_code":"00000",` +
			`"message":"checkpoint starting: time","backend_type":"checkpointer"}`,
		stderr: "2026-08-30 10:11:14.500 CEST [31338] LOG:  checkpoint starting: time",
	}}
}

func parseOne(t *testing.T, in string, cfg Config) *OwnedRecord {
	t.Helper()
	p := New(strings.NewReader(in+"\n"), cfg)
	require.True(t, p.Next(), "no record from %q", in)
	r := p.Record().Clone()
	require.NoError(t, p.Err())
	assert.Zero(t, p.Stats().Malformed)
	return r
}

func TestDifferentialCSVvsJSON(t *testing.T) {
	for _, e := range differentialEvents() {
		t.Run(e.name, func(t *testing.T) {
			c := parseOne(t, e.csv, Config{Format: FormatCSV})
			j := parseOne(t, e.json, Config{Format: FormatJSON})

			assert.Equal(t, c.Severity, j.Severity)
			assert.Equal(t, string(c.RawSeverity), string(j.RawSeverity))
			assert.Equal(t, string(c.User), string(j.User))
			assert.Equal(t, string(c.Database), string(j.Database))
			assert.Equal(t, c.ProcessID, j.ProcessID)
			assert.Equal(t, string(c.SessionID), string(j.SessionID))
			assert.Equal(t, c.SessionLineNum, j.SessionLineNum)
			assert.Equal(t, string(c.CommandTag), string(j.CommandTag))
			assert.Equal(t, string(c.VirtualXID), string(j.VirtualXID))
			assert.Equal(t, c.TransactionID, j.TransactionID)
			assert.Equal(t, c.SQLState, j.SQLState)
			assert.Equal(t, string(c.Message), string(j.Message))
			assert.Equal(t, string(c.Statement), string(j.Statement))
			assert.Equal(t, string(c.ApplicationName), string(j.ApplicationName))
			assert.Equal(t, string(c.BackendType), string(j.BackendType))
			assert.Equal(t, c.Duration, j.Duration)
			assert.Equal(t, c.Flags&FlagHasDuration, j.Flags&FlagHasDuration)
			assert.True(t, c.Time.Equal(j.Time), "csvlog %s, jsonlog %s", c.Time, j.Time)
			assert.True(t, c.SessionStart.Equal(j.SessionStart),
				"csvlog %s, jsonlog %s", c.SessionStart, j.SessionStart)
		})
	}
}

func TestDifferentialCSVvsStderr(t *testing.T) {
	// stderr carries fewer fields -- its prefix decides which -- so only the
	// ones the chosen prefix provides are compared. That is not a weaker
	// test: it is the actual COR-004 obligation, since a field the prefix
	// omits was never in the log.
	for _, e := range differentialEvents() {
		t.Run(e.name, func(t *testing.T) {
			c := parseOne(t, e.csv, Config{Format: FormatCSV})
			s := parseOne(t, e.stderr, Config{
				Format:     FormatStderr,
				LinePrefix: differentialPrefix,
			})

			assert.Equal(t, c.Severity, s.Severity)
			assert.Equal(t, string(c.RawSeverity), string(s.RawSeverity))
			assert.Equal(t, string(c.User), string(s.User))
			assert.Equal(t, string(c.Database), string(s.Database))
			assert.Equal(t, c.ProcessID, s.ProcessID)
			assert.Equal(t, string(c.Message), string(s.Message))
			assert.Equal(t, string(c.Statement), string(s.Statement))
			assert.Equal(t, string(c.Query), string(s.Query))
			assert.Equal(t, c.Duration, s.Duration)
			assert.True(t, c.Time.Equal(s.Time), "csvlog %s, stderr %s", c.Time, s.Time)
		})
	}
}

func TestDifferentialAbsentFieldsAgree(t *testing.T) {
	// The subtlest disagreement: whether an absent field is nil or empty.
	// Both are "no value", but a caller testing v == nil gets different
	// answers, so the formats must at least agree on emptiness.
	for _, e := range differentialEvents() {
		t.Run(e.name, func(t *testing.T) {
			c := parseOne(t, e.csv, Config{Format: FormatCSV})
			j := parseOne(t, e.json, Config{Format: FormatJSON})
			pairs := []struct {
				name string
				c, j []byte
			}{
				{"User", c.User, j.User},
				{"Database", c.Database, j.Database},
				{"Detail", c.Detail, j.Detail},
				{"Hint", c.Hint, j.Hint},
				{"Context", c.Context, j.Context},
				{"InternalQuery", c.InternalQuery, j.InternalQuery},
				{"CommandTag", c.CommandTag, j.CommandTag},
			}
			for _, pr := range pairs {
				assert.Equal(t, len(pr.c) == 0, len(pr.j) == 0,
					"%s: csvlog empty=%v, jsonlog empty=%v", pr.name, len(pr.c) == 0, len(pr.j) == 0)
			}
		})
	}
}
