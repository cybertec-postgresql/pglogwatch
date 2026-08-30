package pglogwatch

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Buffer growth as observed through the parser rather than through buf
// directly, because AC-013 and AC-014 are promises made to a caller of the
// public API, and it is the public path that has to keep them.

func TestParserBufferGrowsThenSettles(t *testing.T) {
	// AC-013: a record larger than InitialBufferBytes grows the buffer, and
	// subsequent records of the same size allocate nothing more.
	in := strings.Repeat(string(fixture(t, "csv/pg14-basic.csv")), 40)
	p := New(strings.NewReader(in), Config{Format: FormatCSV, InitialBufferBytes: 128})

	n := 0
	for p.Next() {
		n++
	}
	require.NoError(t, p.Err())
	require.Equal(t, 240, n)

	st := p.Stats()
	assert.Greater(t, st.BufferGrows, int64(0), "a 128-byte buffer cannot hold these records")
	// Doubling from 128 reaches 1024 in three steps. Anything much beyond
	// that means growth is not amortised, which is what PERF-008 forbids.
	assert.LessOrEqual(t, st.BufferGrows, int64(8),
		"growth must be amortised by doubling, not linear; got %d grows for %d records", st.BufferGrows, n)
}

func TestParserBufferDoesNotGrowWhenSizedWell(t *testing.T) {
	in := strings.Repeat(string(fixture(t, "csv/pg14-basic.csv")), 40)
	p := New(strings.NewReader(in), Config{Format: FormatCSV})
	for p.Next() { //nolint:revive // drain
	}
	require.NoError(t, p.Err())
	assert.Zero(t, p.Stats().BufferGrows, "the default 64 KiB buffer must fit ordinary records")
}

func TestParserSkipsRecordOverMaxRecordBytes(t *testing.T) {
	// AC-014 and E18: Err stays nil, the record is skipped, Truncated
	// counts it, and nothing panics. The stream must survive -- one
	// pathological record cannot be allowed to end a 10 GB scan.
	huge := hugeStatementCSV(t, 64<<10)
	data, err := os.ReadFile(huge)
	require.NoError(t, err)

	in := string(data) + string(fixture(t, "csv/pg14-basic.csv"))
	p := New(strings.NewReader(in), Config{
		Format:             FormatCSV,
		MaxRecordBytes:     1024,
		InitialBufferBytes: 512,
	})

	n := 0
	for p.Next() {
		n++
	}
	require.NoError(t, p.Err(), "AC-014 requires Err() to stay nil")
	assert.Equal(t, int64(1), p.Stats().Truncated)
	assert.Equal(t, 6, n, "every record after the skipped one must still parse")
}

func TestParserHandlesEightMiBRecord(t *testing.T) {
	// TST-004's 8 MiB single statement, generated rather than committed.
	// It fits under the default 16 MiB cap, so it must parse rather than be
	// skipped -- the buffer has to grow to hold it.
	const size = 8 << 20
	path := hugeStatementCSV(t, size)
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck // read-only

	p := New(f, Config{Format: FormatCSV})
	require.True(t, p.Next(), "an 8 MiB record is under the default cap and must parse")
	r := p.Record()
	assert.Greater(t, len(r.Message), size, "the whole statement must be retrievable (COR-001)")
	assert.Zero(t, r.Flags&FlagTruncated)
	assert.False(t, p.Next())
	require.NoError(t, p.Err())
	assert.Zero(t, p.Stats().Truncated)
	assert.Greater(t, p.Stats().BufferGrows, int64(0))
}
