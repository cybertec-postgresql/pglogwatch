package pglogwatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Localised severities on the stderr path (AC-006, E12, E13).
//
// A server with lc_messages set writes severities in that language, so the
// literal "ERROR" never appears. Every consumer that counts errors -- pgwatch's
// server_log_event_counts included -- therefore reports zero for such a server
// unless the parser normalises. That is a silent zero, not an error, which
// makes it the kind of bug that survives for years.

func TestStderrLocalisedSeverity(t *testing.T) {
	recs, p := parseFixture(t, "stderr/lc_messages_ru.log", Config{
		Format:       FormatStderr,
		LinePrefix:   multilinePrefix,
		MessagesLang: "ru",
	})
	require.Len(t, recs, 3)
	assert.Zero(t, p.Stats().Malformed)

	// AC-006 names this case exactly.
	assert.Equal(t, SeverityError, recs[0].Severity)
	assert.Equal(t, "ОШИБКА", string(recs[0].RawSeverity),
		"RawSeverity must hold the original bytes, so nothing is lost")

	// E12.
	assert.Equal(t, SeverityWarning, recs[1].Severity)
	assert.Equal(t, "ПРЕДУПРЕЖДЕНИЕ", string(recs[1].RawSeverity))

	assert.Equal(t, SeverityLog, recs[2].Severity)
}

func TestStderrLocalisedSeverityWrongLanguage(t *testing.T) {
	// E13: the wrong MessagesLang yields SeverityUnknown, the record is
	// STILL emitted, and Malformed is NOT incremented.
	//
	// All three halves matter. A parser that dropped the record would lose
	// the log; one that counted it malformed would make Stats.Malformed
	// useless as a signal, since a misconfigured language would swamp it.
	recs, p := parseFixture(t, "stderr/lc_messages_ru.log", Config{
		Format:       FormatStderr,
		LinePrefix:   multilinePrefix,
		MessagesLang: "de",
	})
	require.Len(t, recs, 3, "records must still be emitted")
	assert.Zero(t, p.Stats().Malformed, "a language mismatch is not a malformed line")
	for i, r := range recs {
		assert.Equal(t, SeverityUnknown, r.Severity, "record %d", i)
		assert.NotEmpty(t, r.RawSeverity, "record %d: the raw bytes must survive", i)
	}
}

func TestStderrLocalisedSeverityDefaultLanguage(t *testing.T) {
	// With no MessagesLang the parser assumes English, so a Russian log
	// resolves nothing -- and still parses. This is the default a user hits
	// before they know MessagesLang exists, so it must degrade rather than
	// fail.
	recs, p := parseFixture(t, "stderr/lc_messages_ru.log",
		Config{Format: FormatStderr, LinePrefix: multilinePrefix})
	require.Len(t, recs, 3)
	assert.Zero(t, p.Stats().Malformed)
	assert.Equal(t, SeverityUnknown, recs[0].Severity)
	assert.Equal(t, "ОШИБКА", string(recs[0].RawSeverity),
		"a caller can still recover the severity from the raw bytes")
}

func TestStderrUnknownLanguagePassesThrough(t *testing.T) {
	// FMT-007: an unrecognised MessagesLang falls back to English
	// resolution rather than to nothing.
	recs, _ := parseFixture(t, "stderr/basic.log", Config{
		Format:       FormatStderr,
		LinePrefix:   multilinePrefix,
		MessagesLang: "klingon",
	})
	require.NotEmpty(t, recs)
	assert.Equal(t, SeverityLog, recs[0].Severity)
}
