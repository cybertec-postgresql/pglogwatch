package pglogwatch

import (
	"context"
	"io"
	"time"
)

// defaultPollInterval is how often a following FileSet looks for new data or
// new files.
//
// A log directory is not a high-frequency event source: PostgreSQL writes when
// something happens, and a consumer counting severities does not need
// sub-second latency. One second keeps a follower's idle cost to one readdir
// per second per directory.
const defaultPollInterval = time.Second

// FileSet resolves a directory of PostgreSQL log files into a single ordered
// byte stream, optionally following it as the server writes.
//
// It is the "watch" in pglogwatch, and it is built on the parser rather than
// the other way round: it produces an [io.Reader] and knows nothing about
// records (PAT-004).
type FileSet struct {
	// Dir is the directory to read, normally the server's log_directory.
	Dir string

	// Glob selects files within Dir. Empty means the conventional pattern
	// for the configured format: *.csv, *.json or *.log.
	Glob string

	// Follow keeps the stream open, waiting for new data and new files
	// instead of ending at the last byte of the last file.
	Follow bool

	// TruncateOnRotation tells the reader that the server has
	// log_truncate_on_rotation on, so a file may be emptied and reused
	// under its existing name (E15).
	//
	// Rotation is detected regardless of this setting; it exists so a
	// follower can distinguish "no new data yet" from "this file was
	// replaced" without waiting for evidence to accumulate.
	TruncateOnRotation bool

	// PollInterval is how often a following reader checks for new data or
	// new files. Zero means one second.
	PollInterval time.Duration

	// Offsets persists how far each file has been read, so a restart
	// resumes rather than re-reading (IFC-006). Nil means a bounded
	// in-memory store, which survives a Reset but not a process restart.
	Offsets OffsetStore

	// Format selects the default Glob. It does not affect parsing, which is
	// the Parser's business; a FileSet only decides which files to open.
	Format Format
}

// Open returns a reader over the file set.
//
// The reader hands the parser one continuous stream across file boundaries and
// across rotation, and while following it withholds a partially written
// trailing record rather than emitting half of one (IFC-005). Close it when
// done; a following reader stops at the next poll after ctx is cancelled.
func (fs *FileSet) Open(ctx context.Context) (io.ReadCloser, error) {
	return fs.open(ctx)
}

// glob returns the effective filename pattern.
func (fs *FileSet) glob() string {
	if fs.Glob != "" {
		return fs.Glob
	}
	return fs.Format.defaultGlob()
}

// pollInterval returns the effective poll interval.
func (fs *FileSet) pollInterval() time.Duration {
	if fs.PollInterval > 0 {
		return fs.PollInterval
	}
	return defaultPollInterval
}

// offsets returns the effective offset store, creating the bounded in-memory
// default on first use.
func (fs *FileSet) offsets() OffsetStore {
	if fs.Offsets == nil {
		fs.Offsets = newMemoryOffsetStore(defaultMaxTrackedFiles)
	}
	return fs.Offsets
}
