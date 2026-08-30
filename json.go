package pglogwatch

// jsonlog framing.
//
// FMT-006 is unusually specific about this format: PostgreSQL emits exactly one
// JSON object per physical line, and the parser MUST NOT attempt multi-line
// assembly. That is a restriction worth stating in code rather than only
// obeying, because "parse JSON" instinctively suggests balancing braces.
//
// Balancing braces here would be actively harmful. A truncated line -- the
// normal result of reading a file while PostgreSQL is writing it -- has an
// unbalanced brace, so a brace-balancing framer would swallow the next record,
// and the one after that, until it found a closing brace somewhere in an
// unrelated message. One torn write would corrupt an unbounded run of records
// instead of costing a single malformed line.

// splitJSONRecord frames one jsonlog record: exactly one physical line.
func splitJSONRecord(data []byte, atEOF bool, emitTail bool) (int, []byte, error) {
	return splitLine(data, atEOF, emitTail)
}
