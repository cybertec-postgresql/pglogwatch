package pglogwatch

import (
	"bytes"
	"io"
)

// The read buffer.
//
// This is bufio.Scanner's split-function model with four changes that the
// specification forces and that Scanner cannot provide:
//
//   - PERF-008 requires a record larger than MaxRecordBytes to be skipped and
//     counted, then parsing to continue. Scanner returns ErrTooLong and stops,
//     which would let one pathological record end a 10 GB scan (E18).
//   - Record.Offset must be the record's byte offset in the stream, so that
//     an OffsetStore can resume with a single Seek (IFC-006). Scanner does not
//     expose how much it has consumed.
//   - Stats.BufferGrows must be observable, because a growth count that never
//     settles is the signal that PERF-001's steady state was never reached.
//   - PERF-011 requires Reset to reuse the buffer, so the storage outlives any
//     one stream.
//
// A token returned by next is a slice into the buffer and is invalidated by
// the following call, which is the borrowing contract PERF-002 states.

// splitFunc reports how the next record ends. It follows bufio.SplitFunc's
// convention so that reading one is enough to understand the other:
// returning (0, nil, nil) asks for more data, and a non-nil token is a record.
type splitFunc func(data []byte, atEOF bool) (advance int, token []byte, err error)

type buf struct {
	src  io.Reader
	data []byte
	r, w int // unconsumed data is data[r:w]

	// consumed is the number of stream bytes before data[r]; it is what
	// Record.Offset is derived from.
	consumed int64

	err      error // first read error, returned after the buffer drains
	atEOF    bool
	max      int
	initial  int
	skipping bool // discarding an over-large record until the next newline

	// bomChecked records that the head of the stream has been inspected for
	// a byte order mark, so the check happens once rather than per record.
	bomChecked bool

	stats *Stats
}

func newBuf(src io.Reader, cfg *Config, stats *Stats) *buf {
	return &buf{
		src:     src,
		data:    make([]byte, cfg.InitialBufferBytes),
		max:     cfg.MaxRecordBytes,
		initial: cfg.InitialBufferBytes,
		stats:   stats,
	}
}

// reset points the buffer at a new stream, keeping its storage (PERF-011).
func (b *buf) reset(src io.Reader) {
	b.src = src
	b.r, b.w = 0, 0
	b.consumed = 0
	b.err = nil
	b.atEOF = false
	b.skipping = false
	b.bomChecked = false
}

// utf8BOM is the byte order mark, which appears at the head of a log that has
// been through a Windows editor or a helpful text pipeline.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// consumeBOM drops a leading byte order mark, once per stream.
//
// Without this the mark becomes three bytes in front of the first record,
// which breaks the timestamp scan and therefore format detection: one
// invisible byte sequence at the head of a file would make the whole log
// unparseable, with nothing in the output to explain why (E17).
//
// The mark is CONSUMED rather than skipped, so Record.Offset stays a true file
// position -- an offset that ignored three bytes at the head would be wrong
// for every record in the file and would resume in the wrong place.
func (b *buf) consumeBOM() {
	if b.bomChecked {
		return
	}
	b.bomChecked = true
	if b.consumed != 0 {
		return // mid-file, after a Seek: nothing here is a file header
	}
	for b.w-b.r < len(utf8BOM) && !b.atEOF {
		if !b.fill() {
			b.atEOF = true
		}
	}
	if bytes.HasPrefix(b.data[b.r:b.w], utf8BOM) {
		b.advance(len(utf8BOM))
	}
}

// next returns the next record, its offset in the stream, and an error.
// io.EOF means the stream is exhausted; every other error is fatal.
func (b *buf) next(split splitFunc) ([]byte, int64, error) {
	b.consumeBOM()
	for {
		if b.skipping {
			if !b.discardToNewline() {
				return nil, 0, b.finalErr()
			}
			b.skipping = false
		}

		if b.r < b.w || b.atEOF {
			advance, token, err := split(b.data[b.r:b.w], b.atEOF)
			if err != nil {
				return nil, 0, err
			}
			if advance > 0 || token != nil {
				off := b.consumed
				b.advance(advance)
				if token != nil {
					return token, off, nil
				}
				continue // record consumed but not emitted
			}
			if b.atEOF {
				return nil, 0, b.finalErr()
			}
		}

		// The split function needs more data than the buffer holds.
		if b.w-b.r >= b.max {
			// PERF-008 / E18: skip the record rather than growing
			// without bound or panicking.
			b.stats.Truncated++
			b.advance(b.w - b.r)
			b.skipping = true
			continue
		}
		if !b.fill() {
			b.atEOF = true
		}
	}
}

