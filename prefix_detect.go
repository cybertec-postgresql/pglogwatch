package pglogwatch

import (
	"bytes"
	"strings"
)

// log_line_prefix auto-detection (FMT-004).
//
// log_line_prefix is a server setting that the log file does not record, so a
// tool reading someone else's log has no way to be told it. Requiring the
// operator to supply it puts a configuration step between them and an answer,
// which is a large part of why pgwatch's log parsing goes unused.
//
// Detection is two strategies in order:
//
//  1. Score a list of known prefixes against the first lines. Almost every
//     server runs a distribution default or something close to it, so this
//     answers the common case exactly.
//  2. Reconstruct a prefix from the shape of a line that has a recognisable
//     "SEVERITY:  " label. This handles the site that invented its own.
//
// Both are verified the same way -- a candidate must parse a majority of the
// sampled lines into a recognisable severity -- so a wrong guess is rejected
// rather than silently adopted.

// detectPeekBytes bounds how much input detection may look at. A 200-line
// sample of a real log fits comfortably; the cap matters for a log with
// pathologically long lines, where DetectLines alone would not bound the read.
const detectPeekBytes = 256 << 10

// candidatePrefixes are the prefixes worth trying before reconstructing one.
//
// Ordered longest-first within each family, because a shorter prefix is a
// prefix of a longer one: "%m [%p] " matches every line that "%m [%p] %q%u@%d "
// matches, but leaves the user and database unparsed. Scoring alone cannot
// separate them -- both match every line -- so specificity breaks the tie.
var candidatePrefixes = []string{
	// PostgreSQL and distribution defaults.
	"%m [%p-%l] %q%u@%d ",
	"%m [%p] %q%u@%d ",
	"%t [%p-%l] %q%u@%d ",
	"%t [%p]: [%l-1] user=%u,db=%d,app=%a,client=%h ",
	"%t [%p]: user=%u,db=%d,app=%a,client=%h ",
	"%m [%p] %q%u@%d %a ",
	"%m [%p] %q%u@%d %r ",
	"%m [%p-%l] ",
	"%t [%p-%l] ",
	"%m [%p]: ",
	"%t [%p]: ",
	"%m [%p] ",
	"%t [%p] ",
	"%m %q%u@%d ",
	"%t %q%u@%d ",
	"%p ",
	"%m ",
	"%t ",
}

// compiledCandidates is candidatePrefixes compiled once.
//
// Detection runs again after every Reset, because a new file may have been
// written by a differently configured server. Compiling eighteen templates per
// file put ~79 allocations into the PERF-001 gate for a parser walking a log
// directory -- caught by TestAllocStderrDetectedPrefix, which exists for
// exactly this. The templates are immutable once built, so sharing them is
// safe and costs nothing at run time (CON-002 forbids mutable global state,
// not constant tables).
var compiledCandidates = func() []*prefixTemplate {
	out := make([]*prefixTemplate, 0, len(candidatePrefixes))
	for _, src := range candidatePrefixes {
		if tpl, err := compilePrefix(src); err == nil {
			out = append(out, tpl)
		}
		// A typo in the table above must not break detection at run
		// time; the table is covered by its own test instead.
	}
	return out
}()

// detectPrefix picks a log_line_prefix for the buffered input, or nil when
// none fits.
func (p *Parser) detectPrefix() *prefixTemplate {
	sample := p.buf.peek(detectPeekBytes)
	if len(sample) == 0 {
		return nil
	}

	// %m and %t differ only in the fractional seconds, and the timestamp
	// scanner accepts both forms for either escape -- deliberately, since
	// being strict there would reject a valid log over a cosmetic
	// difference. Detection, though, must report the escape the server
	// actually used, so the observed form decides which candidates are
	// eligible at all.
	stamp := observedTimestampEscape(sample, p.cfg.DetectLines)

	var best *prefixTemplate
	bestScore, bestExplained, bestSegs := 0, 0, 0
	for _, tpl := range compiledCandidates {
		if stamp != 0 && usesOtherTimestampEscape(tpl.src, stamp) {
			continue
		}
		score, explained := p.scorePrefix(tpl, sample)
		if score == 0 {
			continue
		}
		if best == nil || score > bestScore ||
			(score == bestScore && explained > bestExplained) ||
			(score == bestScore && explained == bestExplained && len(tpl.segs) < bestSegs) {
			best, bestScore, bestExplained, bestSegs = tpl, score, explained, len(tpl.segs)
		}
	}
	if best != nil {
		return best
	}
	return p.reconstructPrefix(sample)
}

