package pglogwatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// log_line_prefix auto-detection (FMT-004, AC-003).
//
// Detection matters because the prefix is a server setting the log file does
// not record. A tool reading someone else's log has no way to be told it, and
// requiring one would put a configuration step between an operator and an
// answer -- which is most of why pgwatch's log parsing goes unused today.

func TestPrefixDetection(t *testing.T) {
	cases := []struct {
		name  string
		want  string
		lines []string
	}{{
		name: "AC-003's prefix",
		want: "%m [%p] %q%u@%d ",
		lines: []string{
			"2026-08-30 10:11:12.123 CEST [31337] app_user@appdb LOG:  statement: SELECT 1",
			"2026-08-30 10:11:13.001 CEST [31337] app_user@appdb ERROR:  boom",
			"2026-08-30 10:11:14.500 CEST [31338] LOG:  checkpoint starting: time",
		},
	}, {
		name: "the PostgreSQL default since 15",
		want: "%m [%p] ",
		lines: []string{
			"2026-08-30 10:11:12.123 CEST [31337] LOG:  statement: SELECT 1",
			"2026-08-30 10:11:13.001 CEST [31337] ERROR:  boom",
		},
	}, {
		name: "the older default, seconds only",
		want: "%t [%p]: ",
		lines: []string{
			"2026-08-30 10:11:12 CEST [31337]: LOG:  statement: SELECT 1",
			"2026-08-30 10:11:13 CEST [31337]: ERROR:  boom",
		},
	}, {
		name: "Debian's prefix with session line number",
		want: "%m [%p-%l] %q%u@%d ",
		lines: []string{
			"2026-08-30 10:11:12.123 CEST [31337-1] app_user@appdb LOG:  statement: SELECT 1",
			"2026-08-30 10:11:13.001 CEST [31337-2] app_user@appdb ERROR:  boom",
		},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := strings.Join(c.lines, "\n") + "\n"
			p := New(strings.NewReader(in), Config{Format: FormatStderr})

			n := 0
			for p.Next() {
				r := p.Record()
				assert.NotEqual(t, SeverityUnknown, r.Severity,
					"AC-003: every record must resolve a severity, line %d", n)
				assert.NotZero(t, r.ProcessID, "the pid must be found, line %d", n)
				n++
			}
			require.NoError(t, p.Err())
			assert.Equal(t, len(c.lines), n)
			assert.Zero(t, p.Stats().Malformed)
			assert.Equal(t, c.want, p.DetectedPrefix())
		})
	}
}

func TestPrefixDetectionOnFixture(t *testing.T) {
	// AC-003 end to end: no LinePrefix configured, and every record must
	// come out with a severity.
	recs, p := parseFixture(t, "stderr/basic.log", Config{Format: FormatStderr})
	require.NotEmpty(t, recs)
	assert.Equal(t, "%m [%p] %q%u@%d ", p.DetectedPrefix())
	for i, r := range recs {
		assert.NotEqual(t, SeverityUnknown, r.Severity, "record %d", i)
	}
}

func TestExplicitPrefixIsNotDetected(t *testing.T) {
	// A configured prefix must be used verbatim. Detection second-guessing
	// the caller would make a correct configuration behave unpredictably.
	const prefix = "%m [%p] "
	p := New(strings.NewReader("2026-08-30 10:11:12.123 CEST [31337] LOG:  hello\n"),
		Config{Format: FormatStderr, LinePrefix: prefix})
	require.True(t, p.Next())
	assert.Equal(t, prefix, p.DetectedPrefix())
}

func TestPrefixDetectionBoundedByDetectLines(t *testing.T) {
	// FMT-004 bounds the scan so that opening a 10 GB log does not read it
	// all before producing a record.
	in := strings.Repeat("2026-08-30 10:11:12.123 CEST [31337] LOG:  hello\n", 5000)
	p := New(strings.NewReader(in), Config{Format: FormatStderr, DetectLines: 5})
	require.True(t, p.Next())
	assert.Equal(t, "%m [%p] ", p.DetectedPrefix())
	assert.Less(t, p.Stats().Bytes, int64(len(in)),
		"detection must not consume the whole stream")
}

func TestPrefixDetectionFallsBackGracefully(t *testing.T) {
	// A log with no recognisable prefix must still produce records rather
	// than failing: COR-002 and FMT-010 both say the stream carries on.
	in := "some completely unstructured line\nanother one\n"
	p := New(strings.NewReader(in), Config{Format: FormatStderr})
	n := 0
	for p.Next() {
		n++
	}
	require.NoError(t, p.Err())
	assert.Equal(t, 2, n, "unrecognised lines must still be emitted as records")
}
