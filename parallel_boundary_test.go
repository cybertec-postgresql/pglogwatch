package pglogwatch

import (
	"bytes"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shard-boundary behaviour (IFC-008).
//
// A record that straddles a shard boundary must be processed by exactly one
// worker. Both failures -- losing it and processing it twice -- are silent and
// produce counts that are nearly right, which is the hardest kind of wrong to
// notice in a log analyser: nobody audits a severity histogram by hand.

// shardedBytes is how much input the boundary tests build. planShards cuts at
// targetShardBytes, so this is several shards whatever the fixture's own size.
const shardedBytes = 4 << 20

func TestParallelScanAcrossAllFormats(t *testing.T) {
	// Each format frames records differently, so each has its own boundary
	// behaviour. csvlog is the interesting one: its records can span lines,
	// so a shard boundary can land inside one.
	for _, c := range []struct {
		name   string
		file   string
		format Format
	}{
		{"csvlog", "csv/pg14-basic.csv", FormatCSV},
		{"csvlog with multi-line records", "csv/quotes-newlines-commas.csv", FormatCSV},
		{"jsonlog", "json/basic.json", FormatJSON},
		{"stderr", "stderr/basic.log", FormatStderr},
		{"stderr with continuations", "stderr/multiline.log", FormatStderr},
	} {
		t.Run(c.name, func(t *testing.T) {
			one := fixture(t, c.file)
			// A fixed repeat count gave each format a different size,
			// and the smaller ones now fall inside a single shard --
			// which would leave the csvlog multi-line case, the only
			// coverage of a record straddling a boundary, testing
			// nothing at all. Repeat to a SIZE instead.
			data := bytes.Repeat(one, max(1, shardedBytes/len(one)))
			cfg := Config{Format: c.format}

			p := New(bytes.NewReader(data), cfg)
			want := 0
			for p.Next() {
				want++
			}
			require.NoError(t, p.Err())
			require.Positive(t, want)

			msgs, _ := collect(t, []io.ReaderAt{bytes.NewReader(data)}, cfg, 8)
			assert.Len(t, msgs, want,
				"parallel scanning must not change the record count")
		})
	}
}
func TestParallelScanUsesEveryWorker(t *testing.T) {
	// Sharding that produced one non-empty shard would pass every
	// correctness test above and deliver no parallelism at all.
	//
	// What is checked here changed with the work queue. Workers now draw
	// shards from a shared cursor instead of being dealt a fixed share, so
	// "every worker received a record" is a scheduling accident and not a
	// contract: a worker that starts promptly can legitimately take two
	// shards while another takes none. Asserting it would be a flaky test
	// dressed as a guarantee.
	//
	// The property that does hold, and that the test exists for, is that the
	// work was divisible and was in fact divided.
	const workers = 4
	data := bigLog(t, shardedLogRecords)
	src := bytes.NewReader(data)

	shards := planShards([]io.ReaderAt{src})
	require.GreaterOrEqual(t, len(shards), workers,
		"the input must plan into at least one shard per worker, or this "+
			"test cannot observe parallelism at all")

	_, byWorker := collect(t, []io.ReaderAt{src}, Config{Format: FormatJSON}, workers)
	used := map[int]bool{}
	for _, w := range byWorker {
		used[w] = true
	}
	assert.Greater(t, len(used), 1, "the shards must be spread over more than one worker")
	assert.LessOrEqual(t, len(used), workers, "no worker index may exceed --jobs")
}

func TestParallelScanDetectsFormatFromTheHead(t *testing.T) {
	// Format detection reads the first non-empty line. At a shard's start
	// that line is almost always a FRAGMENT, so a shard that detected
	// locally would classify a jsonlog file as stderr -- its fragment does
	// not begin with a brace -- and then parse the whole shard wrongly
	// while reporting no errors at all.
	//
	// Every earlier parallel test passed an explicit Format, so none of them
	// could see this. It was found by running the CLI over the generated
	// corpus, where the counts disagreed with the manifest.
	for _, c := range []struct {
		name   string
		file   string
		expect Format
	}{
		{"jsonlog", "json/basic.json", FormatJSON},
		{"csvlog", "csv/pg14-basic.csv", FormatCSV},
		{"stderr", "stderr/basic.log", FormatStderr},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Sized, not counted: this is the test that exists to catch
			// a shard detecting the format from its own start rather
			// than from the head of the file, so it has to produce more
			// than one shard.
			one := fixture(t, c.file)
			data := bytes.Repeat(one, max(1, shardedBytes/len(one)))

			// The count a single parser gets, with detection.
			p := New(bytes.NewReader(data), Config{})
			want := 0
			for p.Next() {
				want++
			}
			require.NoError(t, p.Err())
			require.Equal(t, c.expect, p.DetectedFormat())
			require.Positive(t, want)

			// The same, sharded, with detection left to each worker.
			var mu sync.Mutex
			got := 0
			require.NoError(t, ParallelScan(t.Context(),
				[]io.ReaderAt{bytes.NewReader(data)}, Config{}, 8,
				func(_ int, _ *Record) error {
					mu.Lock()
					got++
					mu.Unlock()
					return nil
				}))
			assert.Equal(t, want, got,
				"auto-detection must give the same record count sharded as serial")
		})
	}
}

// TestParallelScanKeepsMultiLineCSVRecords is the parallel half of the loss
// that TestSeekDoesNotSkipAMultiLineCSVRecord pins at the Seek level, and it
// is the one that matters to a user: a resumption that skips a record loses
// one record, but a shard that skips its first record loses it outright,
// because the previous shard already stopped at that offset expecting this one
// to take it. Nothing reads it, and the scan reports no error.
//
// It sweeps the INPUT SIZE, not the worker count. When this test was written
// planShards derived the shard count from --jobs, so sweeping workers was how
// to reach enough shards for a boundary to land inside a record whose message
// spans lines. It no longer does: the plan is a function of the input alone,
// so every worker count now produces the SAME shards and a worker sweep would
// run one configuration seven times over. Size is the axis that varies them.
//
// The worker sweep is kept underneath, because concurrency is still worth
// varying -- it just no longer varies the thing this bug needs.
func TestParallelScanKeepsMultiLineCSVRecords(t *testing.T) {
	one := fixture(t, "csv/quotes-newlines-commas.csv")
	require.Contains(t, string(one), "\nFROM t",
		"the fixture must contain a record whose message spans lines, "+
			"or this test cannot see the bug it exists for")

	cfg := Config{Format: FormatCSV}
	for _, reps := range []int{2000, 4793, 12000} {
		data := bytes.Repeat(one, reps)

		p := New(bytes.NewReader(data), cfg)
		want := 0
		for p.Next() {
			want++
		}
		require.NoError(t, p.Err())
		require.Positive(t, want)

		shards := planShards([]io.ReaderAt{bytes.NewReader(data)})
		require.Greater(t, len(shards), 4,
			"%d bytes must plan into several shards for this to mean anything",
			len(data))

		for _, workers := range []int{1, 2, 3, 4, 8, 16} {
			got, _ := collect(t, []io.ReaderAt{bytes.NewReader(data)}, cfg, workers)
			assert.Len(t, got, want,
				"%d shards, %d workers: parallel scanning lost or doubled a "+
					"record that spans lines", len(shards), workers)
		}
	}
}
