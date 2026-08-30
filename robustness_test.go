package pglogwatch

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Robustness: what happens when the input is not what the parser expected.
//
// The governing rule across all of it (FMT-010, IFC-003, COR-002) is that the
// stream carries on. A log is an append-only file written by another process
// while being read; torn lines, truncated tails and unfamiliar formats are
// normal, not exceptional. A parser that stopped at the first one would fail
// exactly when a system is under stress and its logs matter most.

func TestMalformedLineIsCountedAndSkipped(t *testing.T) {
	// AC-007 and FMT-010, in all three formats.
	cases := []struct {
		name   string
		format Format
		prefix string
		good   string
		bad    string
	}{{
		name:   "csvlog",
		format: FormatCSV,
		good: `2026-08-30 10:11:12.123 CEST,"u","d",1,"h",s,1,"t",2026-08-30 10:10:00 CEST,` +
			`1/1,0,LOG,00000,"ok",,,,,,,,,"a","b",,0`,
		bad: "far,too,few,columns",
	}, {
		name:   "jsonlog",
		format: FormatJSON,
		good:   `{"error_severity":"LOG","message":"ok"}`,
		bad:    `{"error_severity":"LOG",,,`,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.good + "\n" + c.bad + "\n" + c.good + "\n"
			var lines [][]byte
			var errs []error
			p := New(strings.NewReader(in), Config{
				Format:     c.format,
				LinePrefix: c.prefix,
				OnMalformed: func(line []byte, err error) {
					lines = append(lines, bytes.Clone(line))
					errs = append(errs, err)
				},
			})
			n := 0
			for p.Next() {
				assert.Equal(t, "ok", string(p.Record().Message))
				n++
			}

			require.NoError(t, p.Err(), "IFC-003: a malformed line is never fatal")
			assert.Equal(t, 2, n, "both good records must survive the bad one between them")
			assert.Equal(t, int64(1), p.Stats().Malformed)

			require.Len(t, lines, 1)
			assert.Equal(t, c.bad, string(lines[0]),
				"OnMalformed must receive the offending bytes")
			require.Len(t, errs, 1)
			assert.ErrorIs(t, errs[0], ErrMalformedLine)
		})
	}
}

func TestMalformedCallbackSliceIsBorrowed(t *testing.T) {
	// The line handed to OnMalformed is borrowed on the same terms as a
	// Record's fields (FMT-010). Documenting that is not enough on its own,
	// but the test at least pins that the callback sees the right bytes at
	// the time it is called.
	in := "not a csv line at all\n" +
		`2026-08-30 10:11:12.123 CEST,"u","d",1,"h",s,1,"t",2026-08-30 10:10:00 CEST,1/1,0,LOG,00000,"ok",,,,,,,,,"a","b",,0` + "\n"
	seen := ""
	p := New(strings.NewReader(in), Config{
		Format:      FormatCSV,
		OnMalformed: func(line []byte, _ error) { seen = string(line) },
	})
	for p.Next() { //nolint:revive // drain
	}
	require.NoError(t, p.Err())
	assert.Equal(t, "not a csv line at all", seen)
}

func TestMalformedDoesNotStopALongStream(t *testing.T) {
	// The property that actually matters: bad lines throughout a long log
	// must not reduce the number of good records recovered.
	good := `{"error_severity":"LOG","message":"ok"}` + "\n"
	bad := "}{\n"
	var sb strings.Builder
	for range 500 {
		sb.WriteString(good)
		sb.WriteString(bad)
	}
	p := New(strings.NewReader(sb.String()), Config{Format: FormatJSON})
	n := 0
	for p.Next() {
		n++
	}
	require.NoError(t, p.Err())
	assert.Equal(t, 500, n)
	assert.Equal(t, int64(500), p.Stats().Malformed)
}

func TestTruncatedTailAcrossFormats(t *testing.T) {
	// FMT-009 and AC-008 in all three formats. A file whose last line has no
	// newline is what reading a live log always looks like, so this is the
	// common case rather than a corner one.
	cases := []struct {
		name   string
		format Format
		prefix string
		in     string
		want   int
	}{{
		name:   "csvlog",
		format: FormatCSV,
		in: `2026-08-30 10:11:12.123 CEST,"u","d",1,"h",s,1,"t",2026-08-30 10:10:00 CEST,1/1,0,LOG,00000,"first",,,,,,,,,"a","b",,0` + "\n" +
			`2026-08-30 10:11:13.123 CEST,"u","d",1,"h",s,2,"t",2026-08-30 10:10:00 CEST,1/1,0,LOG,00000,"second",,,,,,,,,"a","b",,0`,
		want: 2,
	}, {
		name:   "jsonlog",
		format: FormatJSON,
		in: `{"error_severity":"LOG","message":"first"}` + "\n" +
			`{"error_severity":"LOG","message":"second"}`,
		want: 2,
	}, {
		name:   "stderr",
		format: FormatStderr,
		prefix: "%m [%p] ",
		in: "2026-08-30 10:11:12.123 CEST [1] LOG:  first\n" +
			"2026-08-30 10:11:13.123 CEST [1] LOG:  second",
		want: 2,
	}}

	for _, c := range cases {
		t.Run(c.name+"/emitted with FlagTruncated", func(t *testing.T) {
			p := New(strings.NewReader(c.in), Config{Format: c.format, LinePrefix: c.prefix})
			var last *Record
			n := 0
			for p.Next() {
				last = p.Record()
				n++
			}
			require.NoError(t, p.Err())
			require.Equal(t, c.want, n)
			require.NotNil(t, last)
			assert.Equal(t, "second", string(last.Message))
			assert.NotZero(t, last.Flags&FlagTruncated,
				"AC-008: an unterminated final record must be flagged")
		})

		t.Run(c.name+"/discarded on request", func(t *testing.T) {
			p := New(strings.NewReader(c.in), Config{
				Format:          c.format,
				LinePrefix:      c.prefix,
				NoTruncatedTail: true,
			})
			n := 0
			for p.Next() {
				assert.Zero(t, p.Record().Flags&FlagTruncated)
				n++
			}
			require.NoError(t, p.Err())
			assert.Equal(t, c.want-1, n)
		})
	}
}

func TestTruncatedFlagOnlyOnTheLastRecord(t *testing.T) {
	// The flag must mark the record that was cut short, not every record in
	// a file that happens to end without a newline.
	in := `{"error_severity":"LOG","message":"a"}` + "\n" +
		`{"error_severity":"LOG","message":"b"}` + "\n" +
		`{"error_severity":"LOG","message":"c"}`
	p := New(strings.NewReader(in), Config{Format: FormatJSON})
	var flags []Flags
	for p.Next() {
		flags = append(flags, p.Record().Flags&FlagTruncated)
	}
	require.NoError(t, p.Err())
	require.Len(t, flags, 3)
	assert.Zero(t, flags[0])
	assert.Zero(t, flags[1])
	assert.NotZero(t, flags[2])
}
