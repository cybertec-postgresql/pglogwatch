package main

import (
	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["system"] = command{
		summary: "server lifecycle and internal events",
		flags:   noFlags,
		run:     runSystem,
	}
}

// runSystem reports the server's own events: startup, shutdown, recovery,
// checkpoints, autovacuum, temp files and background worker activity.
//
// Unlike the other reports this one lists events in TIME ORDER rather than by
// frequency. A restart is interesting because of when it happened and what
// surrounded it, and a frequency table of lifecycle events -- "3 shutdowns" --
// tells an operator nothing they can act on.
func runSystem(o *options) error {
	type event struct {
		r      *pglogwatch.OwnedRecord
		reason string
	}
	var events []event
	var checkpoints, autovacuums, tempFiles int64
	var tempBytes int64

	err := o.eachRecord(func(r *pglogwatch.Record) error {
		switch classify(r) {
		case kindSystem:
			// Retained, so it can be listed after the scan. This is
			// the one report that keeps records, and it is bounded
			// by --top rather than by the log's size.
			if len(events) < o.top*trackingFactor {
				events = append(events, event{r: r.Clone(), reason: "lifecycle"})
			}
		case kindCheckpoint:
			checkpoints++
		case kindAutovacuum:
			autovacuums++
		case kindTempFile:
			tempFiles++
			tempBytes += tempFileSize(r.Message)
		default:
			// A PANIC or FATAL from any source is a system event
			// whatever its message says: it is the server telling
			// you it is in trouble.
			if r.Severity >= pglogwatch.SeverityFatal && len(events) < o.top*trackingFactor {
				events = append(events, event{r: r.Clone(), reason: r.Severity.String()})
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(events) > o.top {
		events = events[:o.top]
	}

	if o.jsonOut {
		j := newJSONWriter(o.stdout)
		j.begin()
		j.strS("report", "system")
		j.numAlways("checkpoints", checkpoints)
		j.numAlways("autovacuums", autovacuums)
		j.numAlways("temp_files", tempFiles)
		j.numAlways("temp_bytes", tempBytes)
		j.end()
		for _, e := range events {
			j.begin()
			j.strS("report", "system.event")
			j.time("time", e.r.Time)
			j.strS("severity", e.r.Severity.String())
			j.strS("message", oneLine(string(e.r.Message)))
			j.end()
		}
		return j.flush()
	}

	t := newTable(o.stdout, "event", "count")
	t.add("checkpoints", itoa(checkpoints))
	t.add("autovacuums", itoa(autovacuums))
	t.add("temp files", itoa(tempFiles))
	t.add("temp bytes", itoa(tempBytes))
	t.flush()

	o.stdout.Write([]byte("\n")) //nolint:errcheck // report output
	e := newTable(o.stdout, "time", "severity", "message")
	for _, ev := range events {
		when := ""
		if !ev.r.Time.IsZero() {
			when = ev.r.Time.Format("2006-01-02 15:04:05")
		}
		e.add(when, ev.r.Severity.String(), oneLine(truncate(string(ev.r.Message), 120)))
	}
	e.flush()
	return nil
}

// tempFileSize extracts the byte count from a temporary file message.
//
// PostgreSQL writes: temporary file: path "...", size 1048576. Reporting the
// total is what makes the count actionable -- a thousand small spills and one
// enormous one need different responses.
func tempFileSize(m []byte) int64 {
	v := fieldAfter(m, "size ")
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
