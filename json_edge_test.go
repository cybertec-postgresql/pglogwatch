package pglogwatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonlog edge cases (E8, E9, FMT-002).

func TestJSONMissingKeysAreZeroValued(t *testing.T) {
	// E8 and FMT-002: absent keys yield zero-valued fields, not errors.
	//
	// This is the common case, not a corner one. PostgreSQL omits every key
	// that does not apply, so a checkpointer line carries five keys where a
	// failed query carries twenty. A parser that required its key set would
	// reject most of a real log.
	recs, p := parseFixture(t, "json/basic.json", Config{Format: FormatJSON})
	require.Len(t, recs, 3)
	assert.Zero(t, p.Stats().Malformed)

	ckpt := recs[2]
	assert.Equal(t, SeverityLog, ckpt.Severity)
	assert.Equal(t, "checkpoint starting: time", string(ckpt.Message))
	assert.Equal(t, int32(31338), ckpt.ProcessID)
	assert.Equal(t, "checkpointer", string(ckpt.BackendType))

	assert.Empty(t, ckpt.User)
	assert.Empty(t, ckpt.Database)
	assert.Empty(t, ckpt.SessionID)
	assert.Empty(t, ckpt.Statement)
	assert.Zero(t, ckpt.SessionLineNum)
	assert.Zero(t, ckpt.QueryID)
	assert.Equal(t, [5]byte{}, ckpt.SQLState)
	assert.True(t, ckpt.SessionStart.IsZero())
}

func TestJSONEmptyObject(t *testing.T) {
	// The limit of E8: an object with no keys at all is still a line of the
	// log and must not be an error.
	p := New(strings.NewReader("{}\n"), Config{Format: FormatJSON})
	require.True(t, p.Next())
	assert.Equal(t, SeverityUnknown, p.Record().Severity)
	assert.False(t, p.Next())
	require.NoError(t, p.Err())
}
