package main

import (
	"flag"
	"strconv"

	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["slow"] = command{
		summary: "statements above --min-duration: the slowest, and aggregates",
		flags: func(fs *flag.FlagSet, o *options) {
			fs.DurationVar(&o.minDur, "min-duration", 0,
				"only statements at least this slow, e.g. 500ms")
		},
		run: runSlow,
	}
}

// runSlow reports slow statements, both individually and grouped.
//
// Both views are needed and answer different questions. The slowest individual
// statements find the one query that blocked everything at 03:00; the grouped
// view finds the query that is only moderately slow but runs ten thousand
// times an hour, which is usually the more expensive problem and never appears
// in a list of the ten slowest.
func runSlow(o *options) error {
	byStatement := newCounter(o.top)
	slowest := newCounter(o.top)
	var count int64
	var totalMS int64
	var buf, normBuf []byte

	err := o.eachRecordWithFormat(func(r *pglogwatch.Record, f pglogwatch.Format) error {
		if r.Flags&pglogwatch.FlagHasDuration == 0 {
			return nil
		}
		if o.minDur > 0 && r.Duration < o.minDur {
			return nil
		}
		count++
		ms := r.Duration.Milliseconds()
		totalMS += ms

		text := statementText(r, f, &buf)
		normBuf = normalizeMessage(normBuf[:0], text)
		sample := func() string { return oneLine(truncate(string(text), 200)) }
		byStatement.addBytes(normBuf, ms, sample)
		slowest.addBytes(normBuf, ms, sample)
		return nil
	})
	if err != nil {
		return err
	}

	if o.jsonOut {
		return slowJSON(o, count, totalMS, byStatement, slowest)
	}
	slowText(o, count, totalMS, byStatement, slowest)
	return nil
}

// statementText prefers the statement a record is about, falling back to the
// message -- which for a "duration: N ms  statement: ..." record contains it.
func statementText(r *pglogwatch.Record, f pglogwatch.Format, buf *[]byte) []byte {
	if len(r.Statement) > 0 {
		return unquoted(r.Statement, r, f, buf)
	}
	return unquoted(r.Message, r, f, buf)
}

func msString(ms int64) string {
	return strconv.FormatFloat(float64(ms), 'f', 0, 64) + "ms"
}

func slowText(o *options, count, totalMS int64, byStatement, slowest *counter) {
	s := newTable(o.stdout, "statements", "total", "mean")
	mean := int64(0)
	if count > 0 {
		mean = totalMS / count
	}
	s.add(itoa(count), msString(totalMS), msString(mean))
	s.flush()

	o.stdout.Write([]byte("\nslowest single executions\n")) //nolint:errcheck // report output
	a := newTable(o.stdout, "slowest", "count", "statement")
	for _, g := range slowest.bySlowest(o.top) {
		a.add(msString(g.worst), itoa(g.count), g.sample)
	}
	a.flush()

	o.stdout.Write([]byte("\nmost total time\n")) //nolint:errcheck // report output
	b := newTable(o.stdout, "total", "count", "mean", "statement")
	for _, g := range byStatement.byTotal(o.top) {
		m := int64(0)
		if g.count > 0 {
			m = g.total / g.count
		}
		b.add(msString(g.total), itoa(g.count), msString(m), g.sample)
	}
	b.flush()
	reportDropped(o, byStatement)
}

func slowJSON(o *options, count, totalMS int64, byStatement, slowest *counter) error {
	j := newJSONWriter(o.stdout)
	j.begin()
	j.strS("report", "slow")
	j.numAlways("statements", count)
	j.numAlways("total_ms", totalMS)
	if count > 0 {
		j.numAlways("mean_ms", totalMS/count)
	}
	j.end()

	for _, g := range slowest.bySlowest(o.top) {
		j.begin()
		j.strS("report", "slow.slowest")
		j.numAlways("slowest_ms", g.worst)
		j.numAlways("count", g.count)
		j.strS("statement", g.sample)
		j.end()
	}
	for _, g := range byStatement.byTotal(o.top) {
		j.begin()
		j.strS("report", "slow.total")
		j.numAlways("total_ms", g.total)
		j.numAlways("count", g.count)
		j.strS("statement", g.sample)
		j.end()
	}
	return j.flush()
}
