package pglogwatch

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// csvlog benchmarks.
//
// Every one reports MB/s via b.SetBytes and allocations via b.ReportAllocs, so
// a single `go test -bench . -benchmem` run answers both the throughput
// requirements (PERF-020, PERF-021) and AC-012's "0 B/op, 0 allocs/op".
//
// The input is a fixture repeated to roughly 1 MB. Smaller inputs measure
// start-up, and larger ones stop fitting in L2 and start measuring the
// memory subsystem instead of the parser.

const benchTargetBytes = 1 << 20

// readFixture is the testing.TB-free form of fixture, since benchmarks and
// fuzz seeds both need fixtures outside a *testing.T.
func readFixture(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", name))
}

func benchInput(b *testing.B, name string) []byte {
	b.Helper()
	one, err := readFixture(name)
	if err != nil {
		b.Fatal(err)
	}
	return bytes.Repeat(one, max(1, benchTargetBytes/len(one)))
}

// BenchmarkCSVFullParse is PERF-020: full-field parsing, floor 250 MB/s.
func BenchmarkCSVFullParse(b *testing.B) {
	in := benchInput(b, "csv/pg14-basic.csv")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatCSV})

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			r := p.Record()
			// Touch every field group so the benchmark measures
			// extraction, not just framing. A parser that filled
			// fields lazily would otherwise look artificially fast.
			sink(r.Time.UnixNano(), int64(r.Severity), int64(len(r.Message)),
				int64(len(r.User)), int64(len(r.Database)), int64(r.ProcessID),
				int64(len(r.Query)), int64(r.QueryID))
		}
	}
}

// BenchmarkCSVSeverityOnly is PERF-021: the severity-histogram scan, floor
// 800 MB/s. It is the workload pgwatch runs, and the gap between it and the
// full parse is what field extraction actually costs.
func BenchmarkCSVSeverityOnly(b *testing.B) {
	in := benchInput(b, "csv/pg14-basic.csv")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatCSV})
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
	sink(counts[SeverityError])
}

// BenchmarkCSVFraming measures the split function alone, with no field
// extraction at all. It is the ceiling every other csvlog number is measured
// against: if full parsing is close to this, extraction is cheap.
func BenchmarkCSVFraming(b *testing.B) {
	in := benchInput(b, "csv/pg14-basic.csv")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatCSV})

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			sink(int64(len(p.Record().Raw)))
		}
	}
}

// BenchmarkCSVClone measures the one sanctioned allocation path (PERF-003), so
// that the cost of retaining a record is a number rather than a guess.
func BenchmarkCSVClone(b *testing.B) {
	in := benchInput(b, "csv/pg14-basic.csv")
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatCSV})

	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			sink(int64(len(p.Record().Clone().Message)))
		}
	}
}

// sink defeats dead-store elimination without allocating or branching in a way
// the compiler can fold away.
var sinkValue int64

func sink(vs ...int64) {
	var acc int64
	for _, v := range vs {
		acc += v
	}
	sinkValue = acc
}
