package pglogwatch

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"runtime/metrics"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

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

// The scaling measurement.
//
// The previous implementation took ONE testing.Benchmark sample per side, in
// sequence, and divided them. That is not a measurement: the same binary and
// the same benchmark produce between 3.27x and 5.72x depending only on GOGC,
// GOMAXPROCS and whether the two sides ran in one process or two. A ratio of
// two single samples cannot tell a ceiling in the code from thermal state.
//
// So this one interleaves the sides, warms up, repeats, and reports a spread.
// It also reports how many CPUs were actually BUSY, because "4x at 8 workers"
// has two entirely different causes -- cores that sat idle, and cores that ran
// slower than they do alone -- and the fix is different for each.
//
// Tracked as https://github.com/cybertec-postgresql/pglogwatch/issues/3.

const (
	// scalingWarmupReps are discarded. The first run of a session is faster
	// than the tenth on a boosting CPU and slower on a cold page cache, and
	// neither is the steady state the threshold is stated against.
	scalingWarmupReps = 2

	// scalingDefaultReps is TST-011's minimum. PGLOGWATCH_BENCH_REPS raises
	// it on a machine noisy enough to need more.
	scalingDefaultReps = 10

	// scalingSpan is how long one sample should take. Both sides are timed
	// over about this long -- the faster side simply runs more calls --
	// rather than the parallel side being eight times shorter and eight
	// times noisier than the side it is divided into.
	scalingSpan = 250 * time.Millisecond

	// scalingNoiseRatio is how far the median and best-case speedups may
	// diverge before the run is called too noisy to publish (VAL-004).
	scalingNoiseRatio = 1.15
)

// cpuClassNames are the runtime/metrics counters that say how the CPUs were
// spent. runtime/metrics is used rather than getrusage or GetProcessTimes
// because it is portable: the Linux reference machine and a Windows
// development checkout then report the same quantity.
var cpuClassNames = [...]string{
	"/cpu/classes/total:cpu-seconds",
	"/cpu/classes/idle:cpu-seconds",
	"/cpu/classes/gc/total:cpu-seconds",
}

type cpuClasses struct {
	total, idle, gc float64
	ok              bool
}

// readCPUClasses samples the CPU counters, reporting ok=false rather than
// failing if a future Go renames one. A missing diagnostic is worth a line
// saying so; it is not worth failing a threshold measurement over.
//
// The runtime accumulates these counters at the end of a garbage collection
// cycle, not continuously, so a window that collects nothing shows no change
// at all -- which is precisely the 1-worker side, since it makes an eighth of
// the garbage. flushCPUClasses is what makes the sample meaningful; read the
// counters through it rather than calling this directly.
func readCPUClasses() cpuClasses {
	samples := make([]metrics.Sample, len(cpuClassNames))
	for i, name := range cpuClassNames {
		samples[i].Name = name
	}
	metrics.Read(samples)
	for i := range samples {
		if samples[i].Value.Kind() != metrics.KindFloat64 {
			return cpuClasses{}
		}
	}
	return cpuClasses{
		total: samples[0].Value.Float64(),
		idle:  samples[1].Value.Float64(),
		gc:    samples[2].Value.Float64(),
		ok:    true,
	}
}

// flushCPUClasses forces the accumulation described above and then samples.
//
// The forced collection runs OUTSIDE the timed region, so it does not enter
// the wall clock; its own CPU time does land in the counter delta, which
// overstates parallelism by roughly a millisecond of CPU against a window of
// scalingSpan across every P. That is under a tenth of a percent, and it is
// the price of the counters being readable at all.
func flushCPUClasses() cpuClasses {
	runtime.GC()
	return readCPUClasses()
}

// scalingSample is one timed measurement of ParallelScan at one worker count.
type scalingSample struct {
	perCall time.Duration

	// parallelism is CPU-seconds burned per second of wall clock: how many
	// CPUs were busy. It is bounded above by GOMAXPROCS by construction,
	// which is exactly the honesty wanted -- 8 workers on a process limited
	// to 6.3 busy CPUs has not earned the right to blame the parser.
	parallelism float64

	// gcShare is the fraction of that spent collecting. ParallelScan
	// allocates one parser buffer per worker per call, so the parallel side
	// makes about eight times the garbage the serial side does.
	gcShare float64
}

