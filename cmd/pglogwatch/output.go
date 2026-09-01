package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/cybertec-postgresql/pglogwatch"
)

// Output helpers shared by every subcommand.
//
// Two shapes, decided by --output: a human-readable table, and NDJSON. IFC-011
// puts NDJSON on stdout alone, so nothing here ever writes a note, a heading or
// a progress line to the JSON stream.
//
// The JSON is written by hand rather than with encoding/json. The parser's own
// ban (CON-004) does not extend to this module, but the record writer has to
// emit borrowed []byte fields without allocating a string per field per record
// -- which is the entire point of the parse subcommand -- and once that writer
// exists, using it for the reports as well keeps one escaping implementation
// instead of two.

// jsonWriter builds NDJSON objects.
type jsonWriter struct {
	w     *bufio.Writer
	first bool
}

func newJSONWriter(w io.Writer) *jsonWriter {
	return &jsonWriter{w: bufio.NewWriterSize(w, 64<<10)}
}

func (j *jsonWriter) begin() { j.w.WriteByte('{'); j.first = true } //nolint:errcheck // bufio defers errors

func (j *jsonWriter) end() {
	j.w.WriteString("}\n") //nolint:errcheck // bufio defers errors
}

func (j *jsonWriter) key(name string) {
	if !j.first {
		j.w.WriteByte(',') //nolint:errcheck // bufio defers errors
	}
	j.first = false
	j.w.WriteByte('"')    //nolint:errcheck // bufio defers errors
	j.w.WriteString(name) //nolint:errcheck // bufio defers errors
	j.w.WriteString(`":`) //nolint:errcheck // bufio defers errors
}

// str writes a string field, omitting it when empty.
//
// Omitting rather than writing "" keeps a record's absent fields out of the
// output entirely, which matters for a format meant to be piped into something
// else: a consumer can test for presence rather than for emptiness.
func (j *jsonWriter) str(name string, v []byte) {
	if len(v) == 0 {
		return
	}
	j.key(name)
	j.quote(v)
}

func (j *jsonWriter) strS(name, v string) {
	if v == "" {
		return
	}
	j.key(name)
	j.quote([]byte(v))
}

func (j *jsonWriter) num(name string, v int64) {
	if v == 0 {
		return
	}
	j.key(name)
	j.w.WriteString(strconv.FormatInt(v, 10)) //nolint:errcheck // bufio defers errors
}

// numAlways writes a numeric field even when zero, for counts where zero is a
// meaningful answer rather than an absent one.
func (j *jsonWriter) numAlways(name string, v int64) {
	j.key(name)
	j.w.WriteString(strconv.FormatInt(v, 10)) //nolint:errcheck // bufio defers errors
}

func (j *jsonWriter) time(name string, t time.Time) {
	if t.IsZero() {
		return
	}
	j.key(name)
	j.w.WriteByte('"')                          //nolint:errcheck // bufio defers errors
	j.w.WriteString(t.Format(time.RFC3339Nano)) //nolint:errcheck // bufio defers errors
	j.w.WriteByte('"')                          //nolint:errcheck // bufio defers errors
}

func (j *jsonWriter) dur(name string, d time.Duration) {
	if d == 0 {
		return
	}
	j.key(name)
	j.w.WriteString(strconv.FormatFloat(d.Seconds()*1000, 'f', 3, 64)) //nolint:errcheck // bufio defers errors
}

const hexdigits = "0123456789abcdef"

// quote writes a JSON string, escaping what RFC 8259 requires and nothing more.
//
// Invalid UTF-8 is passed through as-is rather than replaced, for the same
// reason the parser does (COR-005): this tool reports what the log contains.
func (j *jsonWriter) quote(v []byte) {
	j.w.WriteByte('"') //nolint:errcheck // bufio defers errors
	for _, c := range v {
		switch c {
		case '"':
			j.w.WriteString(`\"`) //nolint:errcheck // bufio defers errors
		case '\\':
			j.w.WriteString(`\\`) //nolint:errcheck // bufio defers errors
		case '\n':
			j.w.WriteString(`\n`) //nolint:errcheck // bufio defers errors
		case '\r':
			j.w.WriteString(`\r`) //nolint:errcheck // bufio defers errors
		case '\t':
			j.w.WriteString(`\t`) //nolint:errcheck // bufio defers errors
		default:
			if c < 0x20 {
				j.w.WriteString(`\u00`)          //nolint:errcheck // bufio defers errors
				j.w.WriteByte(hexdigits[c>>4])   //nolint:errcheck // bufio defers errors
				j.w.WriteByte(hexdigits[c&0x0f]) //nolint:errcheck // bufio defers errors
				continue
			}
			j.w.WriteByte(c) //nolint:errcheck // bufio defers errors
		}
	}
	j.w.WriteByte('"') //nolint:errcheck // bufio defers errors
}

