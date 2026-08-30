package pglogwatch

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseFixture reads every record from a fixture file, cloning each one so the
// assertions can look at all of them after the parser has moved on.
func parseFixture(t *testing.T, name string, cfg Config) ([]*OwnedRecord, *Parser) {
	t.Helper()
	p := New(bytes.NewReader(fixture(t, name)), cfg)
	var out []*OwnedRecord
	for p.Next() {
		out = append(out, p.Record().Clone())
	}
	require.NoError(t, p.Err())
	return out, p
}

// TestCSVColumnLayouts covers FMT-001: the three csvlog column layouts must be
// told apart from the line itself, because nothing in the file says which
// PostgreSQL version wrote it.
//
// The distinguishing evidence is only in the trailing columns, which is why a
// parser that reads the first twelve fields and stops -- as pgwatch's current
// regex does -- cannot tell these apart at all.
func TestCSVColumnLayouts(t *testing.T) {
	t.Run("26 columns, PostgreSQL 14-18", func(t *testing.T) {
		recs, p := parseFixture(t, "csv/pg14-basic.csv", Config{Format: FormatCSV})
		require.Len(t, recs, 6)
		assert.Equal(t, FormatCSV, p.DetectedFormat())

		r := recs[0]
		assert.Equal(t, "app_user", string(r.User))
		assert.Equal(t, "appdb", string(r.Database))
		assert.Equal(t, int32(31337), r.ProcessID)
		assert.Equal(t, "client backend", string(r.BackendType))
		assert.Equal(t, int64(8109862653847632261), r.QueryID)
	})

	t.Run("24 columns, PostgreSQL 13", func(t *testing.T) {
		recs, _ := parseFixture(t, "csv/pg13-basic.csv", Config{Format: FormatCSV})
		require.Len(t, recs, 2)
		// backend_type exists in this layout, leader_pid and query_id do
		// not, so they must read as absent rather than as garbage
		// borrowed from another column.
		assert.Equal(t, "client backend", string(recs[0].BackendType))
		assert.Zero(t, recs[0].LeaderPID)
		assert.Zero(t, recs[0].QueryID)
	})

	t.Run("23 columns, PostgreSQL 12", func(t *testing.T) {
		recs, _ := parseFixture(t, "csv/pg12-basic.csv", Config{Format: FormatCSV})
		require.Len(t, recs, 2)
		assert.Empty(t, recs[0].BackendType)
		assert.Zero(t, recs[0].LeaderPID)
		assert.Zero(t, recs[0].QueryID)
		// The columns that all three layouts share must still be right.
		assert.Equal(t, "app_user", string(recs[0].User))
		assert.Equal(t, SeverityLog, recs[0].Severity)
	})
}

// TestCSVAllColumnsRetrievable covers COR-001: parsing must be lossless. Every
// column PostgreSQL wrote has to come back, not just the ones a severity
// counter happens to need.
func TestCSVAllColumnsRetrievable(t *testing.T) {
	recs, _ := parseFixture(t, "csv/pg14-basic.csv", Config{Format: FormatCSV})
	require.Len(t, recs, 6)

	err := recs[1]
	assert.Equal(t, SeverityError, err.Severity)
	assert.Equal(t, "ERROR", string(err.RawSeverity))
	assert.Equal(t, [5]byte{'4', '2', 'P', '0', '1'}, err.SQLState)
	// Unescaping is deferred, so Message still holds the doubled quotes
	// exactly as PostgreSQL wrote them (PERF-009).
	assert.Equal(t, `relation ""no_such_table"" does not exist`, string(err.Message))
	assert.NotZero(t, err.Flags&FlagNeedsUnquote)
	assert.Equal(t, `relation "no_such_table" does not exist`,
		string(AppendUnquoted(nil, err.Message, FormatCSV)))
	assert.Equal(t, "SELECT * FROM no_such_table;", string(err.Query))
	assert.Equal(t, int32(15), err.QueryPos)
	assert.Equal(t, "parserOpenTable, parse_relation.c:1392", string(err.Location))
	assert.Equal(t, "3/15", string(err.VirtualXID))
	assert.Equal(t, "68b2c4a0.7a69", string(err.SessionID))
	assert.Equal(t, int64(7), err.SessionLineNum)
	assert.Equal(t, "SELECT", string(err.CommandTag))
	assert.Equal(t, "10.0.0.5:52344", string(err.ConnectionFrom))
	assert.Equal(t, "psql", string(err.ApplicationName))
	assert.False(t, err.Time.IsZero())
	assert.False(t, err.SessionStart.IsZero())

	fatal := recs[3]
	assert.Equal(t, SeverityFatal, fatal.Severity)
	assert.Equal(t, "Connection matched pg_hba.conf line 96.", string(fatal.Detail))

	// A background process writes no user, database or application name.
	ckpt := recs[4]
	assert.Empty(t, ckpt.User)
	assert.Empty(t, ckpt.Database)
	assert.Equal(t, "checkpointer", string(ckpt.BackendType))
}

// TestCSVSQLStateAbsent pins the meaning of the zero SQLState: absent, which
// is normal, rather than an error code of five NUL bytes.
func TestCSVSQLStateAbsent(t *testing.T) {
	recs, _ := parseFixture(t, "csv/pg14-basic.csv", Config{Format: FormatCSV})
	assert.Equal(t, [5]byte{'0', '0', '0', '0', '0'}, recs[0].SQLState,
		"PostgreSQL writes 00000 for successful completion, not an empty column")
}
