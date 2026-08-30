package pglogwatch

import (
	"context"
	"io"
	"sync"
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

	// mu guards ids, which readers update as they open files.
	mu sync.Mutex

	// ids remembers which file each path referred to when it was last read.
	//
	// A stored offset is only meaningful for the file it was taken from,
	// and a path is not a file: rotation can replace the file behind a name
	// while the name stays put. Comparing identities is what makes that
	// detectable when the size test cannot see it (COR-007).
	ids map[string]fileIdentity
}

// rememberIdentity records which file a path referred to.
func (fs *FileSet) rememberIdentity(path string, id fileIdentity) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.ids == nil {
		fs.ids = make(map[string]fileIdentity)
	}
	fs.ids[path] = id
}

// isSameFileAsLastTime reports whether a path still refers to the file it did
// when it was last read. An unknown path counts as the same, since there is
// nothing to contradict.
func (fs *FileSet) isSameFileAsLastTime(path string, id fileIdentity) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	prev, ok := fs.ids[path]
	if !ok {
		return true
	}
	return prev.sameFile(id)
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
