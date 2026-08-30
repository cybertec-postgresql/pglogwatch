package pgremote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Remote log reading.
//
// PostgreSQL exposes its log directory through two functions: pg_ls_logdir()
// lists it, and pg_read_file(path, offset, length) returns a slice of a file.
// The second is why byte offsets run through this whole module (§7.6) -- the
// remote interface is offset-based, so a line-counting design would have to
// convert, and could not.

// ErrNoDir reports a Config with no Dir set.
var ErrNoDir = errors.New("pgremote: Config.Dir is required")

// listLogDir names the files in the server's log directory, with their sizes.
//
// pg_ls_logdir returns names relative to log_directory, not full paths, which
// is why Config.Dir is needed separately even though the server knows it.
//
// The size is what makes remote reading compose with the local semantics
// (IFC-006, COR-007): it is the only evidence available here that a file has
// nothing new, or that it has been truncated and reused. There is no remote
// equivalent of the content fingerprint the local reader uses, so a rotation
// that replaces a file with one of the same size is not detectable from here
// -- that limitation is documented on Open.
const listLogDir = `SELECT name, size FROM pg_ls_logdir() ORDER BY name`

// readChunk fetches a slice of one file.
//
// pg_read_file returns text, so a byte that is not valid in the server
// encoding would fail the read. That is the documented interface and there is
// no bytea equivalent; a log containing invalid UTF-8 (COR-005, E11) can
// therefore only be read locally. The limitation is recorded on Open.
const readChunk = `SELECT pg_read_file($1, $2, $3)`

// Open returns a reader over the server's log directory.
//
// Files are read oldest first, each from its stored offset, and concatenated
// into one stream for a parser -- the same shape pglogwatch.FileSet produces
// locally, so a caller can switch between them without changing anything else.
//
// # Limitations
//
// pg_read_file returns text rather than bytea, so a log containing bytes that
// are invalid in the server encoding cannot be read this way. That is a
// property of PostgreSQL's interface, not of this package; such a log has to
// be read from the filesystem.
//
// The returned reader cannot seek. Resumption works by offset through the
// OffsetStore instead, which is what pg_read_file's own interface supports.
//
// Rotation is detected by SIZE only: a file shorter than its stored offset is
// re-read from the start, and one whose size equals its offset is skipped.
// Unlike the local reader there is no content fingerprint available here, so a
// rotation that replaces a file with a different one of exactly the same size
// is not detectable remotely. That is rare -- log files that differ are almost
// never byte-identical in length -- but it is a real difference between the
// two readers and not a bug to be found later.
func Open(ctx context.Context, conn Conn, cfg Config) (io.ReadCloser, error) {
	if cfg.Dir == "" {
		return nil, ErrNoDir
	}
	names, err := listNames(ctx, conn, cfg)
	if err != nil {
		return nil, err
	}
	return &remoteReader{
		ctx:   ctx,
		conn:  conn,
		cfg:   cfg,
		names: names,
	}, nil
}

func listNames(ctx context.Context, conn Conn, cfg Config) ([]remoteFile, error) {
	rows, err := conn.Query(ctx, listLogDir)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []remoteFile
	for rows.Next() {
		var f remoteFile
		if err := rows.Scan(&f.name, &f.size); err != nil {
			return nil, err
		}
		name := f.name
		if cfg.Glob != "" {
			ok, err := path.Match(cfg.Glob, name)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		names = append(names, f)
	}
	return names, rows.Err()
}

// remoteFile is one entry from pg_ls_logdir().
type remoteFile struct {
	name string
	size int64
}

// remoteReader streams the log directory over the connection.
type remoteReader struct {
	ctx  context.Context //nolint:containedctx // the reader outlives each Read
	conn Conn
	cfg  Config

	names []remoteFile
	idx   int

	curPath string
	curOff  int64 // offset within the current file of the next byte to fetch

	pending []byte // fetched but not yet delivered
	flush   bool   // the current file is finished; deliver what is left
	done    bool
}

func (r *remoteReader) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if n := r.drain(p); n > 0 {
			return n, nil
		}
		if r.done {
			return 0, io.EOF
		}
		if r.curPath == "" && !r.nextFile() {
			r.done = true
			continue
		}
		if err := r.fetch(); err != nil {
			return 0, err
		}
	}
}

