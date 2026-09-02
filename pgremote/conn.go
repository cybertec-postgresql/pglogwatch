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
	"time"

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

	// Follow keeps the stream open, waiting for new data and new files
	// instead of ending at the last byte of the last file.
	//
	// It is the remote counterpart of pglogwatch.FileSet.Follow, and it
	// exists for the same reason: a log reader that stops at the current end
	// of the log reports on the past and then goes silent. Without it a
	// caller has to reopen on a timer, and every reopen re-lists the
	// directory and re-enters this package cold.
	//
	// A following reader blocks in Read until there is something to deliver
	// or ctx is done, at which point it returns io.EOF like any other
	// exhausted reader.
	Follow bool

	// PollInterval is how often a following reader re-lists the directory
	// looking for new data or new files. Zero means one second, matching
	// pglogwatch.FileSet. Ignored when Follow is false.
	PollInterval time.Duration

	// Offsets records how far each file has been read, so a restart resumes
	// rather than re-reading (IFC-006). Nil means no persistence: every run
	// starts from the beginning of every file.
	//
	// Follow overrides that default: a follower re-lists the directory on
	// every poll, so without somewhere to record how far it has read it
	// would re-deliver every file from the start on every pass. When Follow
	// is set and this is nil, Open substitutes a bounded in-memory store,
	// which survives the reader but not the process.
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

// defaultPollInterval matches pglogwatch.FileSet's, so that a caller switching
// between the local and remote readers does not silently change how often the
// log is checked.
const defaultPollInterval = time.Second

func (c *Config) chunkSize() int64 {
	if c.ChunkSize > 0 {
		return c.ChunkSize
	}
	return defaultChunkSize
}

func (c *Config) pollInterval() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return defaultPollInterval
}

// maxTrackedFiles bounds the fallback offset store. A log directory holds one
// file per rotation period; 2500 is what pgwatch tracked before this module
// existed, and is far more than any retention policy leaves in place.
const maxTrackedFiles = 2500

// memOffsets is the in-memory OffsetStore a follower falls back to.
//
// It is bounded rather than growing forever: a long-lived follower watching a
// directory that rotates hourly would otherwise accumulate an entry per file
// for the life of the process. Clearing wholesale rather than evicting one
// entry is deliberate -- the entries this store holds are for files that have
// been fully read, so the cost of losing them is re-reading files that are by
// then almost certainly gone from the directory.
type memOffsets struct {
	m map[string]int64
}

func newMemOffsets() *memOffsets { return &memOffsets{m: make(map[string]int64)} }

func (o *memOffsets) Get(path string) (int64, bool) {
	off, ok := o.m[path]
	return off, ok
}

func (o *memOffsets) Set(path string, offset int64) {
	if len(o.m) >= maxTrackedFiles {
		clear(o.m)
	}
	o.m[path] = offset
}
