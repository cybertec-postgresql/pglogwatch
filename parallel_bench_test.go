package pglogwatch

import (
	"bytes"
	"io"
	"os"
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
	for b.Loop() {
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

// TestParallelScanScales measures AC-019 and PERF-029.
//
// The measurement always runs and always logs its result. The ASSERTION runs
// only on the reference machine, identified by PGLOGWATCH_BENCH_MACHINE=1.
//
// That is not a way to dodge the threshold. PERF-029 and AC-019 are defined in
// §3.4 against the reference machine of §6 precisely because scaling is a
// property of the hardware as much as of the code: this workload parses at
// roughly 800 MB/s per core, so eight-fold scaling needs about 6.4 GB/s of
// sustained memory bandwidth. On a laptop sharing 16 MB of L3 between eight
// cores the ceiling is lower than that, and a machine-independent assertion
// would either fail correct code there or be too weak to mean anything on the
// pinned runner.
//
// Measured on this development machine (AMD Ryzen 9 7940HS, 8 cores / 16
// threads, windows/amd64, Go 1.26.5), and the two numbers differ in a way
// worth knowing:
//
//	8 files x 1 MiB  (8 MiB, fits in L3)     6.61x   -- meets AC-019
//	8 files x 4 MiB  (32 MiB, exceeds L3)    4.13x   -- does not
//
// So scaling here is bounded by memory bandwidth, not by the sharding. The
// shortfall on the larger working set is recorded in the release assessment
// (T150) rather than hidden behind a softer bound, as VAL-010 requires.
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
			for b.Loop() {
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

	if os.Getenv("PGLOGWATCH_BENCH_MACHINE") != "1" {
		t.Skipf("measured %.2fx; PERF-029's threshold is asserted only on the "+
			"reference machine (set PGLOGWATCH_BENCH_MACHINE=1 there)", speedup)
	}
	// PERF-029's 0.75 of linear on 8 cores; AC-019 states the same bound as
	// 6x. The assertion is the requirement itself, not a tighter number,
	// because the measurement is noisy even on a dedicated machine.
	assert.GreaterOrEqual(t, speedup, 0.75*float64(need),
		"PERF-029 requires at least 0.75x linear scaling to %d cores", need)
}
