package pglogwatch

import (
	"bytes"
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
)

// TestAllocJSONParse is AC-011 and PERF-001 for jsonlog.
//
// This is the format where the allocation requirement does the most work.
// encoding/json cannot meet it at all -- Unmarshal into a struct allocates for
// every string field, and Decoder.Token allocates per token -- so CON-004's ban
// on encoding/json and this gate are the same requirement stated twice.
func TestAllocJSONParse(t *testing.T) {
	in := bytes.Repeat(fixture(t, "json/basic.json"), 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatJSON})

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			r := p.Record()
			_ = r.Severity
			_ = r.Database
			_ = r.Message
			_ = r.Time
			_ = r.QueryID
		}
	})
}

// TestAllocJSONLocation gates the assembled Location field specifically.
//
// jsonlog splits the source location across func_name, file_name and
// file_line_num, and §4.2 has Record present them joined. Joining needs a
// buffer, so this is the one field in the package that is built rather than
// borrowed -- and the parser's reusable scratch is what keeps it free after the
// first record.
func TestAllocJSONLocation(t *testing.T) {
	in := bytes.Repeat(fixture(t, "json/basic.json"), 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatJSON})
	seen := 0

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			if len(p.Record().Location) > 0 {
				seen++
			}
		}
	})
	assert.NotZero(t, seen, "the fixture must actually exercise Location")
}

// TestAllocJSONEscapes gates the escape path: detecting that a value needs
// unquoting must not itself cost anything, since most callers never unquote.
func TestAllocJSONEscapes(t *testing.T) {
	in := bytes.Repeat(fixture(t, "json/escapes.json"), 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatJSON})

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			_ = p.Record().Flags & FlagNeedsUnquote
		}
	})
}

// BenchmarkJSONFullParse is PERF-023: floor 150 MB/s.
func BenchmarkJSONFullParse(b *testing.B) {
	in := benchInput(b, "json/basic.json")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatJSON})

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()

	for b.Loop() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			r := p.Record()
			sink(r.Time.UnixNano(), int64(r.Severity), int64(len(r.Message)),
				int64(len(r.User)), int64(len(r.Database)), int64(r.ProcessID),
				int64(r.QueryID))
		}
	}
}

// BenchmarkJSONSeverityOnly is the severity-histogram workload on jsonlog.
//
// The gap against BenchmarkJSONFullParse is larger here than on the other
// formats, because a JSON scanner must walk every key to find one -- there is
// no positional shortcut the way csvlog's column index is.
func BenchmarkJSONSeverityOnly(b *testing.B) {
	in := benchInput(b, "json/basic.json")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatJSON})
	var counts [16]int64

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()

	for b.Loop() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			counts[p.Record().Severity]++
		}
	}
	sink(counts[SeverityLog])
}
