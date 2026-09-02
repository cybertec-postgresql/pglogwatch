package pglogwatch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParallelScan (IFC-008, PERF-029, AC-019).
//
// The whole risk of parallel scanning is at the shard boundaries: a record
// that straddles one must be processed by exactly one worker. Losing it and
// processing it twice are both silent, and both produce counts that are nearly
// right, which is the hardest kind of wrong to notice.

// shardedLogRecords is a record count whose bigLog output spans several
// shards, so that the tests below actually cross a boundary.
//
// It has to be stated rather than assumed. planShards sizes shards from the
// INPUT and no longer from the worker count, so a fixture that used to be cut
// eight ways for eight workers is now cut by its own length -- and one that
// fell under a single shard would keep passing while covering none of the
// boundary behaviour it was written for. At roughly 44 bytes a record this is
// about 1.7 MB, or seven shards.
const shardedLogRecords = 40000

// bigLog builds a log large enough that sharding it is meaningful, with each
// record carrying its own index so duplicates and gaps are detectable.
func bigLog(t *testing.T, n int) []byte {
	t.Helper()
	var sb strings.Builder
	for i := range n {
		sb.WriteString(`{"error_severity":"LOG","message":"record-`)
		sb.WriteString(itoaTest(i))
		sb.WriteString(`"}` + "\n")
	}
	return []byte(sb.String())
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// collect runs ParallelScan and returns every message it saw, plus which
// worker saw it.
func collect(t *testing.T, srcs []io.ReaderAt, cfg Config, workers int) ([]string, map[string]int) {
	t.Helper()
	var mu sync.Mutex
	var msgs []string
	byWorker := map[string]int{}

	err := ParallelScan(t.Context(), srcs, cfg, workers, func(worker int, r *Record) error {
		mu.Lock()
		defer mu.Unlock()
		m := string(r.Message)
		msgs = append(msgs, m)
		byWorker[m] = worker
		return nil
	})
	require.NoError(t, err)
	return msgs, byWorker
}

func TestParallelScanSeesEveryRecordExactlyOnce(t *testing.T) {
	// IFC-008's core obligation. Checked at several worker counts, because a
	// boundary bug can hide at one shard count and appear at another.
	const n = shardedLogRecords
	data := bigLog(t, n)

	for _, workers := range []int{1, 2, 3, 4, 8, 16} {
		t.Run("workers="+itoaTest(workers), func(t *testing.T) {
			msgs, _ := collect(t, []io.ReaderAt{bytes.NewReader(data)},
				Config{Format: FormatJSON}, workers)

			require.Len(t, msgs, n, "wrong number of records with %d workers", workers)
			seen := make(map[string]int, n)
			for _, m := range msgs {
				seen[m]++
			}
			// Report the first few offenders rather than asserting once
			// per record: at this size that is 40000 testify calls per
			// worker count, and forty thousand identical failure lines
			// are harder to read than one naming the boundary.
			var wrong []string
			for i := range n {
				m := "record-" + itoaTest(i)
				if seen[m] != 1 {
					if len(wrong) < 5 {
						wrong = append(wrong,
							m+" seen "+itoaTest(seen[m])+" times")
					}
				}
			}
			assert.Empty(t, wrong, "records lost or doubled at a shard boundary")
		})
	}
}
func TestParallelScanMatchesASingleParser(t *testing.T) {
	// The strongest statement of the same thing: the multiset of records is
	// identical to what one parser produces.
	data := bigLog(t, shardedLogRecords)

	p := New(bytes.NewReader(data), Config{Format: FormatJSON})
	var want []string
	for p.Next() {
		want = append(want, string(p.Record().Message))
	}
	require.NoError(t, p.Err())

	got, _ := collect(t, []io.ReaderAt{bytes.NewReader(data)}, Config{Format: FormatJSON}, 8)
	slices.Sort(want)
	slices.Sort(got)
	assert.Equal(t, want, got)
}
func TestParallelScanAcrossSeveralSources(t *testing.T) {
	// The multi-file workload PERF-029 is about.
	var srcs []io.ReaderAt
	total := 0
	for i := range 8 {
		n := 100 + i*37
		srcs = append(srcs, bytes.NewReader(bigLog(t, n)))
		total += n
	}
	msgs, _ := collect(t, srcs, Config{Format: FormatJSON}, 4)
	assert.Len(t, msgs, total)
}
func TestParallelScanPropagatesAnError(t *testing.T) {
	// A callback error stops the scan and is returned. A collector that
	// cannot store a record has no reason to keep parsing.
	want := errors.New("sink is full")
	var count int
	var mu sync.Mutex

	err := ParallelScan(t.Context(), []io.ReaderAt{bytes.NewReader(bigLog(t, 5000))},
		Config{Format: FormatJSON}, 4,
		func(_ int, _ *Record) error {
			mu.Lock()
			defer mu.Unlock()
			count++
			if count == 10 {
				return want
			}
			return nil
		})
	assert.ErrorIs(t, err, want)
}
func TestParallelScanStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var mu sync.Mutex
	n := 0

	err := ParallelScan(ctx, []io.ReaderAt{bytes.NewReader(bigLog(t, 20000))},
		Config{Format: FormatJSON}, 4,
		func(_ int, _ *Record) error {
			mu.Lock()
			defer mu.Unlock()
			n++
			if n == 50 {
				cancel()
			}
			return nil
		})
	assert.ErrorIs(t, err, context.Canceled)
}
func TestParallelScanEmptyInput(t *testing.T) {
	msgs, _ := collect(t, []io.ReaderAt{bytes.NewReader(nil)}, Config{Format: FormatJSON}, 4)
	assert.Empty(t, msgs)

	msgs, _ = collect(t, nil, Config{Format: FormatJSON}, 4)
	assert.Empty(t, msgs)
}
func TestParallelScanWorkerCountIsClamped(t *testing.T) {
	// Zero or negative means "decide for me" rather than "do nothing".
	data := bigLog(t, 100)
	for _, workers := range []int{0, -1} {
		msgs, _ := collect(t, []io.ReaderAt{bytes.NewReader(data)},
			Config{Format: FormatJSON}, workers)
		assert.Len(t, msgs, 100, "workers=%d must still scan everything", workers)
	}
}
func TestParallelScanOnFiles(t *testing.T) {
	// os.File is the io.ReaderAt callers will actually pass.
	dir := t.TempDir()
	var srcs []io.ReaderAt
	for i := range 4 {
		path := filepath.Join(dir, "log-"+itoaTest(i)+".json")
		require.NoError(t, os.WriteFile(path, bigLog(t, 250), 0o600))
		f, err := os.Open(path) //nolint:gosec // test fixture
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })
		srcs = append(srcs, f)
	}
	msgs, _ := collect(t, srcs, Config{Format: FormatJSON}, 4)
	assert.Len(t, msgs, 1000)
}
func TestParallelScanRecordsAreUsableInsideTheCallback(t *testing.T) {
	// Each worker has its own Parser (PERF-012), so the Record passed to fn
	// is that worker's own and is valid for the duration of the call.
	data := bigLog(t, 500)
	err := ParallelScan(t.Context(), []io.ReaderAt{bytes.NewReader(data)},
		Config{Format: FormatJSON}, 4,
		func(_ int, r *Record) error {
			if !bytes.HasPrefix(r.Message, []byte("record-")) {
				return errors.New("record contents were not valid inside the callback")
			}
			if r.Severity != SeverityLog {
				return errors.New("severity was not parsed")
			}
			return nil
		})
	require.NoError(t, err)
}
