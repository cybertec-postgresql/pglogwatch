package pglogwatch

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// update rewrites the golden files instead of comparing against them:
//
//	go test -run TestGolden -update ./...
//
// Review the resulting diff. A golden test only has value while somebody looks
// at what changed, and -update makes the change easy to produce and easy to
// read.
var update = flag.Bool("update", false, "rewrite golden files")

// TestGolden parses each fixture to NDJSON and diffs it against a committed
// expectation.
//
// Goldens catch the regressions unit tests miss -- a field quietly moving by
// one column, a flag that stops being set -- because they compare the whole
// record rather than the fields a test author thought to check. TST-010 is
// explicit that they must not be the ONLY coverage, which is why every
// primitive also has direct unit tests.
func TestGolden(t *testing.T) {
	cases := []struct {
		file   string
		format Format
		prefix string
		lang   string
	}{
		{file: "csv/pg14-basic.csv", format: FormatCSV},
		{file: "csv/pg13-basic.csv", format: FormatCSV},
		{file: "csv/pg12-basic.csv", format: FormatCSV},
		{file: "csv/quotes-newlines-commas.csv", format: FormatCSV},
		{file: "csv/crlf.csv", format: FormatCSV},
		{file: "csv/truncated-tail.csv", format: FormatCSV},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			p := New(bytes.NewReader(fixture(t, c.file)), Config{
				Format:       c.format,
				LinePrefix:   c.prefix,
				MessagesLang: c.lang,
			})
			var got bytes.Buffer
			for p.Next() {
				appendNDJSON(&got, p.Record())
			}
			require.NoError(t, p.Err())

			goldenPath := filepath.Join("testdata", "golden", strings.ReplaceAll(c.file, "/", "_")+".ndjson")
			if *update {
				require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
				require.NoError(t, os.WriteFile(goldenPath, got.Bytes(), 0o600))
				t.Logf("updated %s", goldenPath)
				return
			}
			want, err := os.ReadFile(goldenPath)
			require.NoError(t, err, "missing golden file; run: go test -run TestGolden -update")
			assert.Equal(t, string(want), got.String())
		})
	}
}

// appendNDJSON writes one record as a JSON object.
//
// Hand-written rather than encoding/json, because CON-004 keeps encoding/json
// out of the package and a test that imported it would be the one place the
// ban did not apply. It is also the format the CLI's parse subcommand emits,
// so writing it here settles the shape early.
func appendNDJSON(w *bytes.Buffer, r *Record) {
	w.WriteByte('{')
	f := fieldWriter{w: w}
	f.time("time", r.Time)
	f.time("session_start", r.SessionStart)
	f.str("severity", []byte(r.Severity.String()))
	f.str("raw_severity", r.RawSeverity)
	f.str("user", r.User)
	f.str("database", r.Database)
	f.str("connection_from", r.ConnectionFrom)
	f.str("application_name", r.ApplicationName)
	f.str("backend_type", r.BackendType)
	f.str("command_tag", r.CommandTag)
	f.int("process_id", int64(r.ProcessID))
	f.int("leader_pid", int64(r.LeaderPID))
	f.str("session_id", r.SessionID)
	f.int("session_line_num", r.SessionLineNum)
	f.str("virtual_xid", r.VirtualXID)
	f.int("transaction_id", r.TransactionID)
	f.int("query_id", r.QueryID)
	if r.SQLState != [5]byte{} {
		f.str("sql_state", r.SQLState[:])
	}
	f.str("message", r.Message)
	f.str("detail", r.Detail)
	f.str("hint", r.Hint)
	f.str("query", r.Query)
	f.str("internal_query", r.InternalQuery)
	f.str("context", r.Context)
	f.str("statement", r.Statement)
	f.int("query_pos", int64(r.QueryPos))
	f.int("internal_query_pos", int64(r.InternalQueryPos))
	f.str("location", r.Location)
	f.int("duration_ns", int64(r.Duration))
	f.int("flags", int64(r.Flags))
	f.int("offset", r.Offset)
	w.WriteString("}\n")
}

type fieldWriter struct {
	w    *bytes.Buffer
	seen bool
}

func (f *fieldWriter) key(name string) {
	if f.seen {
		f.w.WriteByte(',')
	}
	f.seen = true
	f.w.WriteByte('"')
	f.w.WriteString(name)
	f.w.WriteString(`":`)
}

// str omits absent fields entirely, so a golden diff shows a field appearing
// or disappearing rather than changing from "" to "".
func (f *fieldWriter) str(name string, v []byte) {
	if len(v) == 0 {
		return
	}
	f.key(name)
	f.w.WriteByte('"')
	for _, c := range v {
		switch c {
		case '"':
			f.w.WriteString(`\"`)
		case '\\':
			f.w.WriteString(`\\`)
		case '\n':
			f.w.WriteString(`\n`)
		case '\r':
			f.w.WriteString(`\r`)
		case '\t':
			f.w.WriteString(`\t`)
		default:
			if c < 0x20 {
				f.w.WriteString(`\u00`)
				const hexdigits = "0123456789abcdef"
				f.w.WriteByte(hexdigits[c>>4])
				f.w.WriteByte(hexdigits[c&0xf])
			} else {
				f.w.WriteByte(c)
			}
		}
	}
	f.w.WriteByte('"')
}

func (f *fieldWriter) int(name string, v int64) {
	if v == 0 {
		return
	}
	f.key(name)
	f.w.WriteString(strconv.FormatInt(v, 10))
}

func (f *fieldWriter) time(name string, v interface{ IsZero() bool }) {
	if v.IsZero() {
		return
	}
	type stringer interface{ Format(string) string }
	f.key(name)
	f.w.WriteByte('"')
	f.w.WriteString(v.(stringer).Format("2006-01-02T15:04:05.000000000Z07:00"))
	f.w.WriteByte('"')
}
