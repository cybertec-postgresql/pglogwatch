package pglogwatch

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repeatFixture returns a fixture repeated enough times to look like a real
// scan rather than a handful of records, so the gate measures steady state and
// not start-up.
func repeatFixture(t *testing.T, name string, times int) []byte {
	t.Helper()
	return bytes.Repeat(fixture(t, name), times)
}

// TestAllocCSVParse is AC-011 and PERF-001 for csvlog: after warm-up, parsing
// every record of the corpus must perform exactly zero heap allocations.
//
// This is the gate the whole design exists to satisfy. If it fails, the cause
// is almost always one of four things: a []byte-to-string conversion that the
// compiler could not keep on the stack, a strconv call, a time.Parse call, or
// a closure capturing something that now escapes.
func TestAllocCSVParse(t *testing.T) {
	in := repeatFixture(t, "csv/pg14-basic.csv", 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatCSV})

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			r := p.Record()
			// Touch the fields a real consumer touches, so the gate
			// covers field extraction and not only framing.
			_ = r.Severity
			_ = r.Database
			_ = r.Message
			_ = r.Time
		}
	})
}

// TestAllocCSVSeverityOnly covers the severity-histogram workload, which is
// what pgwatch actually does and what PERF-021 measures.
func TestAllocCSVSeverityOnly(t *testing.T) {
	in := repeatFixture(t, "csv/pg14-basic.csv", 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatCSV})
	var counts [16]int64

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for p.Next() {
			counts[p.Record().Severity]++
		}
	})
	assert.NotZero(t, counts[SeverityError])
}

// TestAllocCSVAllFormatsIterator checks that the range-over-func form costs
// nothing per record either (IFC-004), since a caller who prefers it should
// not silently pay for the convenience.
func TestAllocCSVIterator(t *testing.T) {
	in := repeatFixture(t, "csv/pg14-basic.csv", 50)
	rd := bytes.NewReader(in)
	p := New(rd, Config{Format: FormatCSV})

	allocs.Zero(t, 10, func() {
		rd.Reset(in)
		p.Reset(rd)
		for r, err := range p.All() {
			if err != nil {
				return
			}
			_ = r.Severity
		}
	})
}

// TestAllocCloneIsBounded is PERF-003: Clone is the one sanctioned allocation
// path and costs at most two allocations -- the OwnedRecord and one byte array
// behind all of its fields.
func TestAllocCloneIsBounded(t *testing.T) {
	p := New(bytes.NewReader(fixture(t, "csv/pg14-basic.csv")), Config{Format: FormatCSV})
	require.True(t, p.Next())
	r := p.Record()

	allocs.AtMost(t, 100, 2, func() {
		_ = r.Clone()
	})
}

// TestCloneOutlivesTheParser is the behavioural half of PERF-003: a clone must
// still be readable after the parser has moved on and overwritten its buffer.
func TestCloneOutlivesTheParser(t *testing.T) {
	in := strings.Repeat(string(fixture(t, "csv/pg14-basic.csv")), 20)
	p := New(strings.NewReader(in), Config{Format: FormatCSV})

	require.True(t, p.Next())
	first := p.Record().Clone()
	wantMessage := string(first.Message)
	wantRaw := string(first.Raw)

	for p.Next() { //nolint:revive // drain the stream to force buffer reuse
	}
	require.NoError(t, p.Err())

	assert.Equal(t, wantMessage, string(first.Message), "clone was invalidated by later parsing")
	assert.Equal(t, wantRaw, string(first.Raw))
	assert.Equal(t, "app_user", string(first.User))
	assert.Equal(t, "appdb", string(first.Database))
}
