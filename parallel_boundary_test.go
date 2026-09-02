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
			data := bytes.Repeat(one, 200)
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
	// The input has to be big enough to be worth splitting: shards below
	// minShardBytes are not created, because coordinating them costs more
	// than the parsing they save. 20000 records is comfortably past that
	// for four workers.
	data := bigLog(t, 20000)
	_, byWorker := collect(t, []io.ReaderAt{bytes.NewReader(data)},
		Config{Format: FormatJSON}, 4)

	used := map[int]bool{}
	for _, w := range byWorker {
		used[w] = true
	}
	assert.Len(t, used, 4, "every worker must receive some records")
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
			data := bytes.Repeat(fixture(t, c.file), 400)

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
// It sweeps the WORKER count because that is what planShards derives the shard
// count from. The loss needs a boundary to land inside a record whose message
// spans lines, so it appears only once there are enough shards: at eight it
// does not reproduce, at twenty-six it drops six records.
func TestParallelScanKeepsMultiLineCSVRecords(t *testing.T) {
	one := fixture(t, "csv/quotes-newlines-commas.csv")
	require.Contains(t, string(one), "\nFROM t",
		"the fixture must contain a record whose message spans lines, "+
			"or this test cannot see the bug it exists for")

	data := bytes.Repeat(one, 2000)
	cfg := Config{Format: FormatCSV}

	p := New(bytes.NewReader(data), cfg)
	want := 0
	for p.Next() {
		want++
	}
	require.NoError(t, p.Err())
	require.Positive(t, want)

	for _, workers := range []int{1, 2, 4, 8, 16, 26, 32} {
		got, _ := collect(t, []io.ReaderAt{bytes.NewReader(data)}, cfg, workers)
		assert.Len(t, got, want,
			"%d workers: parallel scanning lost or doubled a record that spans lines",
			workers)
	}
}
