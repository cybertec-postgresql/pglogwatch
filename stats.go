package pglogwatch

// Stats counts what a [Parser] has done since it was created or last reset.
// It is a snapshot: [Parser.Stats] returns a copy, so reading it never races
// with the parser's own accounting.
type Stats struct {
	// Records is the number of records returned by Next.
	Records int64

	// Bytes is the number of input bytes consumed, including the newlines
	// between records. Divided by elapsed time it gives the throughput
	// figure PERF-020 through PERF-023 are stated in.
	Bytes int64

	// Malformed is the number of lines that could not be interpreted and
	// were skipped. These are never reported through Err (IFC-003); a log
	// with a few unparseable lines is normal, not a failure.
	Malformed int64

	// Truncated is the number of records that exceeded
	// Config.MaxRecordBytes and were skipped, plus a final record cut short
	// by end of input.
	Truncated int64

	// BufferGrows is the number of times the read buffer had to grow. In
	// steady state this stops increasing; if it keeps climbing, the input
	// contains steadily larger records and PERF-001's "0 allocations after
	// warm-up" has not been reached.
	BufferGrows int64
}
