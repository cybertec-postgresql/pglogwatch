package pglogwatch

// csvlog column layouts.
//
// PostgreSQL's csvlog format has grown by appending columns, never by
// reordering them, so the first 23 columns are identical in every supported
// version and the layout is decided entirely by the column count (FMT-001,
// DAT-003):
//
//	columns  versions  columns added
//	23       12        -
//	24       13        backend_type
//	26       14-18     leader_pid, query_id
//
// Column 25 does not exist: PostgreSQL 14 added both leader_pid and query_id
// in the same release, so a 25-column line is not a layout any version emits.
//
// Nothing in the file says which version wrote it, and there is no header
// row, so counting the columns is the only available evidence. That is why a
// parser that reads a fixed prefix of the record -- as the current pgwatch
// regex does, stopping after 12 columns -- cannot support more than one
// version at a time.
const (
	csvLogTime = iota
	csvUserName
	csvDatabaseName
	csvProcessID
	csvConnectionFrom
	csvSessionID
	csvSessionLineNum
	csvCommandTag
	csvSessionStartTime
	csvVirtualTransactionID
	csvTransactionID
	csvErrorSeverity
	csvSQLStateCode
	csvMessage
	csvDetail
	csvHint
	csvInternalQuery
	csvInternalQueryPos
	csvContext
	csvQuery
	csvQueryPos
	csvLocation
	csvApplicationName

	// Present only in the wider layouts; guarded by the column count.
	csvBackendType // 24 and 26 columns
	csvLeaderPID   // 26 columns
	csvQueryID     // 26 columns

	csvMinColumns = csvApplicationName + 1 // 23
)

// csvLayoutIsKnown reports whether a column count matches a layout PostgreSQL
// actually emits.
//
// Counts between the known layouts are accepted rather than rejected: a
// PostgreSQL that appends a column in some future release should degrade to
// "the columns I recognise are correct, the rest are unread", not to a log
// that suddenly counts as entirely malformed. Only a record too short to hold
// the shared columns is unusable, because then the severity itself is in
// doubt.
func csvLayoutIsKnown(columns int) bool { return columns >= csvMinColumns }

// csvHasBackendType reports whether this layout carries backend_type
// (PostgreSQL 13 and later).
func csvHasBackendType(columns int) bool { return columns > csvBackendType }

// csvHasParallelColumns reports whether this layout carries leader_pid and
// query_id (PostgreSQL 14 and later).
func csvHasParallelColumns(columns int) bool { return columns > csvQueryID }
