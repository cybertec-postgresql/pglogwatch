package pglogwatch

import (
	"os"
	"path/filepath"
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

func TestFileSetRecordsTheFullSizeOfAFinishedFile(t *testing.T) {
	// After a full drain, each file's stored offset is its size, so the next
	// pass reads nothing from it (IFC-006).
	dir := t.TempDir()
	p1 := writeLog(t, dir, "log-1.json", "a", "b")
	p2 := writeLog(t, dir, "log-2.json", "c")

	store := newMemoryOffsetStore(10)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store}
	require.Equal(t, []string{"a", "b", "c"}, readAll(t, fs))

	for _, path := range []string{p1, p2} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		off, ok := store.Get(path)
		require.True(t, ok, "no offset recorded for %s", path)
		assert.Equal(t, info.Size(), off,
			"a fully read file's offset must equal its size")
	}
	assert.Empty(t, readAll(t, fs), "a second pass must read nothing")
}

func TestFileSetRecordsAFinalLineWithoutANewline(t *testing.T) {
	// A file whose last line has no newline is finished: it will never get
	// one. Leaving the offset short of that fragment would re-deliver it on
	// every subsequent pass, forever.
	dir := t.TempDir()
	path := filepath.Join(dir, "log.json")
	require.NoError(t, os.WriteFile(path,
		[]byte(jsonRecord("complete")+`{"error_severity":"LOG","message":"no newline"}`), 0o600))

	store := newMemoryOffsetStore(10)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store}
	require.Equal(t, []string{"complete", "no newline"}, readAll(t, fs))

	info, err := os.Stat(path)
	require.NoError(t, err)
	off, _ := store.Get(path)
	assert.Equal(t, info.Size(), off)
	assert.Empty(t, readAll(t, fs), "the unterminated line must not be re-delivered")
}

func TestFileSetResumesAcrossManyPasses(t *testing.T) {
	// The property a collector loop depends on: repeated passes over a
	// growing directory deliver every record exactly once in total.
	dir := t.TempDir()
	store := newMemoryOffsetStore(50)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store}

	seen := map[string]int{}
	path := writeLog(t, dir, "log.json")
	for i := range 10 {
		appendLog(t, path, "record-"+string(rune('a'+i)))
		for _, m := range readAll(t, fs) {
			seen[m]++
		}
	}
	assert.Len(t, seen, 10)
	for m, n := range seen {
		assert.Equal(t, 1, n, "%s delivered %d times", m, n)
	}
}
