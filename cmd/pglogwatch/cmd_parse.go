package main

import (
	"fmt"

	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["parse"] = command{
		summary: "emit every record as NDJSON",
		flags:   noFlags,
		run:     runParse,
	}
}

// runParse is §4.8's canonical machine output: one JSON object per record,
// every field the parser found.
//
// It is the subcommand the others are checked against -- a report that
// disagrees with parse is wrong about the log rather than about its own
// aggregation -- and it is what a caller pipes into jq when no built-in report
// answers their question.
//
// Records are written as they are parsed rather than collected first, so
// memory stays flat over a 10 GB log (PERF-026).
func runParse(o *options) error {
	j := newJSONWriter(o.stdout)
	var buf []byte
	format := o.cfg.Format

	err := o.eachRecordWithFormat(func(r *pglogwatch.Record, f pglogwatch.Format) error {
		format = f
		writeRecordJSON(j, r, format, &buf)
		return nil
	})
	if err != nil {
		_ = j.flush()
		return err
	}
	if err := j.flush(); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	return nil
}
