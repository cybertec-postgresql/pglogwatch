package pglogwatch

// parseError is the concrete type behind every error this package produces on
// or near the hot path. Values are preallocated at init: PERF-010 forbids
// building an error per record, so fmt.Errorf and errors.New must not appear
// in any function Next can reach.
//
// Every parseError except ErrBadLinePrefix satisfies errors.Is(err,
// ErrMalformedLine), so callers can either test the general case or switch on
// the specific one without the package having to wrap anything.
type parseError struct {
	msg        string
	malformed  bool
	underlying error
}

func (e *parseError) Error() string { return e.msg }

// Is reports whether e should be treated as target. It gives every specific
// malformed-line reason the ErrMalformedLine identity without allocating a
// wrapper per occurrence.
func (e *parseError) Is(target error) bool {
	if target == ErrMalformedLine {
		return e.malformed
	}
	return e.underlying != nil && e.underlying == target
}

func newParseError(msg string) *parseError {
	return &parseError{msg: msg, malformed: true}
}

// Errors reported by the parser. They are values, not constructors: a parser
// that allocated an error per bad line would allocate per record on a bad
// file, which PERF-010 forbids.
var (
	// ErrMalformedLine is the general "this line could not be interpreted"
	// error handed to Config.OnMalformed. Every specific reason below
	// satisfies errors.Is(err, ErrMalformedLine), so a caller that does not
	// care why can test for just this one.
	//
	// It is never returned from Parser.Err: malformed lines are counted in
	// Stats.Malformed and skipped (FMT-010, IFC-003).
	ErrMalformedLine = &parseError{msg: "pglogwatch: malformed log line", malformed: true}

	// ErrRecordTooLarge reports a single record larger than
	// Config.MaxRecordBytes. The record is skipped and counted in
	// Stats.Truncated; parsing continues with the next one (PERF-008, E18).
	ErrRecordTooLarge = &parseError{msg: "pglogwatch: record exceeds MaxRecordBytes", malformed: true}

	// ErrBadLinePrefix reports a Config.LinePrefix that is not a valid
	// log_line_prefix. Unlike the others this is a configuration mistake
	// rather than bad input, so it is fatal: New records it and Parser.Err
	// returns it.
	ErrBadLinePrefix = &parseError{msg: "pglogwatch: invalid log_line_prefix"}
)

// Specific malformed-line reasons. Unexported: they exist so that
// Config.OnMalformed receives something more useful than a single generic
// error, not so that callers switch on them. Promoting one of these to the
// public API later is additive; demoting an exported one would not be.
var (
	errShortRecord    = newParseError("pglogwatch: record has too few fields")
	errPrefixMismatch = newParseError("pglogwatch: line does not match log_line_prefix")
	errBadJSON        = newParseError("pglogwatch: malformed JSON object")
	errUnterminated   = newParseError("pglogwatch: unterminated quoted field")
)

// Seek argument errors. Unexported for the same reason as the reasons above,
// and additionally because they report a caller mistake rather than bad input:
// a program that passes a whence it did not mean has a bug to fix, not an
// error to branch on. They are not malformed-line errors and so do not satisfy
// errors.Is(err, ErrMalformedLine).
var (
	errBadWhence      = &parseError{msg: "pglogwatch: invalid whence for Seek"}
	errNegativeOffset = &parseError{msg: "pglogwatch: Seek to a negative offset"}
)