// measureScaling runs iters calls and reports the per-call wall time along
// with how many CPUs were busy while it did.
func measureScaling(ctx context.Context, tb testing.TB, srcs []io.ReaderAt,
	cfg Config, workers, iters int,
) scalingSample {
	tb.Helper()

	var n atomic.Int64
	fn := func(_ int, r *Record) error {
		n.Add(int64(r.Severity))
		return nil
	}

	before := flushCPUClasses()
	start := time.Now()
	for range iters {
		if err := ParallelScan(ctx, srcs, cfg, workers, fn); err != nil {
			tb.Fatal(err)
		}
	}
	wall := time.Since(start)
	after := flushCPUClasses()
	sink(n.Load())

	s := scalingSample{perCall: wall / time.Duration(iters)}
	if before.ok && after.ok && wall > 0 {
		busy := (after.total - after.idle) - (before.total - before.idle)
		s.parallelism = busy / wall.Seconds()
		if busy > 0 {
			s.gcShare = (after.gc - before.gc) / busy
		}
	}
	return s
}

// calibrateIters returns how many calls fill about scalingSpan.
func calibrateIters(ctx context.Context, tb testing.TB, srcs []io.ReaderAt,
	cfg Config, workers int,
) int {
	tb.Helper()
	one := measureScaling(ctx, tb, srcs, cfg, workers, 1).perCall
	if one <= 0 {
		return 1
	}
	return max(1, int(scalingSpan/one))
}

// scalingSummary reduces a set of samples to what is worth printing.
type scalingSummary struct {
	median, lo, hi  time.Duration
	parallelism, gc float64
}

func summariseScaling(ss []scalingSample) scalingSummary {
	walls := make([]time.Duration, len(ss))
	par := make([]float64, len(ss))
	gc := make([]float64, len(ss))
	for i, s := range ss {
		walls[i], par[i], gc[i] = s.perCall, s.parallelism, s.gcShare
	}
	slices.Sort(walls)
	slices.Sort(par)
	slices.Sort(gc)
	return scalingSummary{
		median:      walls[len(walls)/2],
		lo:          walls[0],
		hi:          walls[len(walls)-1],
		parallelism: par[len(par)/2],
		gc:          gc[len(gc)/2],
	}
}

func (s scalingSummary) String() string {
	return s.median.Round(time.Microsecond).String() +
		" median (" + s.lo.Round(time.Microsecond).String() +
		"-" + s.hi.Round(time.Microsecond).String() + ")" +
		", parallelism " + strconv.FormatFloat(s.parallelism, 'f', 2, 64) +
		", gc " + strconv.FormatFloat(100*s.gc, 'f', 1, 64) + "%"
}

// measureSpeedup runs the two sides alternately so that drift -- boost state,
// thermals, a page cache still filling -- lands on both of them equally, and
// returns the median and best-case ratios.
//
// The median is the headline. The best case is reported beside it because for
// a CPU-bound benchmark the minimum is the least noise-contaminated estimator:
// when the two disagree, the run measured the machine rather than the code.
func measureSpeedup(ctx context.Context, t *testing.T, srcs []io.ReaderAt,
	cfg Config, need, reps int,
) (median, best float64) {
	t.Helper()

	iters1 := calibrateIters(ctx, t, srcs, cfg, 1)
	itersN := calibrateIters(ctx, t, srcs, cfg, need)

	serial := make([]scalingSample, 0, reps)
	parallel := make([]scalingSample, 0, reps)
	for rep := range scalingWarmupReps + reps {
		a := measureScaling(ctx, t, srcs, cfg, 1, iters1)
		b := measureScaling(ctx, t, srcs, cfg, need, itersN)
		if rep < scalingWarmupReps {
			continue
		}
		serial = append(serial, a)
		parallel = append(parallel, b)
	}

	one, many := summariseScaling(serial), summariseScaling(parallel)
	t.Logf("  1 worker : %s (%d calls/sample)", one, iters1)
	t.Logf("  %d workers: %s (%d calls/sample)", need, many, itersN)

	median = float64(one.median) / float64(many.median)
	best = float64(one.lo) / float64(many.lo)
	t.Logf("  speedup %.2fx (median), %.2fx (best of %d)", median, best, reps)

	if ratio := max(median, best) / min(median, best); ratio > scalingNoiseRatio {
		t.Logf("  WARNING: median and best-case speedups differ by %.0f%%; this run "+
			"measured the machine, not the code, and must not be published (VAL-004)",
			100*(ratio-1))
	}
	return median, best
}

