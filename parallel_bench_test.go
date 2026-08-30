package pglogwatch

import (
	"bytes"
	"io"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Parallel scaling (PERF-029, AC-019).
//
// PERF-029 asks for at least 0.75x linear scaling to 8 cores on a multi-file
// workload, and AC-019 restates it as 8 jobs reaching 6x the throughput of 1.
// Both are about the multi-FILE case, which is what a log directory is.

// parallelBenchInput builds a multi-file corpus of roughly the given size.
func parallelBenchInput(b *testing.B, files, bytesPerFile int) []io.ReaderAt {
	b.Helper()
	one, err := readFixture("json/basic.json")
	if err != nil {
		b.Fatal(err)
	}
	data := bytes.Repeat(one, max(1, bytesPerFile/len(one)))
	srcs := make([]io.ReaderAt, files)
	for i := range srcs {
		srcs[i] = bytes.NewReader(data)
	}
	return srcs
}

func benchmarkParallelScan(b *testing.B, workers int) {
	const files = 8
	const bytesPerFile = 1 << 20
	srcs := parallelBenchInput(b, files, bytesPerFile)

	var total int64
	for _, s := range srcs {
		total += int64(s.(*bytes.Reader).Size())
	}

	var count atomic.Int64
	b.SetBytes(total)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		err := ParallelScan(b.Context(), srcs, Config{Format: FormatJSON}, workers,
			func(_ int, r *Record) error {
				count.Add(int64(r.Severity))
				return nil
			})
		if err != nil {
			b.Fatal(err)
		}
	}
	sink(count.Load())
}

func BenchmarkParallelScan1(b *testing.B)  { benchmarkParallelScan(b, 1) }
func BenchmarkParallelScan2(b *testing.B)  { benchmarkParallelScan(b, 2) }
func BenchmarkParallelScan4(b *testing.B)  { benchmarkParallelScan(b, 4) }
func BenchmarkParallelScan8(b *testing.B)  { benchmarkParallelScan(b, 8) }
func BenchmarkParallelScan16(b *testing.B) { benchmarkParallelScan(b, 16) }

// TestParallelScanScales is AC-019 as a test rather than as a benchmark, so
// that a regression fails CI rather than needing someone to read a number.
//
// It is skipped in short mode and on machines with too few cores to measure
// anything: asserting a speedup on a two-core CI runner would either fail
// honest code or pass by accident, and neither is worth having.
func TestParallelScanScales(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling measurement is not a short test")
	}
	const need = 8
	if runtime.NumCPU() < need {
		t.Skipf("need %d cores to measure scaling, have %d", need, runtime.NumCPU())
	}

	one := fixture(t, "json/basic.json")
	data := bytes.Repeat(one, (4<<20)/len(one))
	srcs := make([]io.ReaderAt, need)
	for i := range srcs {
		srcs[i] = bytes.NewReader(data)
	}

	run := func(workers int) float64 {
		var n atomic.Int64
		res := testing.Benchmark(func(b *testing.B) {
			b.ResetTimer()
			for range b.N {
				_ = ParallelScan(b.Context(), srcs, Config{Format: FormatJSON}, workers,
					func(_ int, r *Record) error {
						n.Add(int64(r.Severity))
						return nil
					})
			}
		})
		return float64(res.NsPerOp())
	}

	serial := run(1)
	parallel := run(need)
	speedup := serial / parallel
	t.Logf("1 worker %.0f ns/op, %d workers %.0f ns/op, speedup %.2fx",
		serial, need, parallel, speedup)

	// PERF-029's 0.75 of linear on 8 cores. AC-019 states the same bound as
	// 6x. The measurement is inherently noisy on a shared machine, so the
	// assertion is the requirement and not a tighter number.
	assert.GreaterOrEqual(t, speedup, 0.75*float64(need),
		"PERF-029 requires at least 0.75x linear scaling to %d cores", need)
}
