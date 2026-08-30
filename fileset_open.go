package pglogwatch

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// The FileSet reader.
//
// It presents a directory of log files to the parser as one continuous stream.
// Three things make that harder than concatenating files:
//
//   - the last file is being written while it is read, so its tail is a
//     partial record that must be withheld rather than delivered (IFC-005);
//   - files are rotated away and new ones appear, so the set changes under the
//     reader;
//   - a restart must resume where the last one stopped, per file, by byte
//     offset (IFC-006).
//
// The reader delivers only COMPLETE LINES. That is what lets it withhold a
// partial trailing record, and it is also what makes the offsets it records
// resumable: a line boundary is a boundary the parser can restart from.
//
// The granularity is lines rather than records, and that is a deliberate
// limit worth knowing. For a multi-line record -- a csvlog message containing
// newlines, or a stderr statement wrapped over several lines -- an interrupted
// read may record an offset inside the record. On resume the parser's
// resynchronisation skips to the next record start, so that one record is lost
// rather than duplicated. Losing at most one record per interruption is the
// right trade for a severity counter; a reader that understood records would
// have to embed a parser, which PAT-004 exists to prevent.

// fileSetReader is the io.ReadCloser returned by FileSet.Open.
type fileSetReader struct {
	fs  *FileSet
	ctx context.Context //nolint:containedctx // the reader outlives each Read

	cur      *os.File // the file being read, nil between files
	curPath  string
	curID    fileIdentity
	curPos   int64 // byte offset within cur of the next byte to deliver
	done     map[string]bool
	pending  []byte // bytes read from cur but not yet a complete line
	finished bool
	closed   bool
	closeMu  sync.Mutex
	scratch  []byte
}

func (fs *FileSet) open(ctx context.Context) (io.ReadCloser, error) {
	// Fail loudly on a directory that does not exist. A wrong log_directory
	// is a configuration problem the caller has to see; reporting no
	// records would look exactly like a quiet server.
	if _, err := os.Stat(fs.Dir); err != nil {
		return nil, err
	}
	return &fileSetReader{
		fs:      fs,
		ctx:     ctx,
		done:    make(map[string]bool),
		scratch: make([]byte, 32<<10),
	}, nil
}

// Read implements io.Reader over the whole file set.
func (r *fileSetReader) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, io.EOF // a cancelled follower ends cleanly
		}
		if n := r.drainPending(p); n > 0 {
			return n, nil
		}
		if r.finished {
			return 0, io.EOF
		}
		if r.cur == nil {
			if !r.openNext() {
				if !r.fs.Follow {
					r.finished = true
					return 0, io.EOF
				}
				if !r.wait() {
					return 0, io.EOF
				}
				continue
			}
		}
		if !r.fill() {
			continue
		}
	}
}

// drainPending copies as much of a COMPLETE line region as fits in p.
//
// Only bytes up to the last newline are eligible. Whatever follows is a
// partially written record and stays in the buffer until the rest of it
// arrives (IFC-005).
func (r *fileSetReader) drainPending(p []byte) int {
	end := lastNewline(r.pending)
	if end < 0 {
		return 0
	}
	n := copy(p, r.pending[:end+1])
	r.pending = r.pending[n:]
	if len(r.pending) == 0 {
		r.pending = r.pending[:0]
	}
	// Record how far this file has been consumed. curPos already counts
	// what was read from disk, so subtract what is still held back.
	if r.curPath != "" {
		r.fs.offsets().Set(r.curPath, r.curPos-int64(len(r.pending)))
	}
	return n
}

// fill reads more of the current file. It reports whether progress was made.
func (r *fileSetReader) fill() bool {
	n, err := r.cur.Read(r.scratch)
	if n > 0 {
		r.pending = append(r.pending, r.scratch[:n]...)
		r.curPos += int64(n)
		return true
	}
	if err == nil {
		return true // a short read with no error; try again
	}

	// End of this file. When following, the last file stays open: more may
	// be appended to it. Any earlier file is finished.
	if r.fs.Follow && r.isNewestFile() {
		return r.wait()
	}
	r.closeCurrent()
	return true
}

// openNext opens the next unread file, and reports whether it found one.
func (r *fileSetReader) openNext() bool {
	paths, err := filepath.Glob(filepath.Join(r.fs.Dir, r.fs.glob()))
	if err != nil {
		return false
	}
	slices.Sort(paths)

	for _, path := range paths {
		if r.done[path] {
			// A file already finished in this session may still be
			// the one being appended to, if it is the newest and a
			// follower is waiting on it. Otherwise skip it.
			continue
		}
		offset, _ := r.fs.offsets().Get(path)
		id, err := identify(path)
		if err != nil {
			continue // vanished between the glob and the stat
		}
		if offset > 0 && id.size <= offset {
			if id.size == offset {
				continue // nothing new since last time
			}
			// The file shrank: truncated and reused under the same
			// name, so the stored offset belongs to a file that no
			// longer exists (E15).
			offset = 0
		}
		f, err := openFileAt(path, offset)
		if err != nil {
			continue
		}
		pos, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			_ = f.Close()
			continue
		}
		r.cur, r.curPath, r.curID, r.curPos = f, path, id, pos
		return true
	}
	return false
}

// isNewestFile reports whether the open file is the last one in the set, which
// is the one PostgreSQL is currently writing to.
func (r *fileSetReader) isNewestFile() bool {
	paths, err := filepath.Glob(filepath.Join(r.fs.Dir, r.fs.glob()))
	if err != nil || len(paths) == 0 {
		return false
	}
	slices.Sort(paths)
	return paths[len(paths)-1] == r.curPath
}

// closeCurrent finishes with the open file.
func (r *fileSetReader) closeCurrent() {
	if r.cur == nil {
		return
	}
	_ = r.cur.Close()
	r.done[r.curPath] = true
	r.cur, r.curPath, r.curID = nil, "", fileIdentity{}
	r.curPos = 0
}

// wait sleeps one poll interval. It reports whether the reader should carry on.
func (r *fileSetReader) wait() bool {
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(r.fs.pollInterval()):
		return true
	}
}

// Close releases the open file. It is safe to call more than once, because a
// caller that has already drained the stream has no way to know whether the
// reader closed the last file itself.
func (r *fileSetReader) Close() error {
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.cur != nil {
		err := r.cur.Close()
		r.cur = nil
		return err
	}
	return nil
}

// lastNewline returns the index of the final newline in b, or -1.
func lastNewline(b []byte) int { return bytes.LastIndexByte(b, 10) }
