package bench_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybertec-postgresql/pglogwatch"
	"github.com/cybertec-postgresql/pglogwatch/bench/gen"
)

// PERF-026: peak memory must be O(1) in input size, and under 64 MiB for a
// 10 GB input.
//
// The requirement is stated in peak RSS, which cannot be measured here: Go
// exposes a child's peak resident set through ru_maxrss, and Windows does not
// provide it. What CAN be measured, on any platform, is the property the RSS
// bound is a proxy for -- that memory does not grow with input size -- and it
// is measured directly rather than inferred from a single number.
//
// bench/THRESHOLDS.md records the gap; this file closes as much of it as the
// platform allows.

// heapDuring returns the largest heap observed while parsing a file.
//
// Sampling from a second goroutine, rather than reading MemStats once at the
// end, is what makes this a PEAK rather than a final figure -- a parser that
// buffered the whole file and released it before returning would look
// perfectly frugal to a single reading.
func heapDuring(tb testing.TB, path string, cfg pglogwatch.Config) (peak uint64, records int64) {
	tb.Helper()

	f, err := os.Open(path) //nolint:gosec // generated corpus
	require.NoError(tb, err)
	defer func() { require.NoError(tb, f.Close()) }()

	// Collect before sampling starts, and only then start the sampler.
	//
	// Getting this order wrong silently measures the wrong program: the
	// corpus generator holds every event in memory while writing, so a
	// sampler started first records ITS heap -- 160 MB of generator, read
	// as 160 MB of parser -- and the bound test fails for a reason that has
	// nothing to do with the parser. Two collections, because the first can
	// leave finalisable objects behind.
	runtime.GC()
	runtime.GC()

	done := make(chan struct{})
	peaks := make(chan uint64, 1)
	go func() {
		// Sampled on a timer, not in a tight loop. ReadMemStats stops
		// the world on every call, so spinning on it does not observe
		// the parser -- it throttles it, and then reports the memory
		// used by a program running at a fraction of its speed.
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()

		var m runtime.MemStats
		var high uint64
		sample := func() {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > high {
				high = m.HeapAlloc
			}
		}
		for {
			select {
			case <-done:
				sample() // once more, in case the peak was late
				peaks <- high
				return
			case <-tick.C:
				sample()
			}
		}
	}()

	p := pglogwatch.New(f, cfg)
	for p.Next() {
		records++
	}
	require.NoError(tb, p.Err())
	close(done)
	return <-peaks, records
}

func TestMemoryDoesNotGrowWithInput(t *testing.T) {
	if testing.Short() {
		t.Skip("memory measurement is not a short test")
	}
	dir := t.TempDir()

	small, err := gen.Write(filepath.Join(dir, "small"), gen.Config{Seed: 1, Records: 20000})
	require.NoError(t, err)
	large, err := gen.Write(filepath.Join(dir, "large"), gen.Config{Seed: 1, Records: 400000})
	require.NoError(t, err)

	require.Greater(t, large.TotalSize, small.TotalSize*15,
		"the two corpora must differ enough for growth to be visible")

	for _, f := range []struct {
		name string
		cfg  pglogwatch.Config
	}{
		{"postgresql-pg14.csv", pglogwatch.Config{Format: pglogwatch.FormatCSV}},
		{"postgresql.json", pglogwatch.Config{Format: pglogwatch.FormatJSON}},
		{"postgresql.log", pglogwatch.Config{
			Format: pglogwatch.FormatStderr, LinePrefix: gen.StderrPrefix,
		}},
	} {
		t.Run(f.name, func(t *testing.T) {
			smallPeak, smallN := heapDuring(t, filepath.Join(dir, "small", f.name), f.cfg)
			largePeak, largeN := heapDuring(t, filepath.Join(dir, "large", f.name), f.cfg)

			t.Logf("%s: %d records -> %d KB peak heap; %d records -> %d KB peak heap",
				f.name, smallN, smallPeak/1024, largeN, largePeak/1024)

			require.Equal(t, int64(20000), smallN)
			require.Equal(t, int64(400000), largeN)

			// Twenty times the input. Anything approaching twenty
			// times the memory means the parser is buffering.
			assert.Less(t, largePeak, smallPeak*2+(4<<20),
				"PERF-026: peak heap grew with input size (%d KB to %d KB)",
				smallPeak/1024, largePeak/1024)
		})
	}
}

func TestMemoryStaysUnderTheBound(t *testing.T) {
	if testing.Short() {
		t.Skip("memory measurement is not a short test")
	}
	// PERF-026's absolute bound is 64 MiB for a 10 GB input. Generating
	// 10 GB here would take minutes and 10 GB of disk, so this measures a
	// smaller corpus against the same bound: the parser holds one buffer
	// and one record, so if it is under 64 MiB at 400 000 records it is
	// under 64 MiB at any size. The scaling test above is what justifies
	// that inference.
	dir := t.TempDir()
	m, err := gen.Write(dir, gen.Config{Seed: 2, Records: 400000})
	require.NoError(t, err)
	require.Greater(t, m.TotalSize, int64(500<<20),
		"the corpus should be large enough for the bound to mean something")

	peak, n := heapDuring(t, filepath.Join(dir, "postgresql-pg14.csv"),
		pglogwatch.Config{Format: pglogwatch.FormatCSV})
	t.Logf("%d records, peak heap %d KB, bound 64 MiB", n, peak/1024)

	assert.Less(t, peak, uint64(64<<20),
		"PERF-026: peak heap %d KB exceeds the 64 MiB bound", peak/1024)
}
