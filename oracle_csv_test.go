//go:build integration

package pglogwatch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC-001's oracle test: PostgreSQL itself decides what a csvlog file means.
//
// The csvlog format exists so that a log can be loaded into a table with COPY,
// and the PostgreSQL documentation publishes the postgres_log table definition
// for exactly that. That makes the server an independent oracle: load the same
// file both ways and every field must agree. It catches the class of bug no
// hand-written expectation can, because the expectations were written by the
// same person who wrote the parser.
//
// Build-tagged out of the normal run: it needs a live server, and TST §6.1
// scopes it to the container-backed integration job. Run it with
//
//	go test -tags integration -run TestOracle ./...
//
// and PGLOGWATCH_TEST_DSN pointing at a throwaway database. psql does the
// loading, so the test adds no dependency to go.mod -- pulling in a driver to
// verify a zero-dependency parser would undercut PKG-002.

const postgresLogDDL = `
DROP TABLE IF EXISTS pglogwatch_oracle;
CREATE TABLE pglogwatch_oracle (
  log_time timestamp(3) with time zone,
  user_name text,
  database_name text,
  process_id integer,
  connection_from text,
  session_id text,
  session_line_num bigint,
  command_tag text,
  session_start_time timestamp with time zone,
  virtual_transaction_id text,
  transaction_id bigint,
  error_severity text,
  sql_state_code text,
  message text,
  detail text,
  hint text,
  internal_query text,
  internal_query_pos integer,
  context text,
  query text,
  query_pos integer,
  location text,
  application_name text,
  backend_type text,
  leader_pid integer,
  query_id bigint
);`

// oracleSeparator delimits fields in the psql output. A byte PostgreSQL never
// writes into a log, so it cannot be confused with field content the way a tab
// or a pipe could.
const oracleSeparator = "\x01"

func TestOracleCSVAgreesWithPostgres(t *testing.T) {
	dsn := os.Getenv("PGLOGWATCH_TEST_DSN")
	if dsn == "" {
		t.Skip("set PGLOGWATCH_TEST_DSN to run the oracle test")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql not on PATH")
	}

	// Only the 26-column fixture: the table definition above is the
	// PostgreSQL 14+ one, and loading a narrower file into it would test
	// COPY's column defaulting rather than this parser.
	const fixtureName = "csv/pg14-basic.csv"
	abs, err := filepath.Abs(filepath.Join("testdata", fixtureName))
	require.NoError(t, err)

	psql(t, dsn, postgresLogDDL)
	psql(t, dsn, `\copy pglogwatch_oracle FROM '`+filepath.ToSlash(abs)+`' WITH csv`)

	out := psql(t, dsn, `
		SELECT log_time, user_name, database_name, process_id, connection_from,
		       session_id, session_line_num, command_tag, session_start_time,
		       virtual_transaction_id, transaction_id, error_severity,
		       sql_state_code, message, detail, hint, internal_query,
		       internal_query_pos, context, query, query_pos, location,
		       application_name, backend_type, leader_pid, query_id
		FROM pglogwatch_oracle
		ORDER BY session_line_num, log_time`)

	want := parseOracleRows(t, out)

	f, err := os.Open(abs)
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // read-only

	p := New(f, Config{Format: FormatCSV})
	var buf []byte
	got := 0
	for p.Next() {
		require.Less(t, got, len(want), "parser produced more records than COPY did")
		row := want[got]
		r := p.Record()

		unquoted := func(v []byte) string {
			if r.Flags&FlagNeedsUnquote == 0 {
				return string(v)
			}
			buf = AppendUnquoted(buf[:0], v, FormatCSV)
			return string(buf)
		}

		assert.Equal(t, row[1], string(r.User), "record %d: user_name", got)
		assert.Equal(t, row[2], string(r.Database), "record %d: database_name", got)
		assert.Equal(t, row[3], itoa(int64(r.ProcessID)), "record %d: process_id", got)
		assert.Equal(t, row[4], string(r.ConnectionFrom), "record %d: connection_from", got)
		assert.Equal(t, row[5], string(r.SessionID), "record %d: session_id", got)
		assert.Equal(t, row[6], itoa(r.SessionLineNum), "record %d: session_line_num", got)
		assert.Equal(t, row[7], string(r.CommandTag), "record %d: command_tag", got)
		assert.Equal(t, row[9], string(r.VirtualXID), "record %d: virtual_transaction_id", got)
		assert.Equal(t, row[11], string(r.RawSeverity), "record %d: error_severity", got)
		assert.Equal(t, row[12], sqlStateString(r), "record %d: sql_state_code", got)
		assert.Equal(t, row[13], unquoted(r.Message), "record %d: message", got)
		assert.Equal(t, row[14], unquoted(r.Detail), "record %d: detail", got)
		assert.Equal(t, row[15], unquoted(r.Hint), "record %d: hint", got)
		assert.Equal(t, row[19], unquoted(r.Query), "record %d: query", got)
		assert.Equal(t, row[21], string(r.Location), "record %d: location", got)
		assert.Equal(t, row[22], string(r.ApplicationName), "record %d: application_name", got)
		assert.Equal(t, row[23], string(r.BackendType), "record %d: backend_type", got)

		// Timestamps are compared as instants, not as text: psql renders
		// them in the session's TimeZone, which need not be the zone the
		// log was written in.
		assertSameInstant(t, row[0], r.Time, "record %d: log_time", got)
		assertSameInstant(t, row[8], r.SessionStart, "record %d: session_start_time", got)

		got++
	}
	require.NoError(t, p.Err())
	assert.Equal(t, len(want), got, "parser and COPY must agree on the record count")
}

func psql(t *testing.T, dsn, sql string) string {
	t.Helper()
	cmd := exec.Command("psql", dsn, "-X", "-q", "-A", "-t",
		"-F", oracleSeparator, "-v", "ON_ERROR_STOP=1", "-c", sql)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "psql failed: %s", out)
	return string(out)
}

func parseOracleRows(t *testing.T, out string) [][]string {
	t.Helper()
	var rows [][]string
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, oracleSeparator))
	}
	require.NotEmpty(t, rows, "COPY produced no rows")
	return rows
}

func assertSameInstant(t *testing.T, want string, got time.Time, msg string, args ...any) {
	t.Helper()
	label := fmt.Sprintf(msg, args...)
	if want == "" {
		assert.True(t, got.IsZero(), label)
		return
	}
	// psql's -A -t output for timestamptz is "2026-08-30 10:11:12.123+02".
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05.999999-07:00",
	} {
		if parsed, err := time.Parse(layout, want); err == nil {
			assert.True(t, parsed.Equal(got),
				"%s: server says %s, parser says %s", label, parsed, got)
			return
		}
	}
	t.Fatalf("could not parse the server's timestamp %q", want)
}

func sqlStateString(r *Record) string {
	if r.SQLState == [5]byte{} {
		return ""
	}
	return string(r.SQLState[:])
}

// itoa renders an integer field the way psql does, with an absent value as the
// empty string rather than as "0".
func itoa(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}
