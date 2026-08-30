package pglogwatch_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/cybertec-postgresql/pglogwatch"
)

const sampleLog = `{"timestamp":"2026-08-30 10:11:12.123 UTC","error_severity":"LOG","message":"duration: 1.500 ms  statement: SELECT 1"}
{"timestamp":"2026-08-30 10:11:13.001 UTC","error_severity":"ERROR","state_code":"42P01","message":"relation \"nope\" does not exist"}
{"timestamp":"2026-08-30 10:11:14.500 UTC","error_severity":"WARNING","message":"there is already a transaction in progress"}
`

// The zero Config detects the log destination and, for stderr, the
// log_line_prefix. The loop below performs no heap allocations.
func Example() {
	p := pglogwatch.New(strings.NewReader(sampleLog), pglogwatch.Config{})

	var problems int
	for p.Next() {
		if p.Record().Severity.IsProblem() {
			problems++
		}
	}
	if err := p.Err(); err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("detected %s, %d records, %d problems\n",
		p.DetectedFormat(), p.Stats().Records, problems)
	// Output: detected jsonlog, 3 records, 2 problems
}

// Record fields are borrowed: they alias the parser's buffer and are
// invalidated by the next call to Next. Clone is the way to keep one.
func ExampleRecord_Clone() {
	p := pglogwatch.New(strings.NewReader(sampleLog), pglogwatch.Config{})

	var worst *pglogwatch.OwnedRecord
	for p.Next() {
		if r := p.Record(); worst == nil || r.Severity > worst.Severity {
			worst = r.Clone() // the only allocation in this loop
		}
	}
	fmt.Printf("%s: %s\n", worst.Severity, worst.Message)
	// Output: ERROR: relation \"nope\" does not exist
}

// Unescaping is deferred, so a caller that never reads a message never pays
// for one. Reuse a buffer of your own across records.
func ExampleAppendUnquoted() {
	p := pglogwatch.New(strings.NewReader(sampleLog), pglogwatch.Config{})

	var buf []byte
	for p.Next() {
		r := p.Record()
		msg := r.Message
		if r.Flags&pglogwatch.FlagNeedsUnquote != 0 {
			buf = pglogwatch.AppendUnquoted(buf[:0], r.Message, p.DetectedFormat())
			msg = buf
		}
		if r.Severity == pglogwatch.SeverityError {
			fmt.Println(string(msg))
		}
	}
	// Output: relation "nope" does not exist
}

// ParallelScan is the supported way to use several cores. A Parser is not safe
// for concurrent use, so each worker gets its own; the callback is shared and
// must therefore be safe to call concurrently.
//
// Records arrive in whatever order the workers reach them. This example sorts
// what it collects, because the arrival order is not reproducible.
func ExampleParallelScan() {
	srcs := []io.ReaderAt{strings.NewReader(sampleLog), bytes.NewReader([]byte(sampleLog))}

	var mu sync.Mutex
	var severities []string

	err := pglogwatch.ParallelScan(context.Background(), srcs, pglogwatch.Config{}, 4,
		func(_ int, r *pglogwatch.Record) error {
			mu.Lock()
			defer mu.Unlock()
			severities = append(severities, r.Severity.String())
			return nil
		})
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	sort.Strings(severities)
	fmt.Println(severities)
	// Output: [ERROR ERROR LOG LOG WARNING WARNING]
}