// advance consumes n bytes of the unconsumed region.
func (b *buf) advance(n int) {
	b.r += n
	b.consumed += int64(n)
	b.stats.Bytes += int64(n)
}

// finalErr reports io.EOF for a clean end of stream, or the read error that
// ended it. IFC-003 requires Err to be nil at clean EOF, so io.EOF is
// translated by the caller rather than surfaced.
func (b *buf) finalErr() error {
	if b.err != nil && b.err != io.EOF {
		return b.err
	}
	return io.EOF
}

// discardToNewline drops bytes until just past the next newline, reading more
// as needed. It reports false if the stream ended first.
func (b *buf) discardToNewline() bool {
	for {
		if i := indexNewline(b.data[b.r:b.w]); i >= 0 {
			b.advance(i + 1)
			return true
		}
		b.advance(b.w - b.r)
		if !b.fill() {
			b.atEOF = true
			return false
		}
	}
}

// maxEmptyReads bounds how many (0, nil) results this package tolerates from
// a Reader before treating the stream as finished. io.Reader permits such a
// result but discourages it, and without a bound a misbehaving Reader would
// spin forever. The value matches bufio's.
const maxEmptyReads = 100

// fill makes room and reads. It reports whether any bytes were read.
func (b *buf) fill() bool {
	if b.err != nil {
		return false
	}
	// Slide the unconsumed region to the front before considering growth:
	// a record that has been walking down a large buffer needs no more
	// memory, only the space it already paid for. This is what keeps
	// Stats.BufferGrows flat in steady state.
	if b.r > 0 {
		copy(b.data, b.data[b.r:b.w])
		b.w -= b.r
		b.r = 0
	}
	if b.w == len(b.data) && !b.grow() {
		return false
	}
	for range maxEmptyReads {
		n, err := b.src.Read(b.data[b.w:])
		b.w += n
		if err != nil {
			b.err = err
		}
		if n > 0 {
			return true
		}
		if err != nil {
			return false
		}
	}
	b.err = io.ErrNoProgress
	return false
}

// grow doubles the buffer, capped at max. It reports whether it grew.
func (b *buf) grow() bool {
	if len(b.data) >= b.max {
		return false
	}
	size := len(b.data) * 2
	if size < b.initial {
		size = b.initial
	}
	if size > b.max {
		size = b.max
	}
	grown := make([]byte, size)
	copy(grown, b.data[:b.w])
	b.data = grown
	b.stats.BufferGrows++
	return true
}

// indexNewline returns the index of the next '\n', or -1.
//
// bytes.IndexByte is SIMD assembly on every platform this module targets, and
// this is the hottest scan in the package -- every physical line of every log
// passes through it. A hand-written loop here would be several times slower.
func indexNewline(b []byte) int { return bytes.IndexByte(b, '\n') }

// peek returns the buffered data without consuming it, reading more until it
// holds at least want bytes or the stream ends.
//
// It exists for log_line_prefix detection (FMT-004), which has to look at the
// first lines of the stream before it can parse any of them. Consuming them
// and replaying would mean holding records the parser is not ready to emit;
// peeking keeps the read position where it is, so detection is invisible to
// Record.Offset and to Stats.Bytes.
//
// The returned slice is invalidated by any later call that grows or compacts
// the buffer, which is the same contract records have.
func (b *buf) peek(want int) []byte {
	b.consumeBOM()
	if want > b.max {
		want = b.max
	}
	for b.w-b.r < want && !b.atEOF {
		if !b.fill() {
			b.atEOF = true
		}
	}
	return b.data[b.r:b.w]
}
