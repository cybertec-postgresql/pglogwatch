package pglogwatch

import (
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

// fileIdentity distinguishes one file from another with the same name.
//
// A rotation that reuses a filename produces a different file with identical
// path, and COR-007 requires that not to be mistaken for the same one. Size is
// the portable half of the test; os.SameFile supplies the inode or file-index
// comparison on every platform Go supports, including Windows, which is why
// this is expressed as a stat rather than as a Unix-specific syscall (PKG-005).
type fileIdentity struct {
	info os.FileInfo
	size int64
}

func identify(path string) (fileIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{info: info, size: info.Size()}, nil
}

// sameFile reports whether two identities describe the same file.
func (id fileIdentity) sameFile(other fileIdentity) bool {
	if id.info == nil || other.info == nil {
		return false
	}
	return os.SameFile(id.info, other.info)
}
