package gen

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Rendering one event stream into the three destinations (TST-002).
//
// The three writers below are the only place the corpus knows about formats.
// They are deliberately dumb: they render exactly what PostgreSQL would write,
// including the parts that make parsing awkward -- embedded newlines in
// autovacuum messages, doubled quotes in csvlog, backslash escapes in jsonlog
// -- because a corpus that avoided those would benchmark a parser on input it
// will never see.

// Layout is a csvlog column layout, which differs by PostgreSQL major version
// (FMT-001, DAT-003).
type Layout int

const (
	// LayoutPG12 has 23 columns.
	LayoutPG12 Layout = 23
	// LayoutPG13 has 24: it adds backend_type.
	LayoutPG13 Layout = 24
	// LayoutPG14 has 26: it adds leader_pid and query_id, and is current
	// through PostgreSQL 18.
	LayoutPG14 Layout = 26
)

// AllLayouts is every layout a corpus is generated for.
var AllLayouts = []Layout{LayoutPG12, LayoutPG13, LayoutPG14}

func (l Layout) String() string {
	switch l {
	case LayoutPG12:
		return "pg12"
	case LayoutPG13:
		return "pg13"
	default:
		return "pg14"
	}
}

// StderrPrefix is the log_line_prefix the stderr corpus is written with.
//
// It is PostgreSQL 15's default plus the user and database, which is what most
// deployments actually set, and it exercises %q -- the conditional segment that
// background processes omit (E5).
const StderrPrefix = "%m [%p] %q%u@%d "

const stamp = "2006-01-02 15:04:05.000 MST"

// WriteStderr renders events as stderr output.
func WriteStderr(w io.Writer, events []Event) (Written, error) {
	cw := &countingWriter{w: w}
	var sb strings.Builder

	for i := range events {
		e := &events[i]
		sb.Reset()
		sb.WriteString(e.Time.Format(stamp))
		sb.WriteString(" [")
		sb.WriteString(strconv.FormatInt(int64(e.PID), 10))
		sb.WriteString("] ")
		// %q: a background process writes nothing here, which is the
		// shape a parser has to handle without being told.
		if e.User != "" || e.Database != "" {
			sb.WriteString(e.User)
			sb.WriteByte('@')
			sb.WriteString(e.Database)
			sb.WriteByte(' ')
		}
		sb.WriteString(e.Severity)
		sb.WriteString(":  ")
		sb.WriteString(e.Message)
		sb.WriteByte('\n')

		// Continuations carry the full prefix again, exactly as
		// PostgreSQL writes them.
		for _, c := range []struct{ label, text string }{
			{"DETAIL", e.Detail},
			{"HINT", e.Hint},
			{"STATEMENT", e.Statement},
		} {
			if c.text == "" {
				continue
			}
			sb.WriteString(e.Time.Format(stamp))
			sb.WriteString(" [")
			sb.WriteString(strconv.FormatInt(int64(e.PID), 10))
			sb.WriteString("] ")
			if e.User != "" || e.Database != "" {
				sb.WriteString(e.User)
				sb.WriteByte('@')
				sb.WriteString(e.Database)
				sb.WriteByte(' ')
			}
			sb.WriteString(c.label)
			sb.WriteString(":  ")
			sb.WriteString(c.text)
			sb.WriteByte('\n')
		}
		if _, err := io.WriteString(cw, sb.String()); err != nil {
			return Written{}, err
		}
	}
	return Written{Bytes: cw.n, Records: len(events)}, nil
}

