package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["grep"] = command{
		summary: "record-aware search with -A, -B and -C context",
		flags: func(fs *flag.FlagSet, o *options) {
			fs.IntVar(&o.grepArgs.after, "A", 0, "records of trailing context")
			fs.IntVar(&o.grepArgs.before, "B", 0, "records of leading context")
			fs.IntVar(&o.grepArgs.context, "C", 0, "records of context on both sides")
			fs.BoolVar(&o.grepArgs.ignoreCase, "i", false, "case-insensitive match")
			fs.BoolVar(&o.grepArgs.invert, "v", false, "show records that do NOT match")
		},
		run: runGrep,
	}
}

var errNoPattern = errors.New("grep needs a pattern: pglogwatch grep <pattern> [paths...]")

// runGrep searches records rather than lines.
//
// That is the whole reason this exists next to grep(1). A PostgreSQL log
// record can span many physical lines -- an error with a DETAIL, a HINT and a
// wrapped multi-line statement -- and grep(1) shows you one of them. Matching
// per record means a hit on the message shows you the statement that caused
// it, and -A/-B count RECORDS, so "two records of context" is two events
// rather than two arbitrary lines of one.
func runGrep(o *options) error {
	if len(o.paths) == 0 {
		return errNoPattern
	}
	o.grepArgs.pattern = o.paths[0]
	o.paths = o.paths[1:]

	if o.grepArgs.context > 0 {
		o.grepArgs.before = o.grepArgs.context
		o.grepArgs.after = o.grepArgs.context
	}
	needle := []byte(o.grepArgs.pattern)
	if o.grepArgs.ignoreCase {
		needle = bytes.ToLower(needle)
	}

	var (
		j        *jsonWriter
		matches  int64
		pending  int // trailing context still owed
		ring     []*pglogwatch.OwnedRecord
		buf      []byte
		lastFmt  = pglogwatch.FormatAuto
		emitted  = map[int64]bool{}
		recordNo int64
	)
	if o.jsonOut {
		j = newJSONWriter(o.stdout)
	}

	emit := func(r *pglogwatch.Record, n int64, isMatch bool) {
		if emitted[n] {
			return
		}
		emitted[n] = true
		if j != nil {
			writeRecordJSON(j, r, lastFmt, &buf)
			return
		}
		marker := "  "
		if isMatch {
			marker = "> "
		}
		fmt.Fprintf(o.stdout, "%s%s\n", marker, oneLine(recordLine(r))) //nolint:errcheck // report output
	}
	emitOwned := func(r *pglogwatch.OwnedRecord, n int64) {
		emit(&r.Record, n, false)
	}

	err := o.eachRecordWithFormat(func(r *pglogwatch.Record, f pglogwatch.Format) error {
		lastFmt = f
		recordNo++
		n := recordNo

		hay := r.Raw
		if o.grepArgs.ignoreCase {
			buf = append(buf[:0], hay...)
			hay = bytes.ToLower(buf)
		}
		hit := bytes.Contains(hay, needle)
		if o.grepArgs.invert {
			hit = !hit
		}

		if hit {
			matches++
			// Leading context first, in order, then the match.
			for i, c := range ring {
				emitOwned(c, n-int64(len(ring)-i))
			}
			ring = ring[:0]
			emit(r, n, true)
			pending = o.grepArgs.after
			return nil
		}
		if pending > 0 {
			pending--
			emit(r, n, false)
			return nil
		}
		if o.grepArgs.before > 0 {
			// A ring of the last B records, so leading context costs
			// B records of memory rather than the whole log.
			if len(ring) == o.grepArgs.before {
				ring = ring[1:]
			}
			ring = append(ring, r.Clone())
		}
		return nil
	})
	if err != nil {
		if j != nil {
			_ = j.flush()
		}
		return err
	}
	if j != nil {
		return j.flush()
	}
	if matches == 0 {
		fmt.Fprintln(o.stdout, "(no matching records)") //nolint:errcheck // report output
	}
	return nil
}

// recordLine renders a record as one line for text output.
func recordLine(r *pglogwatch.Record) string {
	var sb strings.Builder
	if !r.Time.IsZero() {
		sb.WriteString(r.Time.Format("2006-01-02 15:04:05.000"))
		sb.WriteByte(' ')
	}
	if s := r.Severity.String(); s != "" {
		sb.WriteString(s)
		sb.WriteString(": ")
	}
	sb.Write(r.Message)
	for _, extra := range []struct {
		label string
		v     []byte
	}{
		{"DETAIL", r.Detail},
		{"HINT", r.Hint},
		{"STATEMENT", r.Statement},
	} {
		if len(extra.v) > 0 {
			sb.WriteString(" | ")
			sb.WriteString(extra.label)
			sb.WriteString(": ")
			sb.Write(extra.v)
		}
	}
	return sb.String()
}
