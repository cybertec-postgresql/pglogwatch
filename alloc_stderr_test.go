package pglogwatch

import (
	"bytes"
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
)

// TestAllocStderrParse is AC-011 and PERF-001 for stderr.
//
// The stderr path has two allocation hazards csvlog does not: the compiled
// prefix template, which must be built once per parser and not per record, and
// continuation folding, which must not build a new buffer per multi-line
// record.
func TestAllocStderrParse(t *testing.T) {
	in := bytes.Repeat(fixture(t, "stderr/basic.log"), 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatStderr, LinePrefix: multilinePrefix})

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			r := p.Record()
			_ = r.Severity
			_ = r.Database
			_ = r.Message
			_ = r.Time
		}
	})
}

// TestAllocStderrMultiline gates the folding path specifically: a record that
// absorbs three continuation lines must still cost nothing.
func TestAllocStderrMultiline(t *testing.T) {
	in := bytes.Repeat(fixture(t, "stderr/multiline.log"), 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatStderr, LinePrefix: multilinePrefix})

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			r := p.Record()
			_ = r.Detail
			_ = r.Hint
			_ = r.Statement
		}
	})
}

// TestAllocStderrDetectedPrefix checks that auto-detection is a start-up cost
// and not a per-record one.
func TestAllocStderrDetectedPrefix(t *testing.T) {
	in := bytes.Repeat(fixture(t, "stderr/basic.log"), 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatStderr})

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			_ = p.Record().Severity
		}
	})
	assert.NotEmpty(t, p.DetectedPrefix())
}

// BenchmarkStderrFullParse is PERF-022: floor 200 MB/s.
func BenchmarkStderrFullParse(b *testing.B) {
	in := benchInput(b, "stderr/basic.log")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatStderr, LinePrefix: multilinePrefix})

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			r := p.Record()
			sink(r.Time.UnixNano(), int64(r.Severity), int64(len(r.Message)),
				int64(len(r.User)), int64(len(r.Database)), int64(r.ProcessID))
		}
	}
}

// BenchmarkStderrSeverityOnly is the severity-histogram workload on stderr,
// the shape pgwatch runs.
func BenchmarkStderrSeverityOnly(b *testing.B) {
	in := benchInput(b, "stderr/basic.log")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatStderr, LinePrefix: multilinePrefix})
	var counts [16]int64

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			counts[p.Record().Severity]++
		}
	}
	sink(counts[SeverityLog])
}

// BenchmarkStderrMultiline measures the folding path, where each record costs
// four physical lines instead of one.
func BenchmarkStderrMultiline(b *testing.B) {
	in := benchInput(b, "stderr/multiline.log")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatStderr, LinePrefix: multilinePrefix})

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			r := p.Record()
			sink(int64(len(r.Message)), int64(len(r.Detail)), int64(len(r.Statement)))
		}
	}
}
