package compress_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"

	"github.com/cybertec-postgresql/pglogwatch"
	"github.com/cybertec-postgresql/pglogwatch/compress"
)

// A representative log: several records, long enough that compression
// actually produces multiple internal blocks.
func sampleLog() []byte {
	var sb strings.Builder
	for i := range 500 {
		sb.WriteString(`{"timestamp":"2026-08-30 10:11:12.123 UTC","error_severity":"LOG",`)
		sb.WriteString(`"pid":31337,"message":"duration: 1.500 ms  statement: SELECT `)
		sb.WriteString(string(rune('a' + i%26)))
		sb.WriteString(`"}` + "\n")
	}
	return []byte(sb.String())
}

// compressors produce the four formats PKG-004 names. gzip and xz are written
// with the same libraries used to read them; zstd likewise. bzip2 has no
// writer in Go's standard library or in klauspost/compress, so its fixture is
// a committed file rather than one generated here.
var compressors = map[string]func(*testing.T, []byte) []byte{
	".gz": func(t *testing.T, in []byte) []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, err := w.Write(in)
		require.NoError(t, err)
		require.NoError(t, w.Close())
		return buf.Bytes()
	},
	".zst": func(t *testing.T, in []byte) []byte {
		var buf bytes.Buffer
		w, err := zstd.NewWriter(&buf)
		require.NoError(t, err)
		_, err = w.Write(in)
		require.NoError(t, err)
		require.NoError(t, w.Close())
		return buf.Bytes()
	},
	".xz": func(t *testing.T, in []byte) []byte {
		var buf bytes.Buffer
		w, err := xz.NewWriter(&buf)
		require.NoError(t, err)
		_, err = w.Write(in)
		require.NoError(t, err)
		require.NoError(t, w.Close())
		return buf.Bytes()
	},
}

func TestOpenDecodesEveryFormat(t *testing.T) {
	want := sampleLog()
	dir := t.TempDir()

	for ext, compressFn := range compressors {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(dir, "postgresql.json"+ext)
			require.NoError(t, os.WriteFile(path, compressFn(t, want), 0o600))

			rc, err := compress.Open(path)
			require.NoError(t, err)
			defer func() { require.NoError(t, rc.Close()) }()

			got, err := io.ReadAll(rc)
			require.NoError(t, err)
			assert.Equal(t, want, got,
				"decompressed bytes must be identical to the original log")
		})
	}
}

func TestOpenPassesPlainFilesThrough(t *testing.T) {
	// The common case: an uncompressed log. A caller should be able to use
	// this package unconditionally rather than checking first.
	want := sampleLog()
	path := filepath.Join(t.TempDir(), "postgresql.json")
	require.NoError(t, os.WriteFile(path, want, 0o600))

	rc, err := compress.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestOpenIgnoresAMisleadingExtension(t *testing.T) {
	// Content decides, not the name. An in-place rotation script that
	// compresses a file without renaming it is common enough that reading
	// by extension would fail on real deployments.
	want := sampleLog()
	path := filepath.Join(t.TempDir(), "postgresql.log") // named plain
	require.NoError(t, os.WriteFile(path, compressors[".gz"](t, want), 0o600))

	rc, err := compress.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, want, got, "gzip content named .log must still decompress")
}

func TestOpenRejectsAnUncompressedFileNamedCompressed(t *testing.T) {
	// The opposite mismatch must be an error. Handing back the raw bytes of
	// a broken .gz would look like a corrupt log and cost a long time to
	// diagnose; saying so immediately does not.
	path := filepath.Join(t.TempDir(), "postgresql.json.gz")
	require.NoError(t, os.WriteFile(path, sampleLog(), 0o600))

	_, err := compress.Open(path)
	assert.ErrorIs(t, err, compress.ErrUnknownFormat)
}

func TestNewReaderWorksOnAPipe(t *testing.T) {
	// Sniffing must not need seeking, so the package works on a stream --
	// which is what a remote reader or a subprocess hands over.
	want := sampleLog()
	pr, pw := io.Pipe()
	go func() {
		_, err := pw.Write(compressors[".gz"](t, want))
		assert.NoError(t, err)
		assert.NoError(t, pw.Close())
	}()

	rc, err := compress.NewReader(pr)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestOpenEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	rc, err := compress.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestOpenMissingFile(t *testing.T) {
	_, err := compress.Open(filepath.Join(t.TempDir(), "nope.json.gz"))
	assert.Error(t, err)
}

func TestCloseReleasesTheFile(t *testing.T) {
	// The decompressor wraps a file, and closing the reader must close both
	// -- otherwise a directory scan leaks a descriptor per rotated file.
	path := filepath.Join(t.TempDir(), "postgresql.json.gz")
	require.NoError(t, os.WriteFile(path, compressors[".gz"](t, sampleLog()), 0o600))

	rc, err := compress.Open(path)
	require.NoError(t, err)
	_, err = io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())

	// On Windows an open handle prevents removal, which makes this a real
	// check there rather than a formality.
	assert.NoError(t, os.Remove(path), "the underlying file was still open")
}

// TestOpenDecodesBzip2 covers the fourth format PKG-004 names.
//
// It uses a committed fixture rather than generating one, because neither the
// standard library nor klauspost/compress can WRITE bzip2 -- compress/bzip2 is
// decompression only. Adding a bzip2 writer purely to build a test fixture
// would put a dependency in this module that nothing in it uses at run time.
func TestOpenDecodesBzip2(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "postgresql.json.plain"))
	require.NoError(t, err)

	rc, err := compress.Open(filepath.Join("testdata", "postgresql.json.bz2"))
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestDecompressedLogParses closes the loop: the point of this module is that
// a compressed log reaches a parser, so one is run over the output.
func TestDecompressedLogParses(t *testing.T) {
	rc, err := compress.Open(filepath.Join("testdata", "postgresql.json.bz2"))
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	p := pglogwatch.New(rc, pglogwatch.Config{})
	n := 0
	for p.Next() {
		assert.Equal(t, pglogwatch.SeverityLog, p.Record().Severity)
		n++
	}
	require.NoError(t, p.Err())
	assert.Equal(t, 500, n)
	assert.Equal(t, pglogwatch.FormatJSON, p.DetectedFormat(),
		"detection must work on decompressed input like any other")
}
