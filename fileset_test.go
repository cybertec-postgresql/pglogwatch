package pglogwatch

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FileSet: reading a directory of PostgreSQL log files as one stream
// (§4.5, IFC-005, IFC-006, COR-007).
//
// Everything here is about the awkward reality of a log directory: files
// appear, are appended to while being read, are rotated away, and sometimes
// come back with the same name and different contents. None of that is
// exceptional -- it is what a log directory does every day.

// jsonRecord renders a minimal jsonlog line, which keeps these tests about the
// FileSet rather than about any format's parser.
func jsonRecord(msg string) string {
	return `{"error_severity":"LOG","message":"` + msg + `"}` + "\n"
}

// writeLog creates or overwrites a log file with the given messages.
func writeLog(t *testing.T, dir, name string, msgs ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(jsonRecord(m))
	}
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o600))
	return path
}

// appendLog appends messages to an existing log file, the way a server does.
func appendLog(t *testing.T, path string, msgs ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test fixture
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	for _, m := range msgs {
		_, err := f.WriteString(jsonRecord(m))
		require.NoError(t, err)
	}
}

// readAll parses everything the FileSet produces, and returns the messages.
func readAll(t *testing.T, fs *FileSet) []string {
	t.Helper()
	rc, err := fs.Open(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	p := New(rc, Config{Format: FormatJSON})
	var msgs []string
	for p.Next() {
		msgs = append(msgs, string(p.Record().Message))
	}
	require.NoError(t, p.Err())
	return msgs
}

func TestFileSetReadsFilesInOrder(t *testing.T) {
	// §4.5: a directory plus a glob resolves to an ordered sequence.
	//
	// Order is by name, which is what PostgreSQL's log_filename patterns are
	// designed for -- postgresql-2026-08-30_101112.log sorts chronologically
	// as a string. Reading them out of order would interleave records from
	// different days.
	dir := t.TempDir()
	writeLog(t, dir, "postgresql-2026-08-28.json", "monday-1", "monday-2")
	writeLog(t, dir, "postgresql-2026-08-29.json", "tuesday-1")
	writeLog(t, dir, "postgresql-2026-08-30.json", "wednesday-1", "wednesday-2")

	fs := &FileSet{Dir: dir, Glob: "*.json"}
	assert.Equal(t, []string{
		"monday-1", "monday-2", "tuesday-1", "wednesday-1", "wednesday-2",
	}, readAll(t, fs))
}

func TestFileSetIgnoresFilesOutsideTheGlob(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "postgresql.json", "wanted")
	writeLog(t, dir, "postgresql.log", "not wanted")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o600))

	fs := &FileSet{Dir: dir, Glob: "*.json"}
	assert.Equal(t, []string{"wanted"}, readAll(t, fs))
}

func TestFileSetDefaultGlobFollowsFormat(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "postgresql.json", "json file")
	writeLog(t, dir, "postgresql.csv", "csv file")

	fs := &FileSet{Dir: dir, Format: FormatJSON}
	assert.Equal(t, []string{"json file"}, readAll(t, fs))
}

func TestFileSetEmptyDirectory(t *testing.T) {
	fs := &FileSet{Dir: t.TempDir(), Glob: "*.json"}
	assert.Empty(t, readAll(t, fs))
}

func TestFileSetMissingDirectoryIsAnError(t *testing.T) {
	// A missing log_directory is a configuration problem the caller has to
	// see, not something to treat as an empty log.
	fs := &FileSet{Dir: filepath.Join(t.TempDir(), "nope"), Glob: "*.json"}
	_, err := fs.Open(t.Context())
	assert.Error(t, err)
}

func TestFileSetCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "postgresql.json", "one")
	fs := &FileSet{Dir: dir, Glob: "*.json"}
	rc, err := fs.Open(t.Context())
	require.NoError(t, err)
	_, err = io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.NoError(t, rc.Close(), "Close must tolerate being called twice")
}
