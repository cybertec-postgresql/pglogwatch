package pglogwatch

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Record.Offset and Record.Raw carry the contract that byte-offset resumption
// rests on (IFC-006, AC-025): Offset is where the record starts in the stream,
// and Raw is the record found there. If those two ever disagree, a resumed
// scan silently starts mid-record, which is the kind of bug that shows up as
// missing log events weeks later.

func TestRawAndOffsetAgreeForEveryRecordShape(t *testing.T) {
	cases := []struct {
		name string
		file string
	}{
		{"single-line records", "csv/pg14-basic.csv"},
		{"a record spanning three physical lines", "csv/quotes-newlines-commas.csv"},
		{"a record with no trailing newline", "csv/truncated-tail.csv"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := fixture(t, c.file)
			p := New(bytes.NewReader(raw), Config{Format: FormatCSV})
			var last int64 = -1
			n := 0
			for p.Next() {
				r := p.Record()
				assert.Greater(t, r.Offset, last, "offsets must strictly increase")
				last = r.Offset
				require.LessOrEqual(t, int(r.Offset), len(raw))
				assert.True(t, bytes.HasPrefix(raw[r.Offset:], r.Raw),
					"record %d: Raw is not the bytes at Offset", n)
				n++
			}
			require.NoError(t, p.Err())
			assert.NotZero(t, n)
		})
	}
}

func TestRawExcludesTheRecordSeparator(t *testing.T) {
	// Raw is the record, not the record plus its terminator. Callers slice
	// it, print it and hash it; a trailing newline would leak into all
	// three.
	p := New(bytes.NewReader(fixture(t, "csv/pg14-basic.csv")), Config{Format: FormatCSV})
	require.True(t, p.Next())
	raw := p.Record().Raw
	assert.NotContains(t, string(raw[len(raw)-1:]), "\n")
}

func TestRawExcludesCarriageReturn(t *testing.T) {
	// COR-006 for Raw specifically: on a CRLF log the carriage return is
	// part of the line ending, not of the record.
	p := New(bytes.NewReader(fixture(t, "csv/crlf.csv")), Config{Format: FormatCSV})
	require.True(t, p.Next())
	raw := p.Record().Raw
	assert.False(t, bytes.HasSuffix(raw, []byte("\r")))
	// Offset still indexes the true stream position, so the next record's
	// offset accounts for both bytes of the CRLF.
	first := p.Record().Offset
	require.True(t, p.Next())
	assert.Equal(t, first+int64(len(raw))+2, p.Record().Offset,
		"the offset of the next record must skip both CR and LF")
}

func TestOffsetsAreStreamRelativeNotBufferRelative(t *testing.T) {
	// The offset must survive buffer refills and compaction, which is
	// exactly where a buffer-relative implementation would go wrong.
	one := string(fixture(t, "csv/pg14-basic.csv"))
	in := strings.Repeat(one, 30)
	p := New(strings.NewReader(in), Config{Format: FormatCSV, InitialBufferBytes: 512})

	var offsets []int64
	for p.Next() {
		offsets = append(offsets, p.Record().Offset)
	}
	require.NoError(t, p.Err())
	require.Len(t, offsets, 180)
	// Many times the buffer size, so the stream is refilled and compacted
	// repeatedly during the scan -- which is where a buffer-relative
	// offset would drift.
	require.Greater(t, len(in), 512*8, "the test needs many refills to be meaningful")

	// Every 6th record starts a fresh copy of the fixture, so its offset is
	// a whole number of fixture lengths in.
	for i := 0; i < len(offsets); i += 6 {
		assert.Equal(t, int64(i/6*len(one)), offsets[i], "record %d", i)
	}
}
