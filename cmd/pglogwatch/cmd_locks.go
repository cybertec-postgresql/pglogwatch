package main

import (
	"bytes"

	"github.com/cybertec-postgresql/pglogwatch"
)

func init() {
	commands["locks"] = command{
		summary: "lock waits, deadlocks and recovery conflicts",
		flags:   noFlags,
		run:     runLocks,
	}
}

// runLocks reports contention.
//
// The three kinds are reported separately because they mean different things
// operationally: a lock wait is a query that eventually proceeded, a deadlock
// is a transaction PostgreSQL killed to break a cycle, and a recovery conflict
// is a standby cancelling a read to keep up with its primary. One combined
// "contention" number would hide which of those is actually happening, and the
// three have entirely different remedies.
func runLocks(o *options) error {
	var waits, deadlocks, conflicts int64
	byTarget := newCounter(o.top)
	byDatabase := newCounter(o.top)
	samples := newCounter(o.top)
	var buf, normBuf []byte

	err := o.eachRecordWithFormat(func(r *pglogwatch.Record, f pglogwatch.Format) error {
		switch classify(r) {
		case kindLockWait:
			waits++
		case kindDeadlock:
			deadlocks++
		case kindRecoveryConflict:
			conflicts++
		default:
			return nil
		}
		addIfPresent(byDatabase, r.Database)
		if t := lockTarget(r.Message); len(t) > 0 {
			byTarget.add(string(t), 0, "")
		}
		msg := unquoted(r.Message, r, f, &buf)
		normBuf = normalizeMessage(normBuf[:0], msg)
		samples.add(string(normBuf), 0, oneLine(truncate(string(msg), 200)))
		return nil
	})
	if err != nil {
		return err
	}

	if o.jsonOut {
		return locksJSON(o, waits, deadlocks, conflicts, byDatabase, byTarget, samples)
	}
	locksText(o, waits, deadlocks, conflicts, byDatabase, byTarget, samples)
	return nil
}

// lockTarget extracts the kind of lock being waited on, e.g.
// "ShareLock on transaction".
//
// The specific object is a transaction id or a tuple that differs on every
// occurrence, so the lock TYPE is the part worth grouping by -- grouping on the
// whole phrase would give one group per wait.
func lockTarget(m []byte) []byte {
	const marker = "waiting for "
	i := bytes.Index(m, []byte(marker))
	if i < 0 {
		return nil
	}
	rest := m[i+len(marker):]
	if j := bytes.Index(rest, []byte(" on ")); j >= 0 {
		end := j + len(" on ")
		if k := bytes.IndexByte(rest[end:], ' '); k >= 0 {
			return rest[:end+k]
		}
		return rest
	}
	if j := bytes.IndexByte(rest, ' '); j >= 0 {
		return rest[:j]
	}
	return rest
}

func locksText(o *options, waits, deadlocks, conflicts int64,
	byDatabase, byTarget, samples *counter,
) {
	t := newTable(o.stdout, "event", "count")
	t.add("lock waits", itoa(waits))
	t.add("deadlocks", itoa(deadlocks))
	t.add("recovery conflicts", itoa(conflicts))
	t.flush()

	for _, sec := range []struct {
		title string
		c     *counter
		text  bool
	}{
		{"waiting for", byTarget, false},
		{"database", byDatabase, false},
		{"message", samples, true},
	} {
		if len(sec.c.groups) == 0 {
			continue
		}
		o.stdout.Write([]byte("\n")) //nolint:errcheck // report output
		tab := newTable(o.stdout, "count", sec.title)
		for _, g := range sec.c.top(o.top) {
			if sec.text {
				tab.add(itoa(g.count), g.sample)
			} else {
				tab.add(itoa(g.count), g.key)
			}
		}
		tab.flush()
	}
}

func locksJSON(o *options, waits, deadlocks, conflicts int64,
	byDatabase, byTarget, samples *counter,
) error {
	j := newJSONWriter(o.stdout)
	j.begin()
	j.strS("report", "locks")
	j.numAlways("lock_waits", waits)
	j.numAlways("deadlocks", deadlocks)
	j.numAlways("recovery_conflicts", conflicts)
	j.end()

	for _, g := range byTarget.top(o.top) {
		j.begin()
		j.strS("report", "locks.target")
		j.numAlways("count", g.count)
		j.strS("waiting_for", g.key)
		j.end()
	}
	for _, g := range byDatabase.top(o.top) {
		j.begin()
		j.strS("report", "locks.database")
		j.numAlways("count", g.count)
		j.strS("database", g.key)
		j.end()
	}
	for _, g := range samples.top(o.top) {
		j.begin()
		j.strS("report", "locks.message")
		j.numAlways("count", g.count)
		j.strS("message", g.sample)
		j.end()
	}
	return j.flush()
}
