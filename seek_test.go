package pglogwatch

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Parser.Seek (IFC-006, AC-025, §7.6).
//
// This is what makes resumption O(1). The implementation this module replaces
// resumed by calling ReadString in a loop as many times as it had previously
// read lines -- re-reading the whole file after every restart. The tests below
// are mostly about the awkward part: an offset need not land on a record
// boundary, and the parser has to recover from that without inventing records.

// recordOffsets parses a stream and returns every record's offset and message.
func recordOffsets(t *testing.T, data []byte, cfg Config) ([]int64, []string) {
	t.Helper()
	p := New(bytes.NewReader(data), cfg)
	var offs []int64
	var msgs []string
	for p.Next() {
		offs = append(offs, p.Record().Offset)
		msgs = append(msgs, string(p.Record().Message))
	}
	require.NoError(t, p.Err())
	return offs, msgs
}

func TestSeekResumesExactlyAtARecordBoundary(t *testing.T) {
	// AC-025's core promise: resuming at a recorded offset counts nothing
	// twice and skips nothing.
	for _, c := range []struct {
		name   string
		file   string
		format Format
	}{
		{"csvlog", "csv/pg14-basic.csv", FormatCSV},
		{"jsonlog", "json/basic.json", FormatJSON},
		{"stderr", "stderr/basic.log", FormatStderr},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := fixture(t, c.file)
			cfg := Config{Format: c.format}
			offs, msgs := recordOffsets(t, data, cfg)
			require.Greater(t, len(offs), 2)

			for i := range offs {
				p := New(bytes.NewReader(data), cfg)
				_, err := p.Seek(offs[i], io.SeekStart)
				require.NoError(t, err)

				var got []string
				for p.Next() {
					got = append(got, string(p.Record().Message))
				}
				require.NoError(t, p.Err())
				assert.Equal(t, msgs[i:], got,
					"seeking to record %d must resume exactly there", i)
			}
		})
	}
}

func TestSeekFromAnArbitraryOffsetResynchronises(t *testing.T) {
	// The offset need not be a boundary. Seeking into the middle of a
	// record must discard that record's tail and continue from the next
	// one -- never emit the tail as if it were a record.
	data := fixture(t, "json/basic.json")
	_, msgs := recordOffsets(t, data, Config{Format: FormatJSON})

	for off := range int64(len(data)) {
		p := New(bytes.NewReader(data), Config{Format: FormatJSON})
		_, err := p.Seek(off, io.SeekStart)
		require.NoError(t, err)

		var got []string
		for p.Next() {
			got = append(got, string(p.Record().Message))
		}
		require.NoError(t, p.Err())

		// Whatever survived must be a suffix of the full record list:
		// no invented records, no partial ones, no reordering.
		require.LessOrEqual(t, len(got), len(msgs), "offset %d produced extra records", off)
		// append onto a nil slice so an empty suffix compares as nil,
		// which is what the loop above produces when nothing survives.
		want := append([]string(nil), msgs[len(msgs)-len(got):]...)
		assert.Equal(t, want, got, "offset %d", off)
	}
}

func TestSeekIntoAMultiLineRecord(t *testing.T) {
	// Landing inside a multi-line record is where a naive "skip to the next
	// newline" resync fails: the next newline is still inside the record,
	// and its remaining lines are not records. The parser must skip lines
	// until one actually starts a record.
	data := fixture(t, "csv/quotes-newlines-commas.csv")
	cfg := Config{Format: FormatCSV}
	offs, msgs := recordOffsets(t, data, cfg)
	require.Len(t, offs, 4)

	// Record 1 is the one spanning three physical lines. Seek into the
	// middle of it.
	mid := offs[1] + 40
	require.Less(t, mid, offs[2])

	p := New(bytes.NewReader(data), cfg)
	_, err := p.Seek(mid, io.SeekStart)
	require.NoError(t, err)
	var got []string
	for p.Next() {
		got = append(got, string(p.Record().Message))
	}
	require.NoError(t, p.Err())
	assert.Equal(t, msgs[2:], got,
		"the remaining lines of a straddled record must not become records")
}

