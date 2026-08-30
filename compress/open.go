// Package compress opens PostgreSQL log files that have been compressed after
// rotation, presenting them to a parser as plain byte streams.
//
// It lives in its own module so that consumers of the root pglogwatch package
// never inherit a compression dependency (PKG-004). Import it only if your
// logs are compressed.
//
// Compressed input cannot be seeked, so a reader from this package does not
// support pglogwatch.Parser.Seek and cannot be resumed from a byte offset. A
// rotated file is finished, though, so the offset that matters for it is
// "fully read", which an OffsetStore records once.
package compress

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	kgzip "github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// ErrUnknownFormat reports input that is not in a compression format this
// package recognises.
var ErrUnknownFormat = errors.New("compress: unrecognised compression format")

// Open opens a log file, decompressing it if it is compressed.
//
// The format is chosen by content, not by filename, with the extension used
// only as a fallback for the formats whose magic bytes are ambiguous. A
// rotated log named .log that is in fact gzip -- which happens when a rotation
// script compresses in place -- is read correctly.
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path) //nolint:gosec // the caller named the file
	if err != nil {
		return nil, err
	}
	rc, err := newReader(f, filepath.Ext(path))
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &closer{Reader: rc, underlying: f}, nil
}

// NewReader wraps r, decompressing it if its contents are compressed.
//
// Uncompressed input is passed through, so a caller need not know in advance
// which it has.
func NewReader(r io.Reader) (io.ReadCloser, error) {
	return newReader(r, "")
}

// magic identifies a compression format by its leading bytes.
type magic struct {
	prefix []byte
	open   func(io.Reader) (io.Reader, error)
}

// magics lists the formats recognised by content.
//
// xz and bzip2 have distinctive multi-byte signatures; gzip and zstd have
// short but unambiguous ones. None of them can begin a PostgreSQL log line,
// which starts with a digit, a brace or a prefix literal, so a false positive
// on plain text is not possible.
var magics = []magic{
	{[]byte{0x1F, 0x8B}, func(r io.Reader) (io.Reader, error) { return kgzip.NewReader(r) }},
	{[]byte{0x28, 0xB5, 0x2F, 0xFD}, func(r io.Reader) (io.Reader, error) {
		d, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return d.IOReadCloser(), nil
	}},
	{[]byte("BZh"), func(r io.Reader) (io.Reader, error) { return bzip2.NewReader(r), nil }},
	{[]byte{0xFD, '7', 'z', 'X', 'Z', 0x00}, func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) }},
}

// maxMagic is the longest signature above.
const maxMagic = 6

func newReader(r io.Reader, ext string) (io.ReadCloser, error) {
	// bufio.Reader is what makes content sniffing possible without
	// consuming the bytes: Peek looks ahead and leaves them in place, so
	// plain input needs no rewinding and no ReaderAt.
	br := bufio.NewReaderSize(r, 64<<10)
	head, err := br.Peek(maxMagic)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return nil, err
	}

	for _, m := range magics {
		if bytes.HasPrefix(head, m.prefix) {
			dec, err := m.open(br)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(dec), nil
		}
	}

	// Nothing recognised. An extension that claims compression is then a
	// real error rather than something to read as text: handing a caller
	// the compressed bytes of a .gz would look like a corrupt log and
	// waste a long time being diagnosed.
	if isCompressedExt(ext) {
		return nil, ErrUnknownFormat
	}
	return io.NopCloser(br), nil
}

func isCompressedExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".gz", ".zst", ".zstd", ".bz2", ".xz":
		return true
	}
	return false
}

// closer joins the decompressor's lifetime to the file's, so closing the
// returned reader closes both.
type closer struct {
	io.Reader
	underlying io.Closer
}

func (c *closer) Close() error {
	if rc, ok := c.Reader.(io.Closer); ok {
		_ = rc.Close()
	}
	return c.underlying.Close()
}
