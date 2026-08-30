package pglogwatch

import "bytes"

// OwnedRecord is a [Record] whose byte slices are owned rather than borrowed.
// It outlives the parser that produced it and is safe to retain, put in a
// slice, or hand to another goroutine.
//
// The distinct type is the point: retention is visible in the type system, so
// a function signature can say whether it keeps what it is given rather than
// leaving it to a doc comment nobody reads.
type OwnedRecord struct {
	Record
}

// borrowedFields returns pointers to every borrowed slice field of the record,
// so that Clone can walk them without repeating the list twice.
//
// Returned as a fixed-size array rather than a slice: it lives on Clone's
// stack and must not become the third allocation.
func (r *Record) borrowedFields() [17]*[]byte {
	return [17]*[]byte{
		&r.RawSeverity,
		&r.User,
		&r.Database,
		&r.ConnectionFrom,
		&r.ApplicationName,
		&r.BackendType,
		&r.CommandTag,
		&r.SessionID,
		&r.VirtualXID,
		&r.Message,
		&r.Detail,
		&r.Hint,
		&r.Query,
		&r.InternalQuery,
		&r.Context,
		&r.Statement,
		&r.Location,
	}
}

// Clone returns an owned copy of the record. It is the only allocation this
// package performs on a caller's behalf, and it performs exactly two: one for
// the [OwnedRecord] and one for the byte array behind all of its fields
// (PERF-003).
//
// Most fields are sub-slices of Raw, so they are re-pointed into the copy of
// Raw instead of being copied again; only fields that came from elsewhere --
// jsonlog's assembled Location, for instance -- add bytes. A clone therefore
// costs about as much memory as the record occupied in the log file.
func (r *Record) Clone() *OwnedRecord {
	o := &OwnedRecord{Record: *r}
	fields := o.borrowedFields()

	// Pass one: size the backing array exactly, so the append below cannot
	// reallocate and invalidate the sub-slices taken from it.
	total := len(r.Raw)
	for _, p := range fields {
		if len(*p) > 0 {
			if _, ok := subsliceOffset(r.Raw, *p); !ok {
				total += len(*p)
			}
		}
	}

	buf := make([]byte, 0, total)
	buf = append(buf, r.Raw...)
	rawCopy := buf[0:len(r.Raw):len(r.Raw)]

	// Pass two: re-point each field at the copy.
	for _, p := range fields {
		b := *p
		switch {
		case b == nil:
			// Absent stays absent: a nil field and a present but
			// empty one mean different things to a caller.
		case len(b) == 0:
			*p = buf[len(buf):len(buf):len(buf)]
		default:
			if off, ok := subsliceOffset(r.Raw, b); ok {
				*p = rawCopy[off : off+len(b) : off+len(b)]
				continue
			}
			start := len(buf)
			buf = append(buf, b...)
			*p = buf[start : start+len(b) : start+len(b)]
		}
	}
	o.Raw = rawCopy
	return o
}

// subsliceOffset reports where b sits inside a, if it does.
//
// For b := a[i:], Go guarantees cap(b) == cap(a)-i, which recovers i without
// unsafe pointer arithmetic -- PKG-007 confines unsafe to string/byte
// conversions, so address comparison is not available here. The arithmetic
// alone could produce a false positive on unrelated slices, so the candidate
// range is verified byte for byte.
//
// A false positive that survives that check is harmless by construction: it
// means a[off:off+len(b)] and b hold identical bytes, so re-pointing at the
// copy of a yields exactly the bytes the caller would have read anyway.
func subsliceOffset(a, b []byte) (int, bool) {
	if len(a) == 0 || len(b) == 0 || cap(b) > cap(a) {
		return 0, false
	}
	off := cap(a) - cap(b)
	if off < 0 || off+len(b) > len(a) {
		return 0, false
	}
	if !bytes.Equal(a[off:off+len(b)], b) {
		return 0, false
	}
	return off, true
}
