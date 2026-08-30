package pglogwatch

// Severity is a PostgreSQL error severity, normalised to English.
//
// The values are ordered so that comparison is meaningful: everything from
// SeverityWarning up is a problem, and SeverityError and above is a failed
// statement. This is the order the specification's §4.1 fixes, which places
// SeverityLog below SeverityInfo; note that PostgreSQL's own log_min_messages
// ranking sorts LOG differently, so do not use this ordering to reimplement
// log_min_messages filtering.
type Severity uint8

// The PostgreSQL error severities. SeverityUnknown is the zero value and means
// the severity was absent or not recognised for the configured
// Config.MessagesLang; the record is still returned (E13).
const (
	SeverityUnknown Severity = iota
	SeverityDebug5
	SeverityDebug4
	SeverityDebug3
	SeverityDebug2
	SeverityDebug1
	SeverityLog
	SeverityInfo
	SeverityNotice
	SeverityWarning
	SeverityError
	SeverityFatal
	SeverityPanic
)

// severityNames is indexed by Severity. A slice index costs one bounds check
// and no allocation, where a map would cost a hash per call (FMT-008).
var severityNames = [...]string{
	SeverityUnknown: "",
	SeverityDebug5:  "DEBUG5",
	SeverityDebug4:  "DEBUG4",
	SeverityDebug3:  "DEBUG3",
	SeverityDebug2:  "DEBUG2",
	SeverityDebug1:  "DEBUG1",
	SeverityLog:     "LOG",
	SeverityInfo:    "INFO",
	SeverityNotice:  "NOTICE",
	SeverityWarning: "WARNING",
	SeverityError:   "ERROR",
	SeverityFatal:   "FATAL",
	SeverityPanic:   "PANIC",
}

// String returns the English severity name as PostgreSQL spells it, or "" for
// SeverityUnknown.
func (s Severity) String() string {
	if int(s) < len(severityNames) {
		return severityNames[s]
	}
	return ""
}

// IsProblem reports whether the severity is WARNING or above, which is the
// cut-off the CLI's errors subcommand and pgwatch's event counting both use.
func (s Severity) IsProblem() bool { return s >= SeverityWarning }

// ParseSeverity maps an English severity name to a [Severity]. It performs no
// allocation: switching on string(b) is compiled to a length dispatch plus
// direct byte comparisons, with no copy of b (FMT-008, PERF-005).
//
// Unrecognised input yields SeverityUnknown rather than an error. Localised
// severities are handled by [Config.MessagesLang], not here.
func ParseSeverity(b []byte) Severity {
	switch string(b) {
	case "LOG":
		return SeverityLog
	case "ERROR":
		return SeverityError
	case "WARNING":
		return SeverityWarning
	case "FATAL":
		return SeverityFatal
	case "NOTICE":
		return SeverityNotice
	case "INFO":
		return SeverityInfo
	case "PANIC":
		return SeverityPanic
	case "DEBUG1":
		return SeverityDebug1
	case "DEBUG2":
		return SeverityDebug2
	case "DEBUG3":
		return SeverityDebug3
	case "DEBUG4":
		return SeverityDebug4
	case "DEBUG5":
		return SeverityDebug5
	case "DEBUG":
		// PostgreSQL never writes a bare "DEBUG", but several locale
		// tables carry it as the translation of the whole DEBUG family.
		// Map it to DEBUG1, the least verbose level.
		return SeverityDebug1
	}
	return SeverityUnknown
}
