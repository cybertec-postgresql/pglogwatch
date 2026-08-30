package pglogwatch

import "iter"

// All returns an iterator over the remaining records.
//
//	for r, err := range p.All() {
//	    if err != nil {
//	        return err
//	    }
//	    use(r.Message)
//	}
//
// The same *[Record] is yielded every time, with new contents, and its byte
// slices are borrowed exactly as they are from [Parser.Record]. Ranging over
// All therefore allocates nothing per record; the only allocation is the
// closure this function returns, once per call (IFC-004).
//
// A fatal error is delivered as a final yield with a nil record, so a loop
// that checks err covers both the error and the end of input. Malformed lines
// are not errors and never appear here (IFC-003).
//
// The low-level [Parser.Next] / [Parser.Record] / [Parser.Err] trio remains
// the documented zero-allocation path (GUD-004); All is a convenience over it
// and shares its semantics exactly, including the borrowing contract. Breaking
// out of the loop leaves the parser positioned after the last record consumed,
// so a second range picks up where the first stopped.
func (p *Parser) All() iter.Seq2[*Record, error] {
	return func(yield func(*Record, error) bool) {
		for p.Next() {
			if !yield(&p.rec, nil) {
				return
			}
		}
		if err := p.Err(); err != nil {
			yield(nil, err)
		}
	}
}
