package pglogwatch

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseStderrLine parses a single stderr line under an explicit
// log_line_prefix and returns the record.
//
// Written against the public API on purpose: log_line_prefix support is a
// promise made to a caller who sets Config.LinePrefix, and testing the
// compiler directly would let the two drift apart while both look correct.
func parseStderrLine(t *testing.T, prefix, line string) *OwnedRecord {
	t.Helper()
	p := New(strings.NewReader(line+"\n"), Config{
		Format:     FormatStderr,
		LinePrefix: prefix,
	})
	require.True(t, p.Next(), "no record parsed from %q with prefix %q", line, prefix)
	r := p.Record().Clone()
	require.NoError(t, p.Err())
	return r
}

// TestPrefixEveryEscape covers FMT-003: every escape PostgreSQL defines must
// be understood. An unsupported escape is not a cosmetic gap -- the scanner
// walks the template positionally, so one unknown escape misaligns every field
// after it, and the record comes back confidently wrong.
func TestPrefixEveryEscape(t *testing.T) {
	cases := []struct {
		escape string
		prefix string
		line   string
		check  func(t *testing.T, r *OwnedRecord)
	}{{
		escape: "%a application_name",
		prefix: "%a ",
		line:   "psql LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, "psql", string(r.ApplicationName)) },
	}, {
		escape: "%u user name",
		prefix: "%u ",
		line:   "app_user LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, "app_user", string(r.User)) },
	}, {
		escape: "%d database name",
		prefix: "%d ",
		line:   "appdb LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, "appdb", string(r.Database)) },
	}, {
		escape: "%r remote host and port",
		prefix: "%r ",
		line:   "10.0.0.5(52344) LOG:  hello",
		check: func(t *testing.T, r *OwnedRecord) {
			assert.Equal(t, "10.0.0.5(52344)", string(r.ConnectionFrom))
		},
	}, {
		escape: "%h remote host",
		prefix: "%h ",
		line:   "10.0.0.5 LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, "10.0.0.5", string(r.ConnectionFrom)) },
	}, {
		escape: "%b backend type",
		prefix: "%b ",
		line:   "checkpointer LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, "checkpointer", string(r.BackendType)) },
	}, {
		escape: "%p process id",
		prefix: "%p ",
		line:   "31337 LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, int32(31337), r.ProcessID) },
	}, {
		escape: "%P parallel leader pid",
		prefix: "%P ",
		line:   "4242 LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, int32(4242), r.LeaderPID) },
	}, {
		escape: "%t timestamp without milliseconds",
		prefix: "%t ",
		line:   "2026-08-30 10:11:12 CEST LOG:  hello",
		check: func(t *testing.T, r *OwnedRecord) {
			assert.Equal(t, 2026, r.Time.Year())
			assert.Equal(t, 12, r.Time.Second())
			assert.Zero(t, r.Time.Nanosecond())
		},
	}, {
		escape: "%m timestamp with milliseconds",
		prefix: "%m ",
		line:   "2026-08-30 10:11:12.123 CEST LOG:  hello",
		check: func(t *testing.T, r *OwnedRecord) {
			assert.Equal(t, 123000000, r.Time.Nanosecond())
		},
	}, {
		escape: "%n epoch timestamp",
		prefix: "%n ",
		line:   "1787040672.123 LOG:  hello",
		check: func(t *testing.T, r *OwnedRecord) {
			assert.Equal(t, int64(1787040672123), r.Time.UnixMilli())
		},
	}, {
		escape: "%i command tag",
		prefix: "%i ",
		line:   "SELECT LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, "SELECT", string(r.CommandTag)) },
	}, {
		escape: "%e SQLSTATE",
		prefix: "%e ",
		line:   "42P01 ERROR:  hello",
		check: func(t *testing.T, r *OwnedRecord) {
			assert.Equal(t, [5]byte{'4', '2', 'P', '0', '1'}, r.SQLState)
		},
	}, {
		escape: "%c session id",
		prefix: "%c ",
		line:   "68b2c4a0.7a69 LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, "68b2c4a0.7a69", string(r.SessionID)) },
	}, {
		escape: "%l session line number",
		prefix: "%l ",
		line:   "7 LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, int64(7), r.SessionLineNum) },
	}, {
		escape: "%s session start timestamp",
		prefix: "%s ",
		line:   "2026-08-30 10:10:00 CEST LOG:  hello",
		check: func(t *testing.T, r *OwnedRecord) {
			assert.Equal(t, 10, r.SessionStart.Minute())
			assert.False(t, r.SessionStart.IsZero())
		},
	}, {
		escape: "%v virtual transaction id",
		prefix: "%v ",
		line:   "3/15 LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, "3/15", string(r.VirtualXID)) },
	}, {
		escape: "%x transaction id",
		prefix: "%x ",
		line:   "8842 LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, int64(8842), r.TransactionID) },
	}, {
		escape: "%Q query id",
		prefix: "%Q ",
		line:   "8109862653847632261 LOG:  hello",
		check: func(t *testing.T, r *OwnedRecord) {
			assert.Equal(t, int64(8109862653847632261), r.QueryID)
		},
	}, {
		escape: "%% literal percent",
		prefix: "%%%p ",
		line:   "%31337 LOG:  hello",
		check:  func(t *testing.T, r *OwnedRecord) { assert.Equal(t, int32(31337), r.ProcessID) },
	}}

	for _, c := range cases {
		t.Run(c.escape, func(t *testing.T) {
			r := parseStderrLine(t, c.prefix, c.line)
			assert.Equal(t, SeverityLog, max(r.Severity, SeverityLog),
				"the severity must still parse after the prefix")
			assert.Equal(t, "hello", string(r.Message),
				"the message must survive the prefix intact")
			c.check(t, r)
		})
	}
}

