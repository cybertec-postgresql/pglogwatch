// Package pglogwatch is a zero-allocation PostgreSQL log parser for the
// stderr, csvlog and jsonlog destinations of PostgreSQL 12 through 18. Every
// string-valued field of a [Record] is a borrowed []byte aliasing the parser's
// internal read buffer: it is valid only until the next call to
// [Parser.Next], and retaining one past that call is a bug. Use
// [Record.Clone] to keep a record; that is the only sanctioned allocation.
//
// Log watching — following a directory of log files across rotation — is built
// on top of the parser by [FileSet], and is a strict addition to it. The
// parser itself never touches the filesystem, never connects to a server and
// never logs.
//
// # Reading a log
//
// The zero value of [Config] auto-detects the log destination from the first
// non-empty line, and for stderr also auto-detects log_line_prefix:
//
//	p := pglogwatch.New(f, pglogwatch.Config{})
//	for p.Next() {
//	    r := p.Record()
//	    if r.Severity >= pglogwatch.SeverityError {
//	        errCount++
//	    }
//	}
//	if err := p.Err(); err != nil {
//	    return err
//	}
//
// That loop performs no heap allocations after the read buffer has grown to
// fit the largest record seen so far.
//
// # Borrowed slices
//
// The borrowing contract is the same one made by [bufio.Scanner.Bytes] and by
// the ReuseRecord mode of encoding/csv, and it is what makes zero-allocation
// parsing possible: returning strings would force one copy per field per
// record. The consequence is that this is wrong,
//
//	var msgs [][]byte
//	for p.Next() {
//	    msgs = append(msgs, p.Record().Message) // every entry aliases one buffer
//	}
//
// and this is right:
//
//	var owned []*pglogwatch.OwnedRecord
//	for p.Next() {
//	    owned = append(owned, p.Record().Clone())
//	}
//
// # Concurrency
//
// A [Parser] is not safe for concurrent use by multiple goroutines, and
// neither is the [Record] it hands out. That is not an oversight: a parser
// hands out slices of one reusable buffer, which is what makes it
// allocation-free, and sharing that buffer between goroutines would mean
// copying -- giving up the property the package exists for.
//
// Give each goroutine its own parser, or use [ParallelScan], which does
// exactly that: one [Parser] per worker, over a different part of the input.
//
// [ParallelScan] does NOT preserve order. Records arrive as workers reach
// them, and the order differs between runs on the same input. A caller that
// needs order should use a [Parser] directly, or sort what it collects.
// Its callback is shared by every worker, so it must be safe to call
// concurrently; the [Record] passed to it belongs to the calling worker and
// is valid until the callback returns.
package pglogwatch
