package pglogwatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Severity normalisation has to give the same answer whichever destination the
// server was configured for (FMT-007, COR-004). Otherwise pgwatch's
// server_log_event_counts -- which is per-severity counters keyed by the
// English name -- would report different numbers for the same activity
// depending on log_destination, and the CON-007 promise that the measurement
// stays byte-identical after migration could not hold.
//
// The two paths resolve severity through the same resolver, but they reach it
// from opposite directions: csvlog takes a column, stderr takes the token
// before a colon. This test is what keeps them from drifting.

// sameEventInThreeFormats renders one log event as csvlog and as stderr, with a
// severity spelled in the given language.
func sameEventInThreeFormats(severity string) (csvlog, stderrlog string) {
	csvlog = `2026-08-30 10:11:12.123 CEST,"app_user","appdb",31337,"10.0.0.5:52344",` +
		`68b2c4a0.7a69,7,"SELECT",2026-08-30 10:10:00 CEST,3/15,0,` + severity +
		`,42P01,"boom",,,,,,,,,"psql","client backend",,0` + "\n"
	stderrlog = "2026-08-30 10:11:12.123 CEST [31337] app_user@appdb " + severity + ":  boom\n"
	return csvlog, stderrlog
}

func TestSeverityAgreesAcrossFormats(t *testing.T) {
	cases := []struct {
		lang     string
		spelling string
		want     Severity
	}{
		{"en", "ERROR", SeverityError},
		{"en", "WARNING", SeverityWarning},
		{"ru", "ОШИБКА", SeverityError},
		{"ru", "ПРЕДУПРЕЖДЕНИЕ", SeverityWarning},
		{"de", "FEHLER", SeverityError},
		{"zh", "错误", SeverityError},
		// A spelling the configured language does not know resolves to
		// Unknown in BOTH formats -- consistently wrong beats
		// inconsistently right (E13).
		{"de", "ОШИБКА", SeverityUnknown},
	}

	for _, c := range cases {
		t.Run(c.lang+"/"+c.spelling, func(t *testing.T) {
			csvIn, stderrIn := sameEventInThreeFormats(c.spelling)

			csvP := New(strings.NewReader(csvIn), Config{
				Format: FormatCSV, MessagesLang: c.lang,
			})
			require.True(t, csvP.Next())
			csvRec := csvP.Record().Clone()

			stderrP := New(strings.NewReader(stderrIn), Config{
				Format:       FormatStderr,
				LinePrefix:   "%m [%p] %q%u@%d ",
				MessagesLang: c.lang,
			})
			require.True(t, stderrP.Next())
			stderrRec := stderrP.Record().Clone()

			assert.Equal(t, c.want, csvRec.Severity, "csvlog")
			assert.Equal(t, c.want, stderrRec.Severity, "stderr")
			assert.Equal(t, csvRec.Severity, stderrRec.Severity,
				"the same event must yield the same severity in both formats")
			assert.Equal(t, string(csvRec.RawSeverity), string(stderrRec.RawSeverity),
				"RawSeverity must hold the same original bytes in both formats")
		})
	}
}

func TestSharedFieldsAgreeAcrossFormats(t *testing.T) {
	// COR-004 more broadly, for the fields both destinations carry.
	csvIn, stderrIn := sameEventInThreeFormats("ERROR")

	csvP := New(strings.NewReader(csvIn), Config{Format: FormatCSV})
	require.True(t, csvP.Next())
	c := csvP.Record().Clone()

	stderrP := New(strings.NewReader(stderrIn), Config{
		Format:     FormatStderr,
		LinePrefix: "%m [%p] %q%u@%d ",
	})
	require.True(t, stderrP.Next())
	s := stderrP.Record().Clone()

	assert.Equal(t, string(c.User), string(s.User))
	assert.Equal(t, string(c.Database), string(s.Database))
	assert.Equal(t, c.ProcessID, s.ProcessID)
	assert.Equal(t, string(c.Message), string(s.Message))
	assert.True(t, c.Time.Equal(s.Time), "csvlog %s, stderr %s", c.Time, s.Time)
	assert.Equal(t, c.Severity, s.Severity)
}