// scorePrefix reports how many sampled lines the template parses into a
// recognisable label, and how many prefix bytes it accounted for in total.
//
// Matching the prefix is not enough on its own: a short prefix matches almost
// anything. Requiring a label after it is what makes the score mean "this
// template explains the line" rather than "this template did not contradict
// it".
//
// The byte count is the tie-breaker, and it is the one that matters. Because
// everything after %q is optional, "%m [%p] %q%u@%d %a " matches every line
// that "%m [%p] " matches -- the optional tail simply goes unmatched -- so
// both score identically on a log with no user or database in its prefix.
// Preferring the template that ACCOUNTED FOR MORE BYTES picks the richer one
// only when the extra fields are really there, and falls back to the simpler
// one when they are not. Preferring more segments, which was the obvious first
// attempt, reports a prefix the server never had.
func (p *Parser) scorePrefix(tpl *prefixTemplate, sample []byte) (score, explained int) {
	seen := 0
	for line := range sampleLines(sample, p.cfg.DetectLines) {
		seen++
		rest, ok := tpl.scanPrefix(line, nil, &p.tz)
		if !ok {
			continue
		}
		label, _, hasLabel := splitLabel(rest)
		if !hasLabel || !p.labelIsKnown(label) {
			continue
		}
		if !p.matchIsPlausible(tpl, line) {
			continue
		}
		score++
		explained += len(line) - len(rest)
	}
	// A majority, not merely a nonzero count: a continuation-heavy log
	// contains lines that no prefix explains, and a template that happened
	// to fit one of them should not win.
	if seen > 0 && score*2 <= seen {
		return 0, 0
	}
	return score, explained
}

// matchIsPlausible rejects a match whose field VALUES cannot be what the
// escapes claim, even though the template matched.
//
// This is the discriminator scoring alone cannot provide. Against a Debian
// prefix line
//
//	2026-08-30 10:11:12.123 CEST [31337-1] app_user@appdb LOG:  ...
//
// the much simpler template "%m %q%u@%d " also matches: %u is free-form text
// delimited by "@", so it happily swallows "CEST [31337-1] app_user". It
// scores identically and accounts for identically many bytes, and on segment
// count it WINS. Only looking at the value shows it to be nonsense.
//
// Applied to detection only. A caller who configures a prefix has told the
// parser what the log looks like, and second-guessing them there would reject
// valid logs.
func (p *Parser) matchIsPlausible(tpl *prefixTemplate, line []byte) bool {
	p.detectRec.reset()
	if _, ok := tpl.scanPrefix(line, &p.detectRec, &p.tz); !ok {
		return false
	}
	r := &p.detectRec
	// A user or database name with a space or a bracket in it is possible
	// in PostgreSQL and essentially unheard of in practice, whereas a
	// mis-delimited match produces one every time.
	for _, v := range [][]byte{r.User, r.Database} {
		if bytes.ContainsAny(v, " []") {
			return false
		}
	}
	for _, v := range [][]byte{r.ApplicationName, r.BackendType, r.CommandTag, r.SessionID, r.VirtualXID} {
		if bytes.ContainsAny(v, "[]") {
			return false
		}
	}
	return true
}

