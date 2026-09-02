package pglogwatch

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shard plan, and the two bugs that hid behind it.
//
// planShards used to clamp the parts per source to the worker count, which had
// two consequences beyond the measurement problem it was changed for. It kept
// shard counts low, so a boundary rarely landed inside a multi-line record and
// the loss below went unseen; and it made the shard count a property of
// --jobs, so no test could sweep it independently. Both are fixed by planning
// from the input, and both are pinned here.

// TestPlanShardsIsIndependentOfTheWorkerCount is the executable form of the
// AC-019 measurement fix: --jobs 1 and --jobs 8 must divide a corpus
// identically and differ only in how many goroutines consume it.
//
// The signature carries most of the guarantee -- planShards takes no worker
// count, so it cannot consult one -- but the property is the point, and a
// future change that reintroduced the coupling through cfg or a global would
// compile.
func TestPlanShardsIsIndependentOfTheWorkerCount(t *testing.T) {
	srcs := []io.ReaderAt{
		bytes.NewReader(bigLog(t, shardedLogRecords)),
		bytes.NewReader(bigLog(t, 100)),
		bytes.NewReader(nil),
	}
	want := planShards(srcs)
	require.Greater(t, len(want), 1, "the fixture must plan into several shards")
	assert.Equal(t, want, planShards(srcs))
}

func TestPlanShardsCoversEveryByteOnce(t *testing.T) {
	// The plan is what the [start, end) ownership rule is stated over. A gap
	// loses every record in it and an overlap doubles them, and both produce
	// a count that is nearly right.
	big := bigLog(t, shardedLogRecords)
	small := bigLog(t, 10) // under minShardBytes

	for _, c := range []struct {
		name string
		data []byte
		want int // -1: do not check the count
	}{
		{"several shards", big, -1},
		{"under minShardBytes goes whole", small, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			size := int64(len(c.data))
			shards := planShards([]io.ReaderAt{bytes.NewReader(c.data)})
			if c.want >= 0 {
				require.Len(t, shards, c.want)
			}
			require.NotEmpty(t, shards)

			assert.Zero(t, shards[0].start, "the first shard must start at 0")
			assert.EqualValues(t, -1, shards[len(shards)-1].end,
				"the last shard must run to the end, or a remainder is stranded")
			for i := range shards[:len(shards)-1] {
				assert.Equal(t, shards[i].end, shards[i+1].start,
					"shard %d ends where shard %d must begin", i, i+1)
				assert.GreaterOrEqual(t, shards[i].end-shards[i].start,
					int64(minShardBytes),
					"shard %d is below the floor that makes splitting worthwhile", i)
			}
			assert.Less(t, shards[len(shards)-1].start, size)
		})
	}
}

func TestPlanShardsEdgeCases(t *testing.T) {
	t.Run("an empty source produces no shards", func(t *testing.T) {
		assert.Empty(t, planShards([]io.ReaderAt{bytes.NewReader(nil)}))
	})
	t.Run("a source of unknown size is not split", func(t *testing.T) {
		// Without a length there is nowhere to cut, so one whole shard is
		// the correct fallback rather than an error.
		shards := planShards([]io.ReaderAt{unsizedReaderAt{bytes.NewReader(bigLog(t, 5000))}})
		require.Len(t, shards, 1)
		assert.EqualValues(t, unknownSize, shards[0].size)
		assert.EqualValues(t, -1, shards[0].end)
	})
}

// unsizedReaderAt hides Size and Stat, so sizeOf cannot measure it.
type unsizedReaderAt struct{ r io.ReaderAt }

func (u unsizedReaderAt) ReadAt(p []byte, off int64) (int, error) { return u.r.ReadAt(p, off) }

// TestParallelScanKeepsMultiLineCSVRecords is the regression for a silent loss
// that survived to v1.0.0.
//
// Resynchronisation asked looksLikeCSVLine whether a line began a record, but
// that function asks whether a line IS one -- a known column count, a severity
// in column 12. The first physical line of a record whose message contains a
// newline ends inside an open quote and fails it, so resync discarded the
// record it had just found. The shard before it had already stopped at that
// offset expecting this one to take it, so the record was simply gone.
//
// Nothing caught it because the loss scales with the SHARD count, and the shard
// count used to be clamped to the worker count: the suite never made enough
// shards. Sweeping the input size is what exposes it, so that is what this
// does rather than sweeping --jobs.
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

// TestParallelScanRejectsABadLinePrefix pins a second silent failure.
//
// New reports an unparseable log_line_prefix through Err and stops. It sets
// that error before assigning p.prefix, though, and scanShard's Reset clears
// err and done -- so every worker started with no prefix and quietly
// auto-detected one instead. A serial Parser refused the same Config and read
// nothing; ParallelScan read the whole file and returned nil.
func TestParallelScanRejectsABadLinePrefix(t *testing.T) {
	data := []byte(strings.Repeat(
		"2026-08-30 10:11:12.123 CEST [1] LOG:  hello\n", 100))
	cfg := Config{Format: FormatStderr, LinePrefix: "%z "}

	p := New(bytes.NewReader(data), cfg)
	for p.Next() {
	}
	require.Error(t, p.Err(), "a serial Parser must reject this Config")

	var got int
	err := ParallelScan(t.Context(), []io.ReaderAt{bytes.NewReader(data)}, cfg, 4,
		func(int, *Record) error { got++; return nil })
	assert.Error(t, err, "ParallelScan must reject what a Parser rejects")
	assert.Zero(t, got, "no record may be delivered under a Config that was refused")
}

// TestParallelScanWorkersDoNotShareABuffer.
//
// Each worker builds its own parser on its own goroutine. An earlier version
// carved all of them out of one slab, which was tidier and 1.8x slower on a
// two-node NUMA machine: the slab lands on whichever node allocated it, and a
// worker's read buffer is the hottest memory in the scan. This pins the
// property that replaced it -- separate buffers, one per worker -- because a
// shared or overlapping buffer is silent corruption the race detector cannot
// see.
func TestParallelScanWorkersDoNotShareABuffer(t *testing.T) {
	var norm Config
	norm.normalize()

	seen := map[*byte]int{}
	for i := range 8 {
		p := newParser(nil, Config{Format: FormatJSON}, nil)
		require.Len(t, p.buf.data, norm.InitialBufferBytes, "worker %d", i)
		require.Equal(t, norm.InitialBufferBytes, cap(p.buf.data),
			"worker %d: capacity past its own buffer lets fill read into "+
				"whatever follows", i)
		seen[&p.buf.data[0]]++
	}
	assert.Len(t, seen, 8, "every worker must get its own buffer")
}

func TestWorkerPrefixRejectsABadPrefix(t *testing.T) {
	_, err := workerPrefix(Config{LinePrefix: "%z "})
	assert.Error(t, err)

	tpl, err := workerPrefix(Config{})
	assert.NoError(t, err)
	assert.Nil(t, tpl, "no configured prefix means detection, not an error")
}
