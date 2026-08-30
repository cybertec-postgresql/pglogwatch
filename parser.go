package pglogwatch

import "io"

// Parser reads PostgreSQL log records from an [io.Reader].
//
// A Parser is not safe for concurrent use by multiple goroutines, and neither
// is the [Record] it returns (PERF-012). Give each goroutine its own, or use
// [ParallelScan].
//
// All parser state lives in this struct: the package has no globals, so two
// parsers never interfere and none of this needs locking (CON-002).
type Parser struct {
	cfg Config
	buf *buf

	// rec is returned by Record for the life of the parser; only its
	// contents change (IFC-002). Callers may therefore hold the pointer,
	// just not the slices inside it.
	rec Record

	stats Stats

	// tz and sev are resolved configuration, computed once so the hot path
	// never touches a map or a location database (PERF-007, FMT-007).
	tz  tzCache
	sev severityResolver

	// detectedPrefix records the log_line_prefix in force for stderr
	// input, whether it was configured or inferred (FMT-004).
	detectedPrefix string

	// format is the resolved destination: cfg.Format unless that is
	// FormatAuto, in which case detection fills it in from the first
	// non-empty line (FMT-005).
	format Format

	// scratch holds bytes a record needs but the input does not contain
	// contiguously -- jsonlog's assembled Location, for instance. It is
	// reused across records, so it grows a bounded number of times and then
	// costs nothing, which is what keeps PERF-001 true for those fields.
	scratch []byte

	// csvFields holds one record's column slices. It lives on the parser
	// rather than on the stack so that splitCSVFields writes into memory
	// that already exists, which is what keeps the split allocation-free
	// (PERF-005).
	csvFields [maxCSVColumns][]byte

	// err is the first fatal error. Malformed lines are not fatal and never
	// land here (IFC-003).
	err error

	// done latches at end of input so that Next keeps returning false
	// without side effects (IFC-001).
	done bool
}

// New returns a Parser reading from r.
//
// The zero [Config] is valid and selects every default, including detecting
// the log destination from the first non-empty line. New does no I/O: the
// first read happens on the first call to [Parser.Next], so a Parser over a
// slow or empty source costs nothing until it is used.
func New(r io.Reader, cfg Config) *Parser {
	cfg.normalize()
	p := &Parser{
		cfg:    cfg,
		format: cfg.Format,
		sev:    newSeverityResolver(cfg.MessagesLang),
	}
	p.buf = newBuf(r, &p.cfg, &p.stats)
	return p
}