// WriteCSV renders events as csvlog output in the given column layout.
func WriteCSV(w io.Writer, events []Event, layout Layout) (Written, error) {
	cw := &countingWriter{w: w}
	var sb strings.Builder

	for i := range events {
		e := &events[i]
		sb.Reset()

		sb.WriteString(e.Time.Format(stamp)) // log_time
		sb.WriteByte(',')
		escapeCSV(&sb, e.User)
		sb.WriteByte(',')
		escapeCSV(&sb, e.Database)
		sb.WriteByte(',')
		sb.WriteString(strconv.FormatInt(int64(e.PID), 10))
		sb.WriteByte(',')
		if e.Host != "" {
			escapeCSV(&sb, e.Host+":"+strconv.Itoa(e.Port))
		}
		sb.WriteByte(',')
		sb.WriteString(e.SessionID)
		sb.WriteByte(',')
		sb.WriteString(strconv.FormatInt(e.LineNum, 10))
		sb.WriteByte(',')
		escapeCSV(&sb, e.CommandTag)
		sb.WriteByte(',')
		sb.WriteString(e.Time.Add(-time.Hour).Format(stamp)) // session_start_time
		sb.WriteByte(',')
		sb.WriteString(e.VXID)
		sb.WriteByte(',')
		sb.WriteString(strconv.FormatInt(e.TXID, 10))
		sb.WriteByte(',')
		sb.WriteString(e.Severity)
		sb.WriteByte(',')
		sb.WriteString(e.SQLState)
		sb.WriteByte(',')
		escapeCSV(&sb, e.Message)
		sb.WriteByte(',')
		escapeCSV(&sb, e.Detail)
		sb.WriteByte(',')
		escapeCSV(&sb, e.Hint)
		sb.WriteString(",,,") // internal_query, internal_query_pos, context
		escapeCSV(&sb, e.Statement)
		sb.WriteString(",,") // query_pos, location
		escapeCSV(&sb, e.App)

		if layout >= LayoutPG13 {
			sb.WriteByte(',')
			escapeCSV(&sb, e.Backend)
		}
		if layout >= LayoutPG14 {
			sb.WriteString(",,") // leader_pid (empty), then query_id
			sb.WriteString(strconv.FormatInt(e.QueryID, 10))
		}
		sb.WriteByte('\n')

		if _, err := io.WriteString(cw, sb.String()); err != nil {
			return Written{}, err
		}
	}
	return Written{Bytes: cw.n, Records: len(events)}, nil
}

// WriteJSON renders events as jsonlog output.
func WriteJSON(w io.Writer, events []Event) (Written, error) {
	cw := &countingWriter{w: w}
	var sb strings.Builder

	for i := range events {
		e := &events[i]
		sb.Reset()
		sb.WriteByte('{')
		jsonStr(&sb, "timestamp", e.Time.Format(stamp), true)
		jsonStr(&sb, "user", e.User, false)
		jsonStr(&sb, "dbname", e.Database, false)
		jsonNum(&sb, "pid", int64(e.PID))
		jsonStr(&sb, "remote_host", e.Host, false)
		if e.Port != 0 {
			jsonNum(&sb, "remote_port", int64(e.Port))
		}
		jsonStr(&sb, "session_id", e.SessionID, false)
		jsonNum(&sb, "line_num", e.LineNum)
		jsonStr(&sb, "ps", e.CommandTag, false)
		jsonStr(&sb, "session_start", e.Time.Add(-time.Hour).Format(stamp), false)
		jsonStr(&sb, "vxid", e.VXID, false)
		jsonNum(&sb, "txid", e.TXID)
		jsonStr(&sb, "error_severity", e.Severity, false)
		jsonStr(&sb, "state_code", e.SQLState, false)
		jsonStr(&sb, "message", e.Message, false)
		jsonStr(&sb, "detail", e.Detail, false)
		jsonStr(&sb, "hint", e.Hint, false)
		jsonStr(&sb, "statement", e.Statement, false)
		jsonStr(&sb, "application_name", e.App, false)
		jsonStr(&sb, "backend_type", e.Backend, false)
		if e.QueryID != 0 {
			jsonNum(&sb, "query_id", e.QueryID)
		}
		sb.WriteString("}\n")

		if _, err := io.WriteString(cw, sb.String()); err != nil {
			return Written{}, err
		}
	}
	return Written{Bytes: cw.n, Records: len(events)}, nil
}

func jsonStr(sb *strings.Builder, key, v string, first bool) {
	if v == "" {
		// PostgreSQL omits keys that do not apply (E8), and a corpus
		// that always wrote every key would never exercise that.
		return
	}
	if !first && sb.Len() > 1 {
		sb.WriteByte(',')
	}
	sb.WriteByte('"')
	sb.WriteString(key)
	sb.WriteString(`":"`)
	for i := range len(v) {
		switch c := v[i]; c {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(sb, `\u%04x`, c)
				continue
			}
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
}

func jsonNum(sb *strings.Builder, key string, v int64) {
	if sb.Len() > 1 {
		sb.WriteByte(',')
	}
	sb.WriteByte('"')
	sb.WriteString(key)
	sb.WriteString(`":`)
	sb.WriteString(strconv.FormatInt(v, 10))
}
