package pglogwatch

import "time"

// Record is one logical PostgreSQL log event, which may have spanned several
// physical lines in the input.
//
// Every []byte field is BORROWED: it aliases the parser's internal read buffer
// and is invalidated by the next call to [Parser.Next]. Copy what you need, or
// call [Record.Clone] for an owned copy. Fields absent from the input are
// zero-valued rather than an error.
//
// The field order follows the specification's §4.2 listing so that the struct
// can be reviewed against the spec line by line; it is not tuned for
// alignment, and govet's fieldalignment analyser is disabled for that reason.
type Record struct {
	// Time is the record's timestamp: csvlog log_time, jsonlog timestamp,
	// or the stderr %m or %t escape. It is zero when the prefix carries no
	// timestamp at all.
	Time time.Time

	// SessionStart is when the backend that wrote this record connected:
	// csvlog session_start_time, jsonlog session_start, stderr %s.
	SessionStart time.Time

	// Severity is the error severity normalised to English, so that a
	// caller counting errors need not know the server's lc_messages.
	Severity Severity

	// RawSeverity is the severity exactly as it appeared, before locale
	// normalisation. Retained so that COR-001 losslessness holds even for a
	// locale this package does not know.
	RawSeverity []byte

	User            []byte // csvlog user_name, jsonlog user, stderr %u
	Database        []byte // csvlog database_name, jsonlog dbname, stderr %d
	ConnectionFrom  []byte // host:port, or the Unix socket path; stderr %r
	ApplicationName []byte // stderr %a
	BackendType     []byte // stderr %b; absent before PostgreSQL 13 csvlog
	CommandTag      []byte // stderr %i

	ProcessID      int32  // stderr %p
	LeaderPID      int32  // parallel worker's leader; stderr %P, PostgreSQL 14+
	SessionID      []byte // stderr %c
	SessionLineNum int64  // stderr %l
	VirtualXID     []byte // stderr %v
	TransactionID  int64  // stderr %x
	QueryID        int64  // stderr %Q, PostgreSQL 14+

	// SQLState is the five-character error code, e.g. "42P01". The zero
	// value (five NUL bytes) means the record carried no SQLSTATE, which is
	// normal for LOG-severity records.
	SQLState [5]byte

	Message          []byte // the primary message text
	Detail           []byte // csvlog detail, stderr DETAIL:
	Hint             []byte // csvlog hint, stderr HINT:
	Query            []byte // csvlog query, stderr STATEMENT:
	InternalQuery    []byte // csvlog internal_query, stderr QUERY:
	Context          []byte // csvlog context, stderr CONTEXT:
	Statement        []byte // the statement this record is about, if any
	QueryPos         int32  // 1-based cursor position within Query
	InternalQueryPos int32  // 1-based cursor position within InternalQuery

	// Location is the server source location, formatted as
	// "func:file:line". csvlog carries it that way already; for jsonlog,
	// which splits it across func_name, file_name and file_line_num, the
	// parser assembles it in a reusable scratch buffer, so it is borrowed
	// on the same terms as every other field.
	Location []byte

	// Duration is the statement duration parsed from a
	// "duration: N.NNN ms" message. Check FlagHasDuration rather than
	// comparing against zero: a genuinely sub-microsecond duration and an
	// absent one are otherwise indistinguishable.
	Duration time.Duration

	Flags Flags

	// Offset is the byte offset of the record's first byte within the
	// stream the parser was given. It is what [Parser.Seek] consumes, and
	// what an [OffsetStore] persists.
	Offset int64

	// Raw is the complete record as it appeared in the input, newlines and
	// all. Borrowed like every other field.
	Raw []byte
}

// reset returns the record to its zero value between records. Slice fields are
// set to nil rather than truncated: they point into the read buffer, which the
// next record is about to overwrite, so keeping a stale length would hand the
// caller another record's bytes if a field turned out to be absent.
func (r *Record) reset() {
	*r = Record{}
}
