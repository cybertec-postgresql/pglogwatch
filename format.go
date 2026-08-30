package pglogwatch

// Format identifies a PostgreSQL log destination, as configured by the
// log_destination GUC.
type Format uint8

// The supported log destinations. FormatAuto asks the parser to decide from
// the first non-empty line of input; see [Parser.DetectedFormat].
const (
	FormatAuto   Format = iota // detect from input
	FormatStderr               // log_destination = 'stderr'
	FormatCSV                  // log_destination = 'csvlog'
	FormatJSON                 // log_destination = 'jsonlog', PostgreSQL 15+
)

// String returns the log_destination value this format corresponds to, so that
// error messages and CLI output can name it the way postgresql.conf does.
func (f Format) String() string {
	switch f {
	case FormatAuto:
		return "auto"
	case FormatStderr:
		return "stderr"
	case FormatCSV:
		return "csvlog"
	case FormatJSON:
		return "jsonlog"
	}
	return "unknown"
}

// defaultGlob returns the filename pattern PostgreSQL uses for this
// destination by default, used by [FileSet] when Glob is empty.
//
// PostgreSQL's csvlog and jsonlog writers derive their filename from
// log_filename by replacing a trailing ".log" with ".csv" or ".json", so these
// are the conventional patterns rather than guaranteed ones. A caller with a
// non-default log_filename sets FileSet.Glob explicitly.
func (f Format) defaultGlob() string {
	switch f {
	case FormatCSV:
		return "*.csv"
	case FormatJSON:
		return "*.json"
	default:
		return "*.log"
	}
}
