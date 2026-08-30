// Package bench holds the corpus generator and the comparative benchmark
// harness that measures pglogwatch against pgbadger and pgweasel.
//
// It lives in its own module because benchmark tooling must never appear in a
// consumer's dependency graph (PKG-004).
package bench
