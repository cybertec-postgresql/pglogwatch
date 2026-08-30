package main

import (
	"strconv"

	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["stats"] = command{
		summary: "counts of errors, connections, checkpoints, autovacuums, temp files",
		flags:   noFlags,
		run:     runStats,
	}
}

// severityOrder is the order severities are reported in: most serious first,
// which is the order someone reads a summary in.
var severityOrder = []pglogwatch.Severity{
	pglogwatch.SeverityPanic,
	pglogwatch.SeverityFatal,
	pglogwatch.SeverityError,
	pglogwatch.SeverityWarning,
	pglogwatch.SeverityNotice,
	pglogwatch.SeverityInfo,
	pglogwatch.SeverityLog,
	pglogwatch.SeverityDebug1,
	pglogwatch.SeverityDebug2,
	pglogwatch.SeverityDebug3,
	pglogwatch.SeverityDebug4,
	pglogwatch.SeverityDebug5,
}

// runStats is §4.8's overview, and the workload AC-010 compares against
// pgbadger and pgweasel: per-severity counts plus the event kinds an operator
// looks at first.
//
// Counting is O(1) in memory -- fixed arrays, no per-message state -- so it is
// the report that can run over any size of log (PERF-026).
func runStats(o *options) error {
	var (
		bySeverity [16]int64
		byKind     [16]int64
		total      int64
		unknown    int64
	)

	err := o.eachRecord(func(r *pglogwatch.Record) error {
		total++
		if int(r.Severity) < len(bySeverity) {
			bySeverity[r.Severity]++
		}
		if r.Severity == pglogwatch.SeverityUnknown {
			unknown++
		}
		if k := classify(r); int(k) < len(byKind) {
			byKind[k]++
		}
		return nil
	})
	if err != nil {
		return err
	}

	if o.jsonOut {
		return statsJSON(o, total, unknown, &bySeverity, &byKind)
	}
	statsText(o, total, unknown, &bySeverity, &byKind)
	return nil
}

// kindRows names the event kinds worth reporting, in the order §4.8 lists them.
var kindRows = []struct {
	name string
	k    kind
}{
	{"connections", kindConnection},
	{"disconnections", kindDisconnection},
	{"checkpoints", kindCheckpoint},
	{"autovacuums", kindAutovacuum},
	{"temp files", kindTempFile},
	{"lock waits", kindLockWait},
	{"deadlocks", kindDeadlock},
	{"recovery conflicts", kindRecoveryConflict},
	{"system events", kindSystem},
	{"slow statements", kindSlow},
}

func statsText(o *options, total, unknown int64, bySeverity, byKind *[16]int64) {
	t := newTable(o.stdout, "severity", "count")
	for _, s := range severityOrder {
		if n := bySeverity[s]; n > 0 {
			t.add(s.String(), strconv.FormatInt(n, 10))
		}
	}
	if unknown > 0 {
		// Reported rather than hidden: a nonzero count here usually
		// means --lang does not match the server's lc_messages, and
		// silently showing fewer errors than the log contains is the
		// failure mode worth surfacing.
		t.add("(unrecognised)", strconv.FormatInt(unknown, 10))
	}
	t.add("total", strconv.FormatInt(total, 10))
	t.flush()

	k := newTable(o.stdout, "event", "count")
	for _, row := range kindRows {
		k.add(row.name, strconv.FormatInt(byKind[row.k], 10))
	}
	o.stdout.Write([]byte("\n")) //nolint:errcheck // report output
	k.flush()
}

func statsJSON(o *options, total, unknown int64, bySeverity, byKind *[16]int64) error {
	j := newJSONWriter(o.stdout)
	j.begin()
	j.strS("report", "stats")
	j.numAlways("records", total)
	j.numAlways("unrecognised_severity", unknown)
	for _, s := range severityOrder {
		j.numAlways(lowerASCII(s.String()), bySeverity[s])
	}
	for _, row := range kindRows {
		j.numAlways(jsonKey(row.name), byKind[row.k])
	}
	j.end()
	return j.flush()
}

// lowerASCII lower-cases a severity name.
//
// The lower-case spelling matches the column names pgwatch's
// server_log_event_counts measurement uses (CON-007), so the JSON report and
// that measurement can be compared without translating names.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// jsonKey turns a display name into a key: lower case, underscores for spaces.
func jsonKey(name string) string {
	b := []byte(lowerASCII(name))
	for i, c := range b {
		if c == ' ' {
			b[i] = '_'
		}
	}
	return string(b)
}
