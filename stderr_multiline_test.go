package pglogwatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stderr continuation handling (FMT-006, AC-004).
//
// Unlike csvlog, stderr has no record delimiter: a record ends where the next
// one begins. PostgreSQL writes DETAIL, HINT, STATEMENT, QUERY and CONTEXT as
// separate physical lines carrying the same prefix, and they belong to the
// record above them. Treating each as its own record is what makes the current
// pgwatch implementation count a single constraint violation as four events,
// three of them with a severity that is not a severity.

const multilinePrefix = "%m [%p] %q%u@%d "

func TestStderrFoldsContinuationLines(t *testing.T) {
	recs, p := parseFixture(t, "stderr/multiline.log",
		Config{Format: FormatStderr, LinePrefix: multilinePrefix})

	require.Len(t, recs, 2, "one error with three continuations plus one LOG = 2 records")
	assert.Zero(t, p.Stats().Malformed)

	r := recs[0]
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, `duplicate key value violates unique constraint "orders_pkey"`, string(r.Message))
	assert.Equal(t, "Key (id)=(42) already exists.", string(r.Detail))
	assert.Equal(t, "Use ON CONFLICT to ignore the duplicate.", string(r.Hint))
	assert.Contains(t, string(r.Statement), "INSERT INTO orders (id, total)")
	assert.NotZero(t, r.Flags&FlagMultiline, "AC-004 requires FlagMultiline")
	assert.NotZero(t, r.Flags&FlagHasStatement)

	assert.Equal(t, SeverityLog, recs[1].Severity)
	assert.Equal(t, "statement: SELECT 1", string(recs[1].Message))
}

func TestStderrContinuationsAreNotSeparateRecords(t *testing.T) {
	// The severity counting failure mode, stated directly: DETAIL, HINT and
	// STATEMENT must not be counted as events of their own.
	recs, _ := parseFixture(t, "stderr/multiline.log",
		Config{Format: FormatStderr, LinePrefix: multilinePrefix})
	counts := map[Severity]int{}
	for _, r := range recs {
		counts[r.Severity]++
	}
	assert.Equal(t, 1, counts[SeverityError])
	assert.Equal(t, 1, counts[SeverityLog])
	assert.Zero(t, counts[SeverityUnknown], "no continuation may leak out as a record")
}

func TestStderrSplitContinuations(t *testing.T) {
	// Config.SplitContinuations restores the per-line behaviour for a
	// caller who wants to see the raw event stream.
	recs, _ := parseFixture(t, "stderr/multiline.log", Config{
		Format:             FormatStderr,
		LinePrefix:         multilinePrefix,
		SplitContinuations: true,
	})
	assert.Len(t, recs, 5, "ERROR, DETAIL, HINT, STATEMENT and LOG become 5 records")

	// The folded fields must then be empty: a caller asking for separate
	// records should not also get them attached to the parent.
	assert.Empty(t, recs[0].Detail)
	assert.Empty(t, recs[0].Hint)
}

func TestStderrQueryAndContextContinuations(t *testing.T) {
	in := strings.Join([]string{
		"2026-08-30 10:11:15.000 CEST [31337] app_user@appdb ERROR:  division by zero",
		"2026-08-30 10:11:15.000 CEST [31337] app_user@appdb QUERY:  SELECT 1/0",
		"2026-08-30 10:11:15.000 CEST [31337] app_user@appdb CONTEXT:  PL/pgSQL function f() line 3",
		"2026-08-30 10:11:16.000 CEST [31337] app_user@appdb LOG:  done",
	}, "\n") + "\n"

	p := New(strings.NewReader(in), Config{Format: FormatStderr, LinePrefix: multilinePrefix})
	require.True(t, p.Next())
	r := p.Record()
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, "division by zero", string(r.Message))
	assert.Equal(t, "SELECT 1/0", string(r.InternalQuery))
	assert.Equal(t, "PL/pgSQL function f() line 3", string(r.Context))

	require.True(t, p.Next())
	assert.Equal(t, "done", string(p.Record().Message))
	assert.False(t, p.Next())
	require.NoError(t, p.Err())
}

func TestStderrContinuationAtEndOfStream(t *testing.T) {
	// A continuation as the last line must still be folded into the record
	// above it and that record must still be emitted -- the parser cannot
	// wait for a following record that never arrives.
	in := "2026-08-30 10:11:15.000 CEST [31337] app_user@appdb ERROR:  boom\n" +
		"2026-08-30 10:11:15.000 CEST [31337] app_user@appdb DETAIL:  because\n"
	p := New(strings.NewReader(in), Config{Format: FormatStderr, LinePrefix: multilinePrefix})
	require.True(t, p.Next())
	assert.Equal(t, "because", string(p.Record().Detail))
	assert.False(t, p.Next())
	require.NoError(t, p.Err())
}

