package pglogwatch

import (
	"bytes"
	"io"
	"os"
)

// Single-file sources.
//
// PAT-004 keeps sourcing separate from parsing: everything here produces an
// io.Reader and knows nothing about records. That separation is what lets the
// same parser read a file, a directory being tailed, a decompressing stream or
// a remote chunk reader without any of them knowing about each other.
//
// CON-005 confines filesystem access to constructors a caller explicitly asks
// for. Opening a file is something the caller requested by name; the parser
// itself still never touches the filesystem.

// openFileAt opens path positioned at offset.
//
// A non-zero offset is the resumption path (IFC-006): one Seek, rather than
// reading and discarding everything before it. If the file is shorter than the
// offset -- which is what a truncating rotation leaves behind -- reading starts
// at the beginning instead, because the file is a new one that happens to have
// the old name (E15).
func openFileAt(path string, offset int64) (*os.File, error) {
	f, err := os.Open(path) //nolint:gosec // the caller named the file
	if err != nil {
		return nil, err
	}
	if offset <= 0 {
		return f, nil
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Size() < offset {
		// The file shrank: it was truncated and reused, so the stored
		// offset belongs to a file that no longer exists. Starting over
		// is the only way not to skip its contents.
		return f, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// fingerprintBytes is how much of a file's head is used to identify it.
//
// One PostgreSQL log line is comfortably shorter than this and begins with a
// timestamp, so two different log files agreeing over this many bytes is not a
// case that occurs.
const fingerprintBytes = 256

// fileIdentity distinguishes one file from another with the same name.
//
// COR-007 requires a rotation that reuses a filename not to be mistaken for
// the same file. The obvious test is os.SameFile, which compares inodes on
// Unix and file indices on Windows -- and it is NOT sufficient. Measured on
// windows/amd64: deleting a file and recreating it under the same name yields
// two FileInfos that os.SameFile reports as identical, because NTFS reuses the
// file index. A test that trusted it would silently skip the new file's
// contents on exactly the platform PKG-005 requires support for.
//
// So identity is also the first fingerprintBytes of content. A file being
// APPENDED to keeps its head, while a truncated or replaced file does not,
// which distinguishes the two cases that matter without depending on
// filesystem semantics at all.
type fileIdentity struct {
	info    os.FileInfo
	size    int64
	head    [fingerprintBytes]byte
	headLen int
}

func identify(path string) (fileIdentity, error) {
	f, err := os.Open(path) //nolint:gosec // the caller named the file
	if err != nil {
		return fileIdentity{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fileIdentity{}, err
	}
	id := fileIdentity{info: info, size: info.Size()}
	n, err := io.ReadFull(f, id.head[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return fileIdentity{}, err
	}
	id.headLen = n
	return id, nil
}

// sameFile reports whether two identities describe the same file.
//
// The heads are compared over their COMMON prefix, so a file that has grown
// since it was last seen still matches: appending changes the length, never
// the beginning. Differing content within that prefix means a different file,
// whatever the filesystem reports.
func (id fileIdentity) sameFile(other fileIdentity) bool {
	if id.info == nil || other.info == nil {
		return false
	}
	n := min(id.headLen, other.headLen)
	if !bytes.Equal(id.head[:n], other.head[:n]) {
		return false
	}
	// On Unix this still adds precision: two files can begin identically
	// and be different files, and the inode says so.
	return os.SameFile(id.info, other.info)
}
