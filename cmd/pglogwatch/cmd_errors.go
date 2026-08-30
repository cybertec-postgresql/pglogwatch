package main

import (
	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["errors"] = command{
		summary: "WARNING and above: histogram and the most frequent messages",
		flags:   noFlags,
		run:     runErrors,
	}
}

// runErrors reports what went wrong: a severity histogram, then the most
// frequent messages.
//
// Messages are grouped by their normalised text (see normalizeMessage), not
// verbatim. Verbatim grouping produces one group per occurrence for any error
// that names a value -- which is most of them -- and a "top 10 errors" report
// where every row has a count of 1 answers nothing.
func runErrors(o *options) error {
	var bySeverity [16]int64
	byState := newCounter(o.top)
	byMessage := newCounter(o.top)
	var total int64
	var buf, normBuf []byte

	err := o.eachRecordWithFormat(func(r *pglogwatch.Record, f pglogwatch.Format) error {
		if !r.Severity.IsProblem() {
			return nil
		}
		total++
		if int(r.Severity) < len(bySeverity) {
			bySeverity[r.Severity]++
		}
		msg := unquoted(r.Message, r, f, &buf)
		normBuf = normalizeMessage(normBuf[:0], msg)
		byMessage.add(string(normBuf), 0, oneLine(truncate(string(msg), 200)))
		if r.SQLState != [5]byte{} {
			byState.add(string(r.SQLState[:]), 0, "")
		}
		return nil
	})
	if err != nil {
		return err
	}

	if o.jsonOut {
		return errorsJSON(o, total, &bySeverity, byState, byMessage)
	}
	errorsText(o, total, &bySeverity, byState, byMessage)
	return nil
}

func errorsText(o *options, total int64, bySeverity *[16]int64, byState, byMessage *counter) {
	t := newTable(o.stdout, "severity", "count")
	for _, s := range severityOrder {
		if s < pglogwatch.SeverityWarning {
			continue
		}
		if n := bySeverity[s]; n > 0 {
			t.add(s.String(), itoa(n))
		}
	}
	t.add("total", itoa(total))
	t.flush()

	o.stdout.Write([]byte("\n")) //nolint:errcheck // report output
	m := newTable(o.stdout, "count", "message")
	for _, g := range byMessage.top(o.top) {
		m.add(itoa(g.count), g.sample)
	}
	m.flush()
	reportDropped(o, byMessage)

	if len(byState.groups) > 0 {
		o.stdout.Write([]byte("\n")) //nolint:errcheck // report output
		s := newTable(o.stdout, "count", "sqlstate")
		for _, g := range byState.top(o.top) {
			s.add(itoa(g.count), g.key)
		}
		s.flush()
	}
}

func errorsJSON(o *options, total int64, bySeverity *[16]int64, byState, byMessage *counter) error {
	j := newJSONWriter(o.stdout)
	j.begin()
	j.strS("report", "errors")
	j.numAlways("total", total)
	for _, s := range severityOrder {
		if s >= pglogwatch.SeverityWarning {
			j.numAlways(lowerASCII(s.String()), bySeverity[s])
		}
	}
	j.end()

	for _, g := range byMessage.top(o.top) {
		j.begin()
		j.strS("report", "errors.message")
		j.numAlways("count", g.count)
		j.strS("message", g.sample)
		j.end()
	}
	for _, g := range byState.top(o.top) {
		j.begin()
		j.strS("report", "errors.sqlstate")
		j.numAlways("count", g.count)
		j.strS("sqlstate", g.key)
		j.end()
	}
	return j.flush()
}

// reportDropped notes a truncated aggregation on stderr.
//
// It goes to stderr rather than stdout so it cannot corrupt --output json
// (IFC-011), and it is reported at all because a top-N drawn from a truncated
// set is a different claim from one drawn from everything.
func reportDropped(o *options, c *counter) {
	if c.dropped > 0 {
		o.stderr.Write([]byte("pglogwatch: " + itoa(c.dropped) +
			" rare groups were discarded to bound memory; counts of frequent groups are exact\n")) //nolint:errcheck // diagnostic
	}
}