// drain copies out complete lines only.
//
// IFC-009: a chunk boundary falls wherever pg_read_file's length lands, which
// is almost never a record boundary. Handing the parser a chunk as it arrives
// would split a record roughly once per chunk -- on a 10 MiB chunk over a
// multi-gigabyte log, hundreds of corrupted records. The tail of a chunk is
// therefore held back and prepended to the next one.
func (r *remoteReader) drain(p []byte) int {
	end := bytes.LastIndexByte(r.pending, '\n')
	if end < 0 {
		switch {
		case r.flush && len(r.pending) > 0:
			// End of file: there will never be a newline, so what is
			// held back is the file's final unterminated line.
			end = len(r.pending) - 1
		case int64(len(r.pending)) >= r.maxHeldBack():
			// A file with no newline for this many bytes is not
			// something this reader can frame, and continuing to
			// hold it back would buffer the whole file. PERF-026
			// puts peak memory at O(1) in input size, and an
			// unbounded carry-over is the one place in this module
			// where that could quietly fail.
			//
			// Hand it over instead: the parser has its own
			// MaxRecordBytes and will skip and COUNT an over-long
			// record (E18), which is a bounded and visible outcome
			// rather than an invisible unbounded one.
			end = len(r.pending) - 1
		default:
			return 0
		}
	}
	n := copy(p, r.pending[:end+1])
	r.pending = r.pending[n:]
	r.recordOffset()
	return n
}

// maxHeldBack bounds the partial record carried between chunks.
//
// Two chunks: enough that a record straddling a boundary is always reassembled
// -- a record longer than one chunk cannot be reassembled by any amount of
// buffering short of the whole file -- and bounded, which is the point.
func (r *remoteReader) maxHeldBack() int64 { return 2 * r.cfg.chunkSize() }

// recordOffset stores how far the current file has been consumed: everything
// fetched, less whatever is still held back.
func (r *remoteReader) recordOffset() {
	if r.cfg.Offsets == nil || r.curPath == "" {
		return
	}
	r.cfg.Offsets.Set(r.curPath, r.curOff-int64(len(r.pending)))
}

// fetch reads the next chunk of the current file.
func (r *remoteReader) fetch() error {
	var chunk string
	err := r.conn.QueryRow(r.ctx, readChunk, r.curPath, r.curOff, r.cfg.chunkSize()).Scan(&chunk)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			r.finishFile()
			return nil
		}
		return err
	}
	if chunk == "" {
		r.finishFile()
		return nil
	}
	r.pending = append(r.pending, chunk...)
	r.curOff += int64(len(chunk))

	// A short chunk means the file ended inside it, so the file is done.
	// Marking it flushed without finishing it would send the reader back
	// for another chunk past end of file -- one wasted round trip per file
	// against a real server, and an unmatched expectation against a mock.
	if int64(len(chunk)) < r.cfg.chunkSize() {
		r.finishFile()
	}
	return nil
}

// finishFile marks the current file complete.
func (r *remoteReader) finishFile() {
	if r.cfg.Offsets != nil && r.curPath != "" {
		r.cfg.Offsets.Set(r.curPath, r.curOff)
	}
	r.flush = true
	r.curPath = ""
}

// nextFile moves to the next file with unread bytes. It reports whether it
// found one.
func (r *remoteReader) nextFile() bool {
	for r.idx < len(r.names) {
		f := r.names[r.idx]
		r.idx++

		full := joinRemote(r.cfg.Dir, f.name)
		var off int64
		if r.cfg.Offsets != nil {
			off, _ = r.cfg.Offsets.Get(full)
		}

		// The same two rotation cases the local reader handles, decided
		// from the only evidence available remotely (COR-007, E15):
		if off > 0 {
			if f.size == off {
				continue // nothing new since last time
			}
			if f.size < off {
				// Shorter than where we stopped: truncated and
				// reused under the same name, so the offset
				// belongs to a file that no longer exists.
				// Reading from it would skip the new contents
				// entirely.
				off = 0
			}
		}

		r.curPath, r.curOff, r.flush = full, off, false
		return true
	}
	return false
}

// Close releases the reader. There is nothing to release: the connection
// belongs to the caller, and this package must not close what it did not open.
func (r *remoteReader) Close() error {
	r.done = true
	r.pending = nil
	return nil
}

// joinRemote joins a server-side directory and filename.
//
// path, not filepath: these are paths on the SERVER, whose separator has
// nothing to do with the client's. Using filepath here would produce
// backslashes when a Windows client reads a Linux server's logs, and
// pg_read_file would not find the file.
func joinRemote(dir, name string) string {
	if dir == "" {
		return name
	}
	return strings.TrimRight(dir, "/") + "/" + name
}