// observedTimestampEscape reports whether the sample's leading timestamps
// carry fractional seconds ('m') or not ('t'), or 0 if there is no timestamp.
func observedTimestampEscape(sample []byte, maxLines int) byte {
	for line := range sampleLines(sample, maxLines) {
		if _, n, ok := scanTimestamp(line); ok {
			if bytes.IndexByte(line[:n], '.') >= 0 {
				return 'm'
			}
			return 't'
		}
	}
	return 0
}

// usesOtherTimestampEscape reports whether src spells its timestamp with the
// escape that the log does not use.
func usesOtherTimestampEscape(src string, observed byte) bool {
	other := "%t"
	if observed == 't' {
		other = "%m"
	}
	return strings.Contains(src, other)
}

// labelIsKnown reports whether a label is a severity in the configured
// language or one of PostgreSQL's continuation labels.
func (p *Parser) labelIsKnown(label []byte) bool {
	if p.sev.resolve(label) != SeverityUnknown {
		return true
	}
	return continuationField(label) != contNone
}

// reconstructPrefix builds a prefix from the shape of a sampled line.
//
// It walks the region before the first recognisable label and emits an escape
// for each thing it recognises -- a timestamp, a run of digits -- and literal
// text for everything else. That yields a template with correct field
// boundaries even for a prefix nobody has seen before, which is the point of
// FMT-004's "generic heuristic scanner".
//
// The result is verified by the same scoring used for the candidate list, so a
// reconstruction that only fits the line it was built from is discarded.
func (p *Parser) reconstructPrefix(sample []byte) *prefixTemplate {
	for line := range sampleLines(sample, p.cfg.DetectLines) {
		region, ok := prefixRegion(line)
		if !ok {
			continue
		}
		src := describePrefixRegion(region)
		if src == "" {
			continue
		}
		tpl, err := compilePrefix(src)
		if err != nil {
			continue
		}
		if score, _ := p.scorePrefix(tpl, sample); score > 0 {
			return tpl
		}
	}
	return nil
}

// prefixRegion returns the part of a line before its "LABEL:  " marker.
func prefixRegion(line []byte) ([]byte, bool) {
	for i := 0; i+1 < len(line); i++ {
		if line[i] != ':' || !isSpaceOrTab(line[i+1]) {
			continue
		}
		start := i
		for start > 0 && !isSpaceOrTab(line[start-1]) {
			start--
		}
		if start == 0 {
			continue // the label is the whole line; nothing to learn
		}
		return line[:start], true
	}
	return nil, false
}

// describePrefixRegion renders a prefix region as a log_line_prefix.
func describePrefixRegion(region []byte) string {
	var sb strings.Builder
	sawEscape := false
	for i := 0; i < len(region); {
		if _, n, ok := scanTimestamp(region[i:]); ok {
			// A timestamp with a fractional part is %m, without is %t.
			// Guessing wrong costs nothing: both parse either form.
			if bytes.IndexByte(region[i:i+n], '.') >= 0 {
				sb.WriteString("%m")
			} else {
				sb.WriteString("%t")
			}
			i += n
			sawEscape = true
			continue
		}
		if n, ok := scanDigitsLen(region[i:]); ok {
			// The first bare number in a prefix is the process id in
			// every layout PostgreSQL or a distribution ships.
			sb.WriteString("%p")
			i += n
			sawEscape = true
			continue
		}
		if region[i] == '%' {
			sb.WriteString("%%")
		} else {
			sb.WriteByte(region[i])
		}
		i++
	}
	if !sawEscape {
		return ""
	}
	return sb.String()
}

// sampleLines iterates over at most max complete lines of the sample.
func sampleLines(sample []byte, maxLines int) func(func([]byte) bool) {
	return func(yield func([]byte) bool) {
		n := 0
		for len(sample) > 0 && n < maxLines {
			i := indexNewline(sample)
			if i < 0 {
				return // a partial trailing line proves nothing
			}
			line := trimCR(sample[:i])
			sample = sample[i+1:]
			if len(line) == 0 {
				continue
			}
			n++
			if !yield(line) {
				return
			}
		}
	}
}
