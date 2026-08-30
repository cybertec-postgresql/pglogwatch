// Package pgremote reads PostgreSQL log files over a database connection using
// pg_ls_logdir() and pg_read_file(), for the case where the log directory is
// not reachable on the local filesystem.
//
// It lives in its own module so that consumers of the root pglogwatch package
// never inherit a pgx dependency (PKG-004).
package pgremote
