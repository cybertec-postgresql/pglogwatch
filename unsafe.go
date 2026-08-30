//go:build !purego

package pglogwatch

import "unsafe"

// This file is the only place in the module permitted to import unsafe
// (PKG-007), and it is limited to the two no-copy conversions between []byte
// and string that unsafe.String and unsafe.Slice exist to provide. Anything
// requiring pointer arithmetic belongs somewhere else, or nowhere.
//
// safe.go carries a copying fallback selected by the purego build tag, so a
// consumer that forbids unsafe still gets a working parser, only slower.

// unsafeString returns a string sharing b's backing array.
//
// The result has the same lifetime as b, which for parser-owned bytes means it
// dies at the next Parser.Next. Callers must not retain it, and must never
// mutate b afterwards: strings are assumed immutable everywhere else in Go,
// including by the map implementation.
func unsafeString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

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