func TestSeekOffsetsAreStreamAbsoluteAfterSeek(t *testing.T) {
	// Record.Offset after a Seek must still be the position in the FILE,
	// not the distance from the seek point. Otherwise a resumed scan would
	// persist offsets that mean nothing on the next restart -- which is
	// the bug that makes offset-based resumption drift.
	data := fixture(t, "json/basic.json")
	offs, _ := recordOffsets(t, data, Config{Format: FormatJSON})
	require.Len(t, offs, 3)

	p := New(bytes.NewReader(data), Config{Format: FormatJSON})
	_, err := p.Seek(offs[1], io.SeekStart)
	require.NoError(t, err)
	require.True(t, p.Next())
	assert.Equal(t, offs[1], p.Record().Offset)
	require.True(t, p.Next())
	assert.Equal(t, offs[2], p.Record().Offset)
}

func TestSeekToZeroReadsEverything(t *testing.T) {
	data := fixture(t, "json/basic.json")
	_, msgs := recordOffsets(t, data, Config{Format: FormatJSON})

	p := New(bytes.NewReader(data), Config{Format: FormatJSON})
	_, err := p.Seek(0, io.SeekStart)
	require.NoError(t, err)
	var got []string
	for p.Next() {
		got = append(got, string(p.Record().Message))
	}
	require.NoError(t, p.Err())
	assert.Equal(t, msgs, got)
}

func TestSeekPastEndIsClean(t *testing.T) {
	data := fixture(t, "json/basic.json")
	p := New(bytes.NewReader(data), Config{Format: FormatJSON})
	_, err := p.Seek(int64(len(data))+1000, io.SeekStart)
	require.NoError(t, err)
	assert.False(t, p.Next())
	assert.NoError(t, p.Err())
}

func TestSeekOnANonSeekableReader(t *testing.T) {
	// A pipe, a network connection or a decompressing reader cannot seek,
	// and saying so plainly is more useful than reading and discarding
	// gigabytes to simulate it.
	p := New(&countingReader{r: strings.NewReader("{}\n")}, Config{Format: FormatJSON})
	_, err := p.Seek(10, io.SeekStart)
	assert.ErrorIs(t, err, ErrNotSeekable)
	_, err = p.Seek(-1, io.SeekStart)
	assert.ErrorIs(t, err, ErrNotSeekable)
}

func TestSeekKeepsDetectionState(t *testing.T) {
	// Format and prefix belong to the FILE, not to a position in it.
	// Re-detecting from the middle would be both wasteful and less
	// reliable, since a mid-file sample may start with a continuation line.
	data := fixture(t, "stderr/basic.log")
	p := New(bytes.NewReader(data), Config{})
	require.True(t, p.Next())
	format, prefix := p.DetectedFormat(), p.DetectedPrefix()
	require.Equal(t, FormatStderr, format)
	require.NotEmpty(t, prefix)

	_, err := p.Seek(int64(len(data)/2), io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, format, p.DetectedFormat())
	assert.Equal(t, prefix, p.DetectedPrefix())
}

func TestSeekRejectsBadArguments(t *testing.T) {
	// Argument errors are distinguishable from ErrNotSeekable: the reader
	// here can seek perfectly well, and reporting otherwise would send a
	// caller looking at the wrong thing.
	data := fixture(t, "json/basic.json")
	p := New(bytes.NewReader(data), Config{Format: FormatJSON})

	_, err := p.Seek(-1, io.SeekStart)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotSeekable)

	_, err = p.Seek(-int64(len(data))-1, io.SeekEnd)
	require.Error(t, err)

	_, err = p.Seek(0, 42)
	require.Error(t, err)
}