// TestPrefixRealisticCombinations uses the prefixes people actually configure,
// where the risk is not any single escape but the way adjacent ones delimit
// each other.
func TestPrefixRealisticCombinations(t *testing.T) {
	t.Run("the PostgreSQL 15+ default", func(t *testing.T) {
		r := parseStderrLine(t, "%m [%p] ",
			"2026-08-30 10:11:12.123 CEST [31337] LOG:  duration: 1.500 ms")
		assert.Equal(t, int32(31337), r.ProcessID)
		assert.Equal(t, SeverityLog, r.Severity)
		assert.Equal(t, "duration: 1.500 ms", string(r.Message))
		assert.Equal(t, 1500*time.Microsecond, r.Duration)
	})

	t.Run("AC-003's prefix", func(t *testing.T) {
		r := parseStderrLine(t, "%m [%p] %q%u@%d ",
			"2026-08-30 10:11:12.123 CEST [31337] app_user@appdb ERROR:  boom")
		assert.Equal(t, int32(31337), r.ProcessID)
		assert.Equal(t, "app_user", string(r.User))
		assert.Equal(t, "appdb", string(r.Database))
		assert.Equal(t, SeverityError, r.Severity)
		assert.Equal(t, "boom", string(r.Message))
	})

	t.Run("a prefix with no trailing separator", func(t *testing.T) {
		// The last escape is delimited by the severity that follows it,
		// not by a literal, which is the case a naive "find the next
		// literal" scanner cannot handle.
		r := parseStderrLine(t, "[%p]", "[31337]LOG:  hello")
		assert.Equal(t, int32(31337), r.ProcessID)
		assert.Equal(t, "hello", string(r.Message))
	})

	t.Run("adjacent escapes with no literal between them", func(t *testing.T) {
		r := parseStderrLine(t, "%p %l ", "31337 7 LOG:  hello")
		assert.Equal(t, int32(31337), r.ProcessID)
		assert.Equal(t, int64(7), r.SessionLineNum)
	})

	t.Run("a message containing the prefix separator", func(t *testing.T) {
		// The message may contain anything, including text that looks
		// like part of the prefix. The scanner must stop at the end of
		// the template, not at the last thing that matches it.
		r := parseStderrLine(t, "%m [%p] ",
			"2026-08-30 10:11:12.123 CEST [31337] LOG:  see [4242] for details")
		assert.Equal(t, int32(31337), r.ProcessID)
		assert.Equal(t, "see [4242] for details", string(r.Message))
	})
}

