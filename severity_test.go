package pglogwatch

import (
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
)

func TestParseSeverityEnglish(t *testing.T) {
	cases := map[string]Severity{
		"DEBUG5":  SeverityDebug5,
		"DEBUG4":  SeverityDebug4,
		"DEBUG3":  SeverityDebug3,
		"DEBUG2":  SeverityDebug2,
		"DEBUG1":  SeverityDebug1,
		"DEBUG":   SeverityDebug1, // the locale tables' collapsed form
		"LOG":     SeverityLog,
		"INFO":    SeverityInfo,
		"NOTICE":  SeverityNotice,
		"WARNING": SeverityWarning,
		"ERROR":   SeverityError,
		"FATAL":   SeverityFatal,
		"PANIC":   SeverityPanic,

		"":       SeverityUnknown,
		"error":  SeverityUnknown, // PostgreSQL writes them uppercase
		"WARN":   SeverityUnknown,
		"DEBUG6": SeverityUnknown,
		"ERRORS": SeverityUnknown,
		"ОШИБКА": SeverityUnknown, // localised input needs MessagesLang
	}
	for in, want := range cases {
		assert.Equal(t, want, ParseSeverity([]byte(in)), "ParseSeverity(%q)", in)
	}
}

func TestSeverityString(t *testing.T) {
	assert.Equal(t, "ERROR", SeverityError.String())
	assert.Equal(t, "DEBUG5", SeverityDebug5.String())
	assert.Equal(t, "", SeverityUnknown.String(), "unknown has no name")
	assert.Equal(t, "", Severity(200).String(), "out of range must not panic")
}

func TestSeverityIsProblem(t *testing.T) {
	problems := []Severity{SeverityWarning, SeverityError, SeverityFatal, SeverityPanic}
	for _, s := range problems {
		assert.True(t, s.IsProblem(), "%s", s)
	}
	fine := []Severity{
		SeverityUnknown, SeverityDebug5, SeverityDebug1,
		SeverityLog, SeverityInfo, SeverityNotice,
	}
	for _, s := range fine {
		assert.False(t, s.IsProblem(), "%s", s)
	}
}

func TestSeverityOrdering(t *testing.T) {
	// The specification's 4.1 ordering, which the doc comment warns is NOT
	// PostgreSQL's log_min_messages ranking. Pinned so that a well-meaning
	// "fix" to match the server has to be a deliberate change.
	assert.Less(t, SeverityLog, SeverityInfo)
	assert.Less(t, SeverityInfo, SeverityNotice)
	assert.Less(t, SeverityNotice, SeverityWarning)
	assert.Less(t, SeverityWarning, SeverityError)
	assert.Less(t, SeverityError, SeverityFatal)
	assert.Less(t, SeverityFatal, SeverityPanic)
	assert.Less(t, SeverityDebug5, SeverityDebug1)
}

// TestAllocSeverity is the FMT-008 gate: no map lookup, no allocation.
func TestAllocSeverity(t *testing.T) {
	err := []byte("ERROR")
	allocs.Zero(t, 100, func() {
		_ = ParseSeverity(err)
		_ = SeverityError.String()
		_ = SeverityError.IsProblem()
	})
}
