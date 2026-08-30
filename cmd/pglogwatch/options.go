package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cybertec-postgresql/pglogwatch"
	"github.com/cybertec-postgresql/pglogwatch/compress"
)

// errNoInput reports that no path matched, which IFC-012 maps to exit code 2.
var errNoInput = errors.New("no input matched")

// options is the global configuration every subcommand shares.
type options struct {
	format     string
	linePrefix string
	lang       string
	begin      string
	end        string
	jobs       int
	output     string
	noColor    bool
	top        int

	paths []string

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer

	// resolved
	cfg      pglogwatch.Config
	beginAt  time.Time
	endAt    time.Time
	jsonOut  bool
	minDur   time.Duration
	bucket   time.Duration
	grepArgs grepOptions
}

func (o *options) register(fs *flag.FlagSet) {
	fs.StringVar(&o.format, "format", "", "stderr, csvlog or jsonlog (default: detect)")
	fs.StringVar(&o.linePrefix, "line-prefix", "", "log_line_prefix for stderr (default: detect)")
	fs.StringVar(&o.lang, "lang", "", "lc_messages of the server that wrote the log")
	fs.StringVar(&o.begin, "begin", "", "ignore records before this time")
	fs.StringVar(&o.end, "end", "", "ignore records at or after this time")
	fs.IntVar(&o.jobs, "jobs", 0, "parallel workers (default: one per CPU)")
	fs.StringVar(&o.output, "output", "text", "text or json")
	fs.BoolVar(&o.noColor, "no-color", false, "never colourise output")
	fs.IntVar(&o.top, "top", 10, "how many rows in a top-N report")
}

func (o *options) normalize() error {
	switch o.output {
	case "", "text":
		o.jsonOut = false
	case "json":
		o.jsonOut = true
	default:
		return fmt.Errorf("unknown --output %q; use text or json", o.output)
	}

	f, err := parseFormat(o.format)
	if err != nil {
		return err
	}
	o.cfg = pglogwatch.Config{
		Format:       f,
		LinePrefix:   o.linePrefix,
		MessagesLang: o.lang,
	}

	if o.begin != "" {
		if o.beginAt, err = parseTimeArg(o.begin); err != nil {
			return err
		}
	}
	if o.end != "" {
		if o.endAt, err = parseTimeArg(o.end); err != nil {
			return err
		}
	}
	if o.top <= 0 {
		o.top = 10
	}
	return nil
}

func parseFormat(s string) (pglogwatch.Format, error) {
	switch s {
	case "", "auto":
		return pglogwatch.FormatAuto, nil
	case "stderr":
		return pglogwatch.FormatStderr, nil
	case "csv", "csvlog":
		return pglogwatch.FormatCSV, nil
	case "json", "jsonlog":
		return pglogwatch.FormatJSON, nil
	}
	return 0, fmt.Errorf("unknown --format %q; use stderr, csvlog or jsonlog", s)
}

// inRange reports whether a record falls inside --begin and --end.
//
// A record with no timestamp is kept. Dropping it would silently discard
// unparseable-but-real log lines whenever a time window is given, which is the
// opposite of what a person narrowing a search expects.
func (o *options) inRange(r *pglogwatch.Record) bool {
	if r.Time.IsZero() {
		return true
	}
	if !o.beginAt.IsZero() && r.Time.Before(o.beginAt) {
		return false
	}
	if !o.endAt.IsZero() && !r.Time.Before(o.endAt) {
		return false
	}
	return true
}

// eachRecord runs fn over every record of every input, in order.
//
// Order matters for the reports that show context or time buckets, so this is
// the serial path; forEachParallel is the one --jobs uses.
func (o *options) eachRecord(fn func(*pglogwatch.Record) error) error {
	return o.eachRecordWithFormat(func(r *pglogwatch.Record, _ pglogwatch.Format) error {
		return fn(r)
	})
}

// eachRecordWithFormat is eachRecord for callers that need the resolved
// format -- which unescaping does, since csvlog and jsonlog escape differently
// and detection may have chosen either.
func (o *options) eachRecordWithFormat(fn func(*pglogwatch.Record, pglogwatch.Format) error) error {
	inputs, err := o.openInputs()
	if err != nil {
		return err
	}
	defer inputs.Close()

	for {
		rc, name, ok := inputs.Next()
		if !ok {
			return nil
		}
		p := pglogwatch.New(rc, o.cfg)
		for p.Next() {
			r := p.Record()
			if !o.inRange(r) {
				continue
			}
			if err := fn(r, p.DetectedFormat()); err != nil {
				_ = rc.Close()
				return err
			}
		}
		perr := p.Err()
		_ = rc.Close()
		if perr != nil {
			return fmt.Errorf("%s: %w", name, perr)
		}
	}
}

// eachRecordStats is eachRecord for the bench subcommand, which needs each
// parser's Stats as well as its records -- Stats.Bytes is what the MB/s figure
// is computed from, and taking it from the parser rather than from file sizes
// means the number describes what was actually parsed.
func (o *options) eachRecordStats(fn func(*pglogwatch.Record), done func(pglogwatch.Stats)) error {
	inputs, err := o.openInputs()
	if err != nil {
		return err
	}
	defer inputs.Close()

	for {
		rc, name, ok := inputs.Next()
		if !ok {
			return nil
		}
		p := pglogwatch.New(rc, o.cfg)
		for p.Next() {
			fn(p.Record())
		}
		perr := p.Err()
		done(p.Stats())
		_ = rc.Close()
		if perr != nil {
			return fmt.Errorf("%s: %w", name, perr)
		}
	}
}

// inputSet yields the readers a subcommand should consume.
type inputSet struct {
	o      *options
	idx    int
	usedIn bool
	open   io.Closer
}

func (o *options) openInputs() (*inputSet, error) {
	if len(o.paths) == 0 {
		return &inputSet{o: o}, nil // standard input (IFC-010)
	}
	// Fail before doing any work if nothing matched, so exit code 2 is
	// reported promptly rather than after streaming half a report.
	for _, p := range o.paths {
		if _, err := os.Stat(p); err != nil {
			return nil, errNoInput
		}
	}
	return &inputSet{o: o}, nil
}

// Next returns the next input reader, its display name, and whether there is
// one.
func (s *inputSet) Next() (io.ReadCloser, string, bool) {
	if len(s.o.paths) == 0 {
		if s.usedIn {
			return nil, "", false
		}
		s.usedIn = true
		return io.NopCloser(s.o.stdin), "(standard input)", true
	}
	if s.idx >= len(s.o.paths) {
		return nil, "", false
	}
	path := s.o.paths[s.idx]
	s.idx++

	// compress.Open passes plain files through, so every input goes through
	// it and a compressed rotated log needs no flag (PKG-004, T134).
	rc, err := compress.Open(path)
	if err != nil {
		return io.NopCloser(errReader{err}), path, true
	}
	return rc, path, true
}

func (s *inputSet) Close() error { return nil }

// errReader turns an open failure into a read failure, so one unreadable file
// reports as an error on that file rather than aborting before the others.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