func (j *jsonWriter) flush() error { return j.w.Flush() }

// table prints aligned columns for --output text.
type table struct {
	w       io.Writer
	headers []string
	rows    [][]string
}

func newTable(w io.Writer, headers ...string) *table {
	return &table{w: w, headers: headers}
}

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

func (t *table) flush() {
	if len(t.rows) == 0 {
		fmt.Fprintln(t.w, "(nothing to report)") //nolint:errcheck // report output
		return
	}
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = len(h)
	}
	for _, row := range t.rows {
		for i, c := range row {
			if i < len(widths) && len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	t.line(t.headers, widths)
	sep := make([]string, len(t.headers))
	for i := range sep {
		sep[i] = strings.Repeat("-", widths[i])
	}
	t.line(sep, widths)
	for _, row := range t.rows {
		t.line(row, widths)
	}
}

func (t *table) line(cells []string, widths []int) {
	var sb strings.Builder
	for i, c := range cells {
		if i > 0 {
			sb.WriteString("  ")
		}
		// The last column is not padded: trailing whitespace on every
		// line makes the output awkward to diff and to paste.
		if i == len(cells)-1 {
			sb.WriteString(c)
			continue
		}
		sb.WriteString(c)
		if pad := widths[i] - len(c); pad > 0 {
			sb.WriteString(strings.Repeat(" ", pad))
		}
	}
	fmt.Fprintln(t.w, sb.String()) //nolint:errcheck // report output
}

// writeRecordJSON emits one Record as an NDJSON object.
//
// Fields carry the names the specification's §4.2 gives them, lower-cased with
// underscores, so the output reads like the csvlog columns a reader already
// knows rather than like Go field names.
func writeRecordJSON(j *jsonWriter, r *pglogwatch.Record, format pglogwatch.Format, buf *[]byte) {
	j.begin()
	j.time("time", r.Time)
	j.time("session_start", r.SessionStart)
	j.strS("severity", r.Severity.String())
	j.str("raw_severity", r.RawSeverity)
	j.str("user", r.User)
	j.str("database", r.Database)
	j.str("connection_from", r.ConnectionFrom)
	j.str("application_name", r.ApplicationName)
	j.str("backend_type", r.BackendType)
	j.str("command_tag", r.CommandTag)
	j.num("process_id", int64(r.ProcessID))
	j.num("leader_pid", int64(r.LeaderPID))
	j.str("session_id", r.SessionID)
	j.num("session_line_num", r.SessionLineNum)
	j.str("virtual_xid", r.VirtualXID)
	j.num("transaction_id", r.TransactionID)
	j.num("query_id", r.QueryID)
	if r.SQLState != [5]byte{} {
		j.str("sql_state", r.SQLState[:])
	}
	j.str("message", unquoted(r.Message, r, format, buf))
	j.str("detail", unquoted(r.Detail, r, format, buf))
	j.str("hint", unquoted(r.Hint, r, format, buf))
	j.str("query", unquoted(r.Query, r, format, buf))
	j.str("internal_query", unquoted(r.InternalQuery, r, format, buf))
	j.str("context", unquoted(r.Context, r, format, buf))
	j.str("statement", unquoted(r.Statement, r, format, buf))
	j.num("query_pos", int64(r.QueryPos))
	j.num("internal_query_pos", int64(r.InternalQueryPos))
	j.str("location", r.Location)
	j.dur("duration_ms", r.Duration)
	j.num("offset", r.Offset)
	j.end()
}

// unquoted resolves a field's escaping only when the record says it has some,
// reusing the caller's buffer.
//
// This is what PERF-009's deferred design buys at the call site: a record with
// no escapes costs nothing, and one with escapes costs a copy into a buffer
// that is already the right size after the first few records.
func unquoted(v []byte, r *pglogwatch.Record, format pglogwatch.Format, buf *[]byte) []byte {
	if len(v) == 0 || r.Flags&pglogwatch.FlagNeedsUnquote == 0 {
		return v
	}
	*buf = pglogwatch.AppendUnquoted((*buf)[:0], v, format)
	return *buf
}
