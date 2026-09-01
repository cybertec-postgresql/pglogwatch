package pglogwatch

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PERF-012 says a Parser is not safe for concurrent use. ParallelScan is the
// sanctioned way to get parallelism, so it must give each worker its own --
// and that has to be demonstrated under the race detector rather than
// asserted in a doc comment.
//
// Run as part of `task test-race`; the detector is what makes these tests
// meaningful, and without it they merely check the counts again.

func TestParallelScanIsRaceFree(t *testing.T) {
	data := bigLog(t, 4000)
	srcs := []io.ReaderAt{
		bytes.NewReader(data),
		bytes.NewReader(data),
		bytes.NewReader(data),
	}

	var total atomic.Int64
	err := ParallelScan(t.Context(), srcs, Config{Format: FormatJSON}, 8,
		func(_ int, r *Record) error {
			// Touch fields from every part of the record, so a shared
			// parser or a shared buffer shows up as a data race
			// rather than only as a wrong count.
			total.Add(int64(len(r.Message) + len(r.Raw) + int(r.Severity)))
			return nil
		})
	require.NoError(t, err)
	assert.Positive(t, total.Load())
}

func TestParallelScanWorkersDoNotShareARecord(t *testing.T) {
	// Each worker's Record must be its own. If two workers shared one, the
	// contents seen inside a callback would change under it, and the worker
	// index carried alongside would stop matching the data.
	data := bigLog(t, 3000)

	var mu sync.Mutex
	perWorker := map[int]int{}

	err := ParallelScan(t.Context(), []io.ReaderAt{bytes.NewReader(data)},
		Config{Format: FormatJSON}, 8,
		func(worker int, r *Record) error {
			// Re-read the same field twice with work in between. A
			// shared record would sometimes differ.
			first := string(r.Message)
			_ = r.Clone()
			second := string(r.Message)
			if first != second {
				return errMalformedRecordChanged
			}
			mu.Lock()
			perWorker[worker]++
			mu.Unlock()
			return nil
		})
	require.NoError(t, err)

	sum := 0
	for _, n := range perWorker {
		sum += n
	}
	assert.Equal(t, 3000, sum)
}

var errMalformedRecordChanged = &parseError{msg: "record contents changed during the callback"}

func TestParallelScanConcurrentCallsAreIndependent(t *testing.T) {
	// Two ParallelScan calls at once must not interfere. A caller polling
	// several servers does exactly this.
	data := bigLog(t, 1500)

	var wg sync.WaitGroup
	counts := make([]atomic.Int64, 4)
	for i := range 4 {
		wg.Go(func() {
			err := ParallelScan(t.Context(), []io.ReaderAt{bytes.NewReader(data)},
				Config{Format: FormatJSON}, 4,
				func(_ int, _ *Record) error {
					counts[i].Add(1)
					return nil
				})
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	for i := range counts {
		assert.Equal(t, int64(1500), counts[i].Load(), "scan %d", i)
	}
}
