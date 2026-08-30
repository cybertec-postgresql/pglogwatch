package pglogwatch

import "io"

// The parser's public control flow.
//
// Next drives a single loop: ask the buffer for the next record using the
// format's split function, hand the bytes to the format's field scanner, and
// either return the record or count it as malformed and try again. Every
// format plugs into those two points and nowhere else, which is what keeps
// csv.go, stderr.go and json.go independent of each other.

// Next advances to the next record and reports whether there is one.
//
// It returns false at end of input and on a fatal error, and keeps returning
// false thereafter with no further reads (IFC-001). A line that cannot be
// parsed is not fatal: it is counted in Stats.Malformed, reported to
// Config.OnMalformed if set, and skipped (FMT-010).
func (p *Parser) Next() bool {
	if p.done {
		return false
	}
	for {
		if !p.ensureFormat() {
			p.done = true
			return false
		}
		tok, off, err := p.buf.next(p.splitRecord)
		if err != nil {
			if err != io.EOF {
				p.err = err
			}
			p.done = true
			return false
		}
		p.rec.reset()
		p.rec.Offset = off
		p.rec.Raw = tok
		if err := p.parseInto(tok); err != nil {
			p.pendingFlags = 0
			p.reportMalformed(tok, err)
			continue
		}
		// Merged after parsing, not before: parseInto assigns to
		// Record.Flags and would otherwise overwrite what the framer
		// found out.
		p.rec.Flags |= p.pendingFlags
		p.pendingFlags = 0
		p.stats.Records++
		return true
	}
}

// Record returns the record most recently read by [Parser.Next].
//
// The pointer is stable for the parser's lifetime; only the contents change
// (IFC-002). The byte slices inside it are borrowed and are invalidated by the
// next call to Next.
func (p *Parser) Record() *Record { return &p.rec }

// Err returns the first fatal error, or nil.
//
// A clean end of input is nil, not io.EOF, and malformed lines are never
// reported here -- they are counted in [Parser.Stats] (IFC-003). An error from
// Err means the input stream itself failed.
func (p *Parser) Err() error { return p.err }

// Stats returns a snapshot of the parser's counters.
func (p *Parser) Stats() Stats { return p.stats }

// DetectedPrefix returns the log_line_prefix in force for stderr input: the
// configured Config.LinePrefix, or the one auto-detected from the first
// Config.DetectLines lines (FMT-004). It is empty for csvlog and jsonlog, and
// before the first successful call to Next.
func (p *Parser) DetectedPrefix() string { return p.detectedPrefix }

// DetectedFormat returns the log destination in use: the configured one, or
// the one detected from the input. It returns FormatAuto only if no record has
// been read yet and no format was configured.
func (p *Parser) DetectedFormat() Format { return p.format }

// Reset points the parser at a new stream, keeping its buffers (PERF-011).
//
// Configuration is preserved, counters and detection state are cleared. Use it
// to walk a directory of log files without paying for a new buffer per file.
func (p *Parser) Reset(r io.Reader) {
	p.buf.reset(r)
	p.rec.reset()
	p.stats = Stats{}
	p.err = nil
	p.done = false
	p.pendingFlags = 0
	p.format = p.cfg.Format
	p.resetFormatState()
	// The timezone cache and severity resolver are deliberately kept: they
	// depend on configuration and on the server that wrote the logs, not on
	// which file is being read, and rebuilding them per file would undo
	// exactly the caching PERF-007 asks for.
}

// reportMalformed counts a line the parser could not interpret and tells the
// caller about it. It never sets p.err: FMT-010 requires the stream to carry
// on, and IFC-003 keeps such lines out of Err.
func (p *Parser) reportMalformed(line []byte, err error) {
	p.stats.Malformed++
	if p.cfg.OnMalformed != nil {
		p.cfg.OnMalformed(line, err)
	}
}
