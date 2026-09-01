//go:build !purego

package pglogwatch

import "unsafe"

// This file is the only place in the module permitted to import unsafe
// (PKG-007), and it is limited to the one no-copy conversion from string to
// []byte that unsafe.Slice exists to provide. Anything requiring pointer
// arithmetic belongs somewhere else, or nowhere.
//
// safe.go carries a copying fallback selected by the purego build tag, so a
// consumer that forbids unsafe still gets a working parser, only slower.

// unsafeBytes returns a []byte sharing s's backing array.
//
// The result MUST NOT be written to. String data may live in read-only memory,
// where a write faults rather than misbehaving. This exists only so that
// string-typed configuration can be scanned by the same byte scanners the
// parser uses on its buffer.
func unsafeBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
