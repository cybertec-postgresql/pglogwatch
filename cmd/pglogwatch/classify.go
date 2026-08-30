package main

import (
	"bytes"

	"github.com/cybertec-postgresql/pglogwatch"
)

// Message classification.
//
// PostgreSQL does not label the KIND of a log event -- there is no column
// saying "this is a checkpoint". Severity says how bad it is, not what it is,
// so every report that counts kinds has to recognise them from the message
// text. pgbadger and pgweasel both do the same thing for the same reason.
//
// The patterns below are anchored to the message prefixes PostgreSQL's own
// source uses, which are stable across versions in a way that free-text
// matching is not. Where a match would be ambiguous the SQLSTATE decides
// instead, since it is machine-readable and PostgreSQL guarantees it.

// kind is what a record is about.
type kind uint8

const (
	kindOther kind = iota
	kindConnection
	kindDisconnection
	kindCheckpoint
	kindAutovacuum
	kindTempFile
	kindLockWait
	kindDeadlock
	kindRecoveryConflict
	kindSystem
	kindSlow
)

// SQLSTATEs that identify a kind more reliably than any message text.
var (
	sqlstateDeadlock          = [5]byte{'4', '0', 'P', '0', '1'}
	sqlstateQueryCanceled     = [5]byte{'5', '7', '0', '1', '4'}
	sqlstateAdminShutdown     = [5]byte{'5', '7', 'P', '0', '1'}
	sqlstateCrashShutdown     = [5]byte{'5', '7', 'P', '0', '2'}
	sqlstateCannotConnectNow  = [5]byte{'5', '7', 'P', '0', '3'}
	sqlstateDatabaseDropped   = [5]byte{'5', '7', 'P', '0', '4'}
	sqlstateInvalidPassword   = [5]byte{'2', '8', 'P', '0', '1'}
	sqlstateInvalidAuthSpec   = [5]byte{'2', '8', '0', '0', '0'}
	sqlstateSerialisationFail = [5]byte{'4', '0', '0', '0', '1'}
)

// classify decides what a record is about.
//
// Order matters: SQLSTATE first where one is decisive, then message prefixes
// from most to least specific. "temporary file" would otherwise be caught by
// nothing and "checkpoint complete" by the same rule as "checkpoint starting",
// which the reports need to tell apart.
func classify(r *pglogwatch.Record) kind {
	switch r.SQLState {
	case sqlstateDeadlock:
		return kindDeadlock
	case sqlstateQueryCanceled:
		// Cancelled by the user or by a recovery conflict; the message
		// distinguishes them.
		if bytes.Contains(r.Message, []byte("recovery")) {
			return kindRecoveryConflict
		}
		return kindOther
	case sqlstateAdminShutdown, sqlstateCrashShutdown,
		sqlstateCannotConnectNow, sqlstateDatabaseDropped:
		return kindSystem
	case sqlstateInvalidPassword, sqlstateInvalidAuthSpec:
		return kindConnection
	case sqlstateSerialisationFail:
		return kindLockWait
	}

	m := r.Message
	switch {
	case hasPrefixB(m, "connection authorized"),
		hasPrefixB(m, "connection received"),
		hasPrefixB(m, "connection authenticated"):
		return kindConnection
	case hasPrefixB(m, "disconnection"):
		return kindDisconnection
	case hasPrefixB(m, "checkpoint starting"),
		hasPrefixB(m, "checkpoint complete"),
		hasPrefixB(m, "restartpoint starting"),
		hasPrefixB(m, "restartpoint complete"):
		return kindCheckpoint
	case hasPrefixB(m, "automatic vacuum"),
		hasPrefixB(m, "automatic analyze"),
		hasPrefixB(m, "automatic aggressive vacuum"):
		return kindAutovacuum
	case hasPrefixB(m, "temporary file:"):
		return kindTempFile
	case bytes.Contains(m, []byte("still waiting for")),
		bytes.Contains(m, []byte("acquired ")) && bytes.Contains(m, []byte("Lock on")):
		return kindLockWait
	case bytes.Contains(m, []byte("canceling statement due to conflict with recovery")):
		return kindRecoveryConflict
	case hasPrefixB(m, "database system is"),
		hasPrefixB(m, "database system was"),
		hasPrefixB(m, "the database system is"),
		hasPrefixB(m, "received "),
		hasPrefixB(m, "starting PostgreSQL"),
		hasPrefixB(m, "listening on"),
		hasPrefixB(m, "shutting down"),
		hasPrefixB(m, "aborting any active transactions"),
		hasPrefixB(m, "background worker"),
		hasPrefixB(m, "server process"),
		hasPrefixB(m, "terminating"):
		return kindSystem
	}

	if r.Flags&pglogwatch.FlagHasDuration != 0 {
		return kindSlow
	}
	return kindOther
}

func hasPrefixB(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}

// connectionUser extracts the user from a connection message when the record's
// own field is empty.
//
// PostgreSQL writes "connection authorized: user=app database=appdb" from a
// backend that does not yet have a user set in its log_line_prefix, so on
// stderr the field is often blank while the message has the answer.
func connectionUser(r *pglogwatch.Record) []byte {
	if len(r.User) > 0 {
		return r.User
	}
	return fieldAfter(r.Message, "user=")
}

// connectionDatabase is connectionUser for the database name.
func connectionDatabase(r *pglogwatch.Record) []byte {
	if len(r.Database) > 0 {
		return r.Database
	}
	return fieldAfter(r.Message, "database=")
}

// fieldAfter returns the space-delimited value following a "key=" marker.
func fieldAfter(m []byte, key string) []byte {
	i := bytes.Index(m, []byte(key))
	if i < 0 {
		return nil
	}
	rest := m[i+len(key):]
	if j := bytes.IndexAny(rest, " ,"); j >= 0 {
		return rest[:j]
	}
	return rest
}
