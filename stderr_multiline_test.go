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
