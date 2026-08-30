package pglogwatch

import (
	"bytes"
	"io"
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
	data := bigLog(t, 5000)
	_, byWorker := collect(t, []io.ReaderAt{bytes.NewReader(data)},
		Config{Format: FormatJSON}, 4)

	used := map[int]bool{}
	for _, w := range byWorker {
		used[w] = true
	}
	assert.Len(t, used, 4, "every worker must receive some records")
}