func TestSeekWhenceCurrentIsRelativeToTheParser(t *testing.T) {
	// The buffer reads ahead, so the underlying reader sits well past the
	// parser's own position. Resolving SeekCurrent against the READER would
	// land arbitrarily far along; it has to resolve against consumed.
	data := fixture(t, "json/basic.json")
	offs, msgs := recordOffsets(t, data, Config{Format: FormatJSON})
	require.Greater(t, len(offs), 2)

	p := New(bytes.NewReader(data), Config{Format: FormatJSON})
	require.True(t, p.Next()) // consumes record 0; parser now sits at offs[1]

	got, err := p.Seek(offs[2]-offs[1], io.SeekCurrent)
	require.NoError(t, err)
	assert.Equal(t, offs[2], got)

	require.True(t, p.Next())
	assert.Equal(t, msgs[2], string(p.Record().Message))
}

func TestSeekWhenceEndLandsAtEndOfInput(t *testing.T) {
	data := fixture(t, "json/basic.json")
	p := New(bytes.NewReader(data), Config{Format: FormatJSON})

	got, err := p.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), got)
	assert.False(t, p.Next())
	assert.NoError(t, p.Err())
}

func TestSeekReturnsTheResynchronisedOffset(t *testing.T) {
	// The returned offset is where the parser LANDED, not what was asked
	// for. That is the whole value of the return: a caller seeking to an
	// approximate position learns the real boundary.
	data := fixture(t, "json/basic.json")
	offs, msgs := recordOffsets(t, data, Config{Format: FormatJSON})
	require.Greater(t, len(offs), 2)

	mid := offs[1] + (offs[2]-offs[1])/2 // deliberately inside record 1
	require.Greater(t, mid, offs[1])

	p := New(bytes.NewReader(data), Config{Format: FormatJSON})
	got, err := p.Seek(mid, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, offs[2], got, "resync should report the boundary it found")

	require.True(t, p.Next())
	assert.Equal(t, msgs[2], string(p.Record().Message))
	assert.Equal(t, offs[2], p.Record().Offset)
}

// TestParserSatisfiesIOSeeker is the point of the signature: a Parser can be
// handed to anything that takes an io.Seeker.
func TestParserSatisfiesIOSeeker(t *testing.T) {
	var _ io.Seeker = (*Parser)(nil)
}

// TestSeekDoesNotSkipAMultiLineCSVRecord is a regression for a silent loss.
//
// Resynchronisation asked looksLikeCSVLine whether a line began a record, but
// that function answers a different question -- whether a line IS a whole
// record, with a known column count and a severity in column 12. The first
// physical line of a record whose message contains a newline ends inside an
// open quote and fails it, so resync stepped over the record it had just
// found and landed on the next one.
//
// Alone that is a resumption that loses one record. Under ParallelScan it is
// worse: the shard before this offset stopped here believing the next shard
// would take the record, so nothing reads it at all and the total is quietly
// one short per boundary that lands in a multi-line record.
func TestSeekDoesNotSkipAMultiLineCSVRecord(t *testing.T) {
	data := bytes.Repeat(fixture(t, "csv/quotes-newlines-commas.csv"), 8)
	cfg := Config{Format: FormatCSV}
	offs, msgs := recordOffsets(t, data, cfg)

	// Find a record whose text spans physical lines; that is the one whose
	// first line the old test rejected.
	idx := -1
	for i := range offs[:len(offs)-1] {
		if bytes.Contains(data[offs[i]:offs[i+1]], []byte("\nFROM t")) {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 1,
		"the fixture must hold a record spanning lines, preceded by another")

	// Seek into the record BEFORE it, which is what a shard boundary landing
	// mid-record does.
	mid := offs[idx-1] + (offs[idx]-offs[idx-1])/2
	require.Greater(t, mid, offs[idx-1])

	p := New(bytes.NewReader(data), cfg)
	got, err := p.Seek(mid, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, offs[idx], got,
		"resync must land on the multi-line record, not step over it")

	require.True(t, p.Next())
	assert.Equal(t, msgs[idx], string(p.Record().Message))
}
