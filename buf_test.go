package pglogwatch

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSplit frames newline-terminated lines, the simplest split function that
// exercises the buffer's growth, compaction and skip paths.
func testSplit(data []byte, atEOF bool) (int, []byte, error) {
	if i := indexNewline(data); i >= 0 {
		return i + 1, trimCR(data[:i]), nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func drain(t *testing.T, b *buf) ([]string, []int64) {
	t.Helper()
	var toks []string
	var offs []int64
	for {
		tok, off, err := b.next(testSplit)
		if errors.Is(err, io.EOF) {
			return toks, offs
		}
		require.NoError(t, err)
		toks = append(toks, string(tok))
		offs = append(offs, off)
	}
}

func newTestBuf(t *testing.T, in string, cfg Config) (*buf, *Stats) {
	t.Helper()
	cfg.normalize()
	st := new(Stats)
	return newBuf(strings.NewReader(in), &cfg, st), st
}

func TestBufTokensAndOffsets(t *testing.T) {
	// IFC-006 wants byte offsets, so check them against positions computed
	// by hand rather than against the buffer's own arithmetic.
	in := "alpha\nbeta\ngamma\n"
	b, st := newTestBuf(t, in, Config{})
	toks, offs := drain(t, b)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, toks)
	assert.Equal(t, []int64{0, 6, 11}, offs)
	assert.Equal(t, int64(len(in)), st.Bytes)
}

func TestBufGrowsOnceThenSettles(t *testing.T) {
	// AC-013: growth happens, and then stops happening for records of the
	// same size. A count that keeps climbing is the signal PERF-001's
	// steady state was never reached.
	line := strings.Repeat("x", 500) + "\n"
	b, st := newTestBuf(t, strings.Repeat(line, 50), Config{InitialBufferBytes: 64})
	toks, _ := drain(t, b)
	require.Len(t, toks, 50)

	grows := st.BufferGrows
	assert.Greater(t, grows, int64(0), "a 500-byte record must outgrow a 64-byte buffer")
	assert.LessOrEqual(t, grows, int64(6), "doubling from 64 reaches 512 in three steps; %d is not amortised", grows)
}

func TestBufCompactionAvoidsGrowth(t *testing.T) {
	// fill() slides the unconsumed region to the front before considering
	// growth. Without that, a long run of small records walking down the
	// buffer would grow it repeatedly for no reason.
	b, st := newTestBuf(t, strings.Repeat("short line\n", 5000), Config{InitialBufferBytes: 4096})
	toks, _ := drain(t, b)
	require.Len(t, toks, 5000)
	assert.Zero(t, st.BufferGrows, "small records must never grow a 4 KiB buffer")
}

func TestBufSkipsOverlargeRecord(t *testing.T) {
	// AC-014 and E18: skipped and counted, stream continues, no panic, and
	// crucially no error -- one pathological record must not end a scan.
	huge := strings.Repeat("x", 2048)
	b, st := newTestBuf(t, huge+"\nsmall\n", Config{MaxRecordBytes: 1024, InitialBufferBytes: 128})
	toks, _ := drain(t, b)
	assert.Equal(t, []string{"small"}, toks)
	assert.Equal(t, int64(1), st.Truncated)
}

func TestBufSkipsOverlargeRecordAtEOF(t *testing.T) {
	// The same, with nothing after the over-large record: the skip must
	// terminate rather than spin looking for a newline that never comes.
	b, st := newTestBuf(t, strings.Repeat("x", 4096), Config{MaxRecordBytes: 1024, InitialBufferBytes: 128})
	toks, _ := drain(t, b)
	assert.Empty(t, toks)
	assert.Equal(t, int64(1), st.Truncated)
}

func TestBufEmptyInput(t *testing.T) {
	b, st := newTestBuf(t, "", Config{})
	toks, _ := drain(t, b)
	assert.Empty(t, toks)
	assert.Zero(t, st.Bytes)
}

func TestBufUnterminatedTail(t *testing.T) {
	b, _ := newTestBuf(t, "a\nb", Config{})
	toks, _ := drain(t, b)
	assert.Equal(t, []string{"a", "b"}, toks)
}

func TestBufReset(t *testing.T) {
	// PERF-011: the storage survives, the position does not.
	b, st := newTestBuf(t, "one\ntwo\n", Config{})
	drain(t, b)
	before := b.data

	b.reset(strings.NewReader("three\n"))
	*st = Stats{}
	toks, offs := drain(t, b)
	assert.Equal(t, []string{"three"}, toks)
	assert.Equal(t, []int64{0}, offs, "offsets restart with the new stream")
	assert.Equal(t, &before[0], &b.data[0], "Reset must not reallocate the buffer")
}

// errReader fails after handing over its content, to check that a read error
// surfaces rather than being mistaken for a clean end of input.
type errReader struct {
	data string
	done bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("device on fire")
	}
	r.done = true
	return copy(p, r.data), nil
}

func TestBufSurfacesReadError(t *testing.T) {
	cfg := Config{}
	cfg.normalize()
	st := new(Stats)
	b := newBuf(&errReader{data: "one\ntwo\n"}, &cfg, st)

	// Buffered records are delivered first; the error arrives once the
	// buffer drains, which is what lets a caller keep good records from a
	// stream that failed part way through.
	tok, _, err := b.next(testSplit)
	require.NoError(t, err)
	assert.Equal(t, "one", string(tok))
	tok, _, err = b.next(testSplit)
	require.NoError(t, err)
	assert.Equal(t, "two", string(tok))

	_, _, err = b.next(testSplit)
	require.Error(t, err)
	assert.NotErrorIs(t, err, io.EOF, "a failed stream must not look like a clean end")
}

// emptyReader always returns (0, nil), which io.Reader permits but discourages.
type emptyReader struct{ calls int }

func (r *emptyReader) Read([]byte) (int, error) { r.calls++; return 0, nil }

func TestBufBoundsEmptyReads(t *testing.T) {
	cfg := Config{}
	cfg.normalize()
	st := new(Stats)
	r := &emptyReader{}
	b := newBuf(r, &cfg, st)

	_, _, err := b.next(testSplit)
	require.Error(t, err)
	assert.ErrorIs(t, err, io.ErrNoProgress)
	assert.LessOrEqual(t, r.calls, maxEmptyReads, "must give up rather than spin")
}

// TestAllocBuf is the PERF-008 gate: once the buffer has reached its working
// size, framing records costs nothing.
func TestAllocBuf(t *testing.T) {
	in := strings.Repeat("a representative log line of moderate length\n", 200)
	cfg := Config{}
	cfg.normalize()
	st := new(Stats)
	// The source reader is built once and rewound with its own Reset, so
	// the only allocations the gate can see are the buffer's own.
	src := strings.NewReader(in)
	b := newBuf(src, &cfg, st)

	allocs.Zero(t, 20, func() {
		src.Reset(in)
		b.reset(src)
		for {
			if _, _, err := b.next(testSplit); err != nil {
				return
			}
		}
	})
}
