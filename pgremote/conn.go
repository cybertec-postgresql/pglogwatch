// Package pgremote reads PostgreSQL log files over a database connection,
// using pg_ls_logdir() and pg_read_file(), for deployments where the log
// directory is not on the local filesystem.
//
// It lives in its own module so that consumers of the root pglogwatch package
// never inherit a pgx dependency (PKG-004). The parser itself never connects to
// anything (CON-005); this package is the only part of pglogwatch that does.
//
// # Privileges
//
// pg_read_file is restricted to superusers and to members of
// pg_read_server_files, and pg_ls_logdir to superusers and pg_monitor. A
// deployment that cannot grant those should read the files locally instead.
package pgremote

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Conn is the part of a pgx connection this package uses.
//
// It is an interface rather than a *pgx.Conn so that a caller can pass a
// connection, a pool, or a mock without this package caring which. pgwatch
// already holds pools rather than connections, and a package that demanded a
// bare *pgx.Conn would force it to check one out and hold it for the length of
// a log scan.
type Conn interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Config configures a remote reader.
type Config struct {
	// Dir is the server's log directory, as resolved from log_directory and
	// data_directory. Required.
	Dir string

	// Glob selects files within Dir. Empty means every file pg_ls_logdir
	// reports, which is what a caller that does not know the destination
	// wants.
	Glob string

	// ChunkSize is how many bytes each pg_read_file call fetches. Zero means
	// 10 MiB, matching pgwatch's maxChunkSize.
	//
	// The value is a trade between round trips and memory: the server
	// materialises the whole chunk as a value before sending it, so a large
	// chunk costs the SERVER that much memory, not just this process.
	ChunkSize int64

	// Offsets records how far each file has been read, so a restart resumes
	// rather than re-reading (IFC-006). Nil means no persistence: every run
	// starts from the beginning of every file.
	Offsets OffsetStore
}

// OffsetStore persists how far each remote file has been read.
//
// It is declared here rather than imported from the root package so that this
// module's API does not depend on the root module's version. The shape is
// identical, so pglogwatch's in-memory store satisfies it directly.
type OffsetStore interface {
	Get(path string) (offset int64, ok bool)
	Set(path string, offset int64)
}

const defaultChunkSize = 10 << 20

func (c *Config) chunkSize() int64 {
	if c.ChunkSize > 0 {
		return c.ChunkSize
	}
	return defaultChunkSize
}
