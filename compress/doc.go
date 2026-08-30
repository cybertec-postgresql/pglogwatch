// Package compress opens PostgreSQL log files that have been compressed after
// rotation, presenting them to the parser as plain io.Reader streams.
//
// It lives in its own module so that consumers of the root pglogwatch package
// never inherit a compression dependency (PKG-004).
package compress