// TestParallelScanScales measures AC-019 and PERF-029.
//
// The measurement always runs and always logs its result. The ASSERTION runs
// only on the reference machine (PGLOGWATCH_BENCH_MACHINE=1) and only at the
// GOMAXPROCS the requirement is stated for.
//
// That is not a way to dodge the threshold. PERF-029 and AC-019 are defined in
// §3.4 against the reference machine of §6, and a scaling ratio measured at
// some other GOMAXPROCS is a different quantity: 8 workers spread over 16 Ps
// leave half the runtime's schedulers spinning for work they will not find,
// and on an SMT machine that steals issue slots from the siblings doing the
// parsing. The test refuses to assert on a configuration it did not ask for
// rather than reporting a number that looks like AC-019 and is not.
func TestParallelScanScales(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling measurement is not a short test")
	}
	const need = 8
	if runtime.NumCPU() < need {
		t.Skipf("need %d cores to measure scaling, have %d", need, runtime.NumCPU())
	}

	reps := scalingReps(t)
	gomaxprocs := runtime.GOMAXPROCS(0)
	t.Logf("NumCPU=%d GOMAXPROCS=%d reps=%d", runtime.NumCPU(), gomaxprocs, reps)
	if runtime.NumCPU() > gomaxprocs {
		// The scheduler is free to move the worker threads around every
		// CPU, and on a multi-die part that means across dies: a thread
		// that migrates arrives with none of its cache. Serial-side
		// spreads of better than two to one come from this, and they are
		// wide enough to swamp the effect being measured.
		t.Logf("  hint: %d CPUs but GOMAXPROCS=%d, so threads may migrate across "+
			"cores or dies between samples. Pin them for a tighter spread: "+
			"taskset -c <one logical CPU per physical core> go test ...",
			runtime.NumCPU(), gomaxprocs)
	}

	one := fixture(t, "json/basic.json")
	cfg := Config{Format: FormatJSON}

	// Two working sets. bench/THRESHOLDS.md records that scaling improves as
	// the input grows, which is the signature of a fixed per-call cost being
	// amortised rather than of a bandwidth ceiling. Measuring both says
	// whether that cost is still there; if it is gone the two converge.
	//
	// The first is the size AC-019 has been measured at, and is the headline.
	var headline float64
	for i, bytesPerFile := range []int{4 << 20, 16 << 20} {
		data := bytes.Repeat(one, bytesPerFile/len(one))
		srcs := make([]io.ReaderAt, need)
		for j := range srcs {
			srcs[j] = bytes.NewReader(data)
		}
		t.Logf("%d files x %d MiB (%d MiB total):",
			need, bytesPerFile>>20, (need*bytesPerFile)>>20)

		median, _ := measureSpeedup(t.Context(), t, srcs, cfg, need, reps)
		if i == 0 {
			headline = median
		}
	}

	if os.Getenv("PGLOGWATCH_BENCH_MACHINE") != "1" {
		t.Skipf("measured %.2fx; PERF-029's threshold is asserted only on the "+
			"reference machine (set PGLOGWATCH_BENCH_MACHINE=1 there)", headline)
	}
	if gomaxprocs != need {
		t.Skipf("measured %.2fx at GOMAXPROCS=%d; AC-019 is stated for %d workers "+
			"on %d cores, so run it as: GOMAXPROCS=%d go test -run TestParallelScanScales .",
			headline, gomaxprocs, need, need, need)
	}
	// PERF-029's 0.75 of linear on 8 cores; AC-019 states the same bound as
	// 6x. The assertion is the requirement itself, not a tighter number,
	// because the measurement is noisy even on a dedicated machine.
	assert.GreaterOrEqual(t, headline, 0.75*float64(need),
		"PERF-029 requires at least 0.75x linear scaling to %d cores", need)
}

// scalingReps resolves PGLOGWATCH_BENCH_REPS.
func scalingReps(t *testing.T) int {
	t.Helper()
	v := os.Getenv("PGLOGWATCH_BENCH_REPS")
	if v == "" {
		return scalingDefaultReps
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("PGLOGWATCH_BENCH_REPS=%q is not a positive integer", v)
	}
	return n
}
