package pglogwatch

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Byte-offset resumption (IFC-006, AC-025).

func TestFileSetResumesFromStoredOffsets(t *testing.T) {
	// IFC-006 and AC-025: a second pass with the same store reads only what
	// arrived since the first, counting nothing twice and skipping nothing.
	dir := t.TempDir()
	path := writeLog(t, dir, "postgresql.json", "first", "second")

	store := newMemoryOffsetStore(10)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store}
	require.Equal(t, []string{"first", "second"}, readAll(t, fs))

	off, ok := store.Get(path)
	require.True(t, ok, "the file's offset must have been recorded")
	assert.Positive(t, off)

	appendLog(t, path, "third")
	assert.Equal(t, []string{"third"}, readAll(t, fs),
		"a resumed read must return only the new records")
}

func TestFileSetResumeIsASeekNotARescan(t *testing.T) {
	// §7.6: the implementation this replaces re-read the whole file to
	// resume. Reading a large file and then resuming must not read it again.
	dir := t.TempDir()
	path := writeLog(t, dir, "postgresql.json")
	var sb strings.Builder
	for i := range 20000 {
		sb.WriteString(jsonRecord("record-" + string(rune('a'+i%26))))
	}
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o600))

	store := newMemoryOffsetStore(10)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store}
	require.Len(t, readAll(t, fs), 20000)

	appendLog(t, path, "the only new one")

	rc, err := fs.Open(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()
	p := New(rc, Config{Format: FormatJSON})
	var msgs []string
	for p.Next() {
		msgs = append(msgs, string(p.Record().Message))
	}
	require.NoError(t, p.Err())

	assert.Equal(t, []string{"the only new one"}, msgs)
	assert.Less(t, p.Stats().Bytes, int64(1000),
		"resuming must read only the new bytes, not rescan the file")
}

func TestFileSetUsesTheDefaultOffsetStore(t *testing.T) {
	// IFC-007: a nil Offsets means the bounded in-memory store, not "do not
	// track" -- otherwise every pass would re-read every file.
	dir := t.TempDir()
	writeLog(t, dir, "postgresql.json", "once")
	fs := &FileSet{Dir: dir, Glob: "*.json"}

	require.Equal(t, []string{"once"}, readAll(t, fs))
	assert.Empty(t, readAll(t, fs), "the default store must remember what was read")
}
