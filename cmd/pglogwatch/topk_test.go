package main

import (
	"bytes"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PERF-028: top-N aggregations must be O(K) in memory, not O(distinct).
//
// This is not a theoretical bound. A server logging query text produces a
// distinct error message per execution, because the parameters differ every
// time -- so an unbounded aggregation holds one string per log record, and the
// tool that was supposed to summarise a 10 GB log needs 10 GB to do it.

// distinctLog builds a log where every record is unique, which is the input
// that breaks an unbounded aggregation.
func distinctLog(n int) string {
	var sb strings.Builder
	for i := range n {
		id := strconv.Itoa(i)
		sb.WriteString(`{"timestamp":"2026-08-30 10:` +
			pad2(i%60) + `:` + pad2((i*7)%60) +
			`.000 UTC","error_severity":"ERROR","state_code":"42P01",` +
			`"pid":` + id + `,"user":"u` + id + `","dbname":"d` + id + `",` +
			`"application_name":"a` + id + `","remote_host":"10.0.` + pad2(i%250) + `.` + pad2((i*3)%250) + `",` +
			`"message":"relation \"t_` + id + `\" does not exist at ` + id + `",` +
			`"statement":"SELECT * FROM t_` + id + ` WHERE id = ` + id + `"}` + "\n")
	}
	return sb.String()
}

func pad2(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v % 100)
}

func TestCounterIsBounded(t *testing.T) {
	// The aggregator itself, directly.
	c := newCounter(10)
	for i := range 100000 {
		c.add("key-"+strconv.Itoa(i), int64(i), "sample")
	}
	assert.LessOrEqual(t, len(c.groups), 10*trackingFactor,
		"PERF-028: the aggregator must stay bounded regardless of distinct keys")
	assert.Positive(t, c.dropped, "dropping must be reported, not silent")
	assert.Len(t, c.top(10), 10)
}

func TestCounterKeepsFrequentGroupsUnderPressure(t *testing.T) {
	// Bounding is only useful if the RIGHT groups survive. A frequent group
	// interleaved with a flood of unique ones must still be reported, or the
	// bound has traded correctness for memory.
	c := newCounter(5)
	for i := range 20000 {
		c.add("unique-"+strconv.Itoa(i), 0, "")
		if i%3 == 0 {
			c.add("the frequent one", 0, "sample")
		}
	}
	top := c.top(5)
	require.NotEmpty(t, top)
	assert.Equal(t, "the frequent one", top[0].key,
		"the most frequent group must survive a flood of unique ones")
	assert.Greater(t, top[0].count, int64(6000))
}

// reportsUnderTest are the subcommands PERF-028 names, plus peaks, whose
// buckets grow with the log's duration rather than with distinct queries.
var reportsUnderTest = []string{"errors", "slow", "connections", "peaks"}

func TestTopNReportsAreBoundedInMemory(t *testing.T) {
	// The property end to end: the same report over ten times the input must
	// not use ten times the memory.
	if testing.Short() {
		t.Skip("memory measurement is not a short test")
	}
	for _, name := range reportsUnderTest {
		t.Run(name, func(t *testing.T) {
			small := measureHeap(t, name, distinctLog(2000))
			large := measureHeap(t, name, distinctLog(20000))

			// Ten times the records. Anything close to ten times the
			// memory means the aggregation is O(distinct).
			assert.Less(t, large, small*4+(1<<20),
				"%s used %d bytes for 2000 records and %d for 20000; "+
					"PERF-028 requires O(K), not O(distinct)", name, small, large)
		})
	}
}

// measureHeap runs one report and returns the heap it retained.
func measureHeap(t *testing.T, name, input string) int64 {
	t.Helper()
	var out, errBuf bytes.Buffer

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	code := run([]string{name}, strings.NewReader(input), &out, &errBuf)
	require.Equal(t, exitOK, code, "stderr: %s", errBuf.String())

	runtime.ReadMemStats(&after)
	//nolint:gosec // heap sizes fit comfortably in int64
	used := int64(after.TotalAlloc - before.TotalAlloc)
	t.Logf("%s over %d bytes of log: %d bytes allocated", name, len(input), used)
	return used
}