// TestStderrWrappedStatement covers E7: a continuation line that begins with
// whitespace rather than with a prefix.
//
// PostgreSQL writes a multi-line statement by emitting the prefix once and
// then indenting the remaining lines with a tab. Those lines match no prefix
// and carry no label, so a parser that only recognises DETAIL/HINT/... style
// continuations turns each into a malformed line -- and a long formatted query
// can be dozens of them.
func TestStderrWrappedStatement(t *testing.T) {
	recs, p := parseFixture(t, "stderr/multiline.log",
		Config{Format: FormatStderr, LinePrefix: multilinePrefix})
	require.Len(t, recs, 2)
	assert.Zero(t, p.Stats().Malformed, "indented statement lines are not malformed")

	stmt := string(recs[0].Statement)
	assert.Contains(t, stmt, "INSERT INTO orders (id, total)")
	assert.Contains(t, stmt, "VALUES (42, 19.99)")
	assert.Contains(t, stmt, "RETURNING id;")
	assert.Equal(t, 2, strings.Count(stmt, "\n"),
		"all three lines of the statement must be in one field")
}

func TestStderrWrappedMessage(t *testing.T) {
	// The same wrapping, but on the message itself rather than on a
	// labelled continuation.
	in := "2026-08-30 10:11:15.000 CEST [31337] app_user@appdb LOG:  a message that\n" +
		"\tcontinues on the next line\n" +
		"2026-08-30 10:11:16.000 CEST [31337] app_user@appdb LOG:  done\n"
	p := New(strings.NewReader(in), Config{Format: FormatStderr, LinePrefix: multilinePrefix})

	require.True(t, p.Next())
	r := p.Record()
	assert.Equal(t, "a message that\n\tcontinues on the next line", string(r.Message))
	assert.NotZero(t, r.Flags&FlagMultiline)

	require.True(t, p.Next())
	assert.Equal(t, "done", string(p.Record().Message))
	require.NoError(t, p.Err())
	assert.Zero(t, p.Stats().Malformed)
}

// TestStderrStatementMirrorsQuery pins COR-004 on the one field where stderr
// and csvlog use different names for the same thing.
//
// csvlog has a "query" column holding the statement a record is about, and the
// csvlog path fills both Query and Statement from it. stderr writes the same
// thing as STATEMENT:. If only one field were filled here, the same server
// activity would produce different records depending on which destination the
// server happened to be configured for -- and a consumer reading Query would
// silently see nothing on stderr logs.
func TestStderrStatementMirrorsQuery(t *testing.T) {
	recs, _ := parseFixture(t, "stderr/multiline.log",
		Config{Format: FormatStderr, LinePrefix: multilinePrefix})
	require.Len(t, recs, 2)
	assert.NotEmpty(t, recs[0].Statement)
	assert.Equal(t, string(recs[0].Statement), string(recs[0].Query),
		"Query and Statement must agree, as they do on the csvlog path")
}

// TestSplitContinuationsContract pins what Config.SplitContinuations actually
// produces, beyond the record count checked earlier.
//
// The option exists for callers who want the physical event stream -- a grep
// tool, a tail follower -- rather than logical records. What they get has to be
// predictable: each continuation must arrive as a record whose label is
// recoverable and whose text is in the field the label names.
func TestSplitContinuationsContract(t *testing.T) {
	recs, p := parseFixture(t, "stderr/multiline.log", Config{
		Format:             FormatStderr,
		LinePrefix:         multilinePrefix,
		SplitContinuations: true,
	})
	require.Len(t, recs, 5)
	assert.Zero(t, p.Stats().Malformed, "a split continuation is not malformed")
	assert.Equal(t, int64(5), p.Stats().Records)

	// The parent keeps its own message and nothing else.
	assert.Equal(t, SeverityError, recs[0].Severity)
	assert.Empty(t, recs[0].Detail)
	assert.Empty(t, recs[0].Hint)
	assert.Empty(t, recs[0].Statement)

	// Each continuation is a record: the label is recoverable through
	// RawSeverity, and the text lands in the field that label names.
	assert.Equal(t, "DETAIL", string(recs[1].RawSeverity))
	assert.Equal(t, "Key (id)=(42) already exists.", string(recs[1].Detail))

	assert.Equal(t, "HINT", string(recs[2].RawSeverity))
	assert.Equal(t, "Use ON CONFLICT to ignore the duplicate.", string(recs[2].Hint))

	assert.Equal(t, "STATEMENT", string(recs[3].RawSeverity))
	assert.Contains(t, string(recs[3].Statement), "INSERT INTO orders")

	// A continuation label is not a severity, and must not be reported as
	// one -- a caller counting severities would otherwise see three extra
	// events of an unknown kind for every error.
	for _, r := range recs[1:4] {
		assert.Equal(t, SeverityUnknown, r.Severity)
	}

	// Prefix-derived fields still parse on a continuation line, since the
	// server writes the full prefix on each.
	assert.Equal(t, int32(31337), recs[1].ProcessID)
	assert.Equal(t, "app_user", string(recs[1].User))
}

func TestSplitContinuationsKeepsWrappedLinesTogether(t *testing.T) {
	// Even when splitting, a wrapped line has no prefix of its own and so
	// cannot become a record: it belongs to the labelled line above it.
	recs, _ := parseFixture(t, "stderr/multiline.log", Config{
		Format:             FormatStderr,
		LinePrefix:         multilinePrefix,
		SplitContinuations: true,
	})
	require.Len(t, recs, 5)
	stmt := string(recs[3].Statement)
	assert.Contains(t, stmt, "VALUES (42, 19.99)")
	assert.Contains(t, stmt, "RETURNING id;")
}