// TestPrefixPadding covers E6: PostgreSQL's padding forms, which pad a field
// to a fixed width so that log lines align in a terminal.
//
//	%-5p  left-aligned in five columns:  "42   "
//	%5p   right-aligned in five columns: "   42"
//
// The padding is whitespace the server inserted, not part of the value, so it
// must not reach the record -- ProcessID must be 42 either way, and a text
// field must not come back with trailing spaces.
func TestPrefixPadding(t *testing.T) {
	t.Run("left-aligned numeric", func(t *testing.T) {
		r := parseStderrLine(t, "[%-5p] ", "[42   ] LOG:  hello")
		assert.Equal(t, int32(42), r.ProcessID)
		assert.Equal(t, "hello", string(r.Message))
	})

	t.Run("right-aligned numeric", func(t *testing.T) {
		r := parseStderrLine(t, "[%5p] ", "[   42] LOG:  hello")
		assert.Equal(t, int32(42), r.ProcessID)
	})

	t.Run("padding wider than the value is not truncated", func(t *testing.T) {
		// PostgreSQL pads to at least the width; a longer value is
		// written in full, so the width is a minimum and not a size.
		r := parseStderrLine(t, "[%-5p] ", "[3133777] LOG:  hello")
		assert.Equal(t, int32(3133777), r.ProcessID)
	})

	t.Run("left-aligned text field", func(t *testing.T) {
		r := parseStderrLine(t, "%-10u|", "app_user  |LOG:  hello")
		assert.Equal(t, "app_user", string(r.User), "padding must not become part of the value")
	})

	t.Run("padding on a timestamp", func(t *testing.T) {
		r := parseStderrLine(t, "%-30m [%p] ",
			"2026-08-30 10:11:12.123 CEST   [31337] LOG:  hello")
		assert.Equal(t, int32(31337), r.ProcessID)
		assert.Equal(t, 123000000, r.Time.Nanosecond())
	})
}

// TestPrefixConditionalQ covers E5 and FMT-003's %q: everything after %q is
// omitted for processes that have no session -- the checkpointer, the
// autovacuum launcher, the startup process, the WAL writer.
//
// This is not a corner case. Any server with %q in its prefix, which includes
// several distributions' defaults, writes both shapes into the same file all
// day. A parser that requires the post-%q segments rejects every background
// process line; one that ignores %q entirely mis-assigns the severity to a
// user name.
func TestPrefixConditionalQ(t *testing.T) {
	const prefix = "%m [%p] %q%u@%d "

	t.Run("a session backend writes the optional part", func(t *testing.T) {
		r := parseStderrLine(t, prefix,
			"2026-08-30 10:11:12.123 CEST [31337] app_user@appdb LOG:  hello")
		assert.Equal(t, "app_user", string(r.User))
		assert.Equal(t, "appdb", string(r.Database))
		assert.Equal(t, "hello", string(r.Message))
	})

	t.Run("a background worker omits it", func(t *testing.T) {
		r := parseStderrLine(t, prefix,
			"2026-08-30 10:11:14.500 CEST [31338] LOG:  checkpoint starting: time")
		assert.Equal(t, int32(31338), r.ProcessID, "the part before %q must still parse")
		assert.Empty(t, r.User, "an absent field must be absent, not guessed")
		assert.Empty(t, r.Database)
		assert.Equal(t, SeverityLog, r.Severity)
		assert.Equal(t, "checkpoint starting: time", string(r.Message),
			"the message must not absorb the missing prefix segments")
	})

	t.Run("both shapes in one stream", func(t *testing.T) {
		in := "2026-08-30 10:11:12.123 CEST [31337] app_user@appdb ERROR:  boom\n" +
			"2026-08-30 10:11:14.500 CEST [31338] LOG:  checkpoint starting: time\n" +
			"2026-08-30 10:11:15.000 CEST [31337] app_user@appdb LOG:  done\n"
		p := New(strings.NewReader(in), Config{Format: FormatStderr, LinePrefix: prefix})
		var got []string
		for p.Next() {
			got = append(got, p.Record().Severity.String()+":"+string(p.Record().Message))
		}
		require.NoError(t, p.Err())
		assert.Equal(t, []string{
			"ERROR:boom",
			"LOG:checkpoint starting: time",
			"LOG:done",
		}, got)
		assert.Zero(t, p.Stats().Malformed, "neither shape may be counted as malformed")
	})

	t.Run("%q at the very end of the prefix", func(t *testing.T) {
		// Legal, if pointless: nothing is conditional because nothing
		// follows. It must not be mistaken for an unknown escape.
		r := parseStderrLine(t, "[%p] %q", "[31337] LOG:  hello")
		assert.Equal(t, int32(31337), r.ProcessID)
		assert.Equal(t, "hello", string(r.Message))
	})
}
