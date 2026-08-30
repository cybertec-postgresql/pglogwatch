package pglogwatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rotation handling (COR-007, E15).

func TestFileSetRotationToANewName(t *testing.T) {
	// The ordinary rotation: yesterday's file stays, today's appears.
	// Records from both must arrive exactly once.
	dir := t.TempDir()
	store := newMemoryOffsetStore(10)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store}

	writeLog(t, dir, "postgresql-1.json", "a", "b")
	require.Equal(t, []string{"a", "b"}, readAll(t, fs))

	writeLog(t, dir, "postgresql-2.json", "c")
	assert.Equal(t, []string{"c"}, readAll(t, fs),
		"COR-007: the old file must not be re-read when a new one appears")
}

func TestFileSetRotationReusingTheSameName(t *testing.T) {
	// E15 and COR-007: log_truncate_on_rotation empties a file and reuses
	// its name. The stored offset then belongs to a file that no longer
	// exists, and honouring it would skip the new file's contents entirely.
	dir := t.TempDir()
	store := newMemoryOffsetStore(10)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store, TruncateOnRotation: true}

	path := writeLog(t, dir, "postgresql-Sunday.json", "old-1", "old-2", "old-3")
	require.Equal(t, []string{"old-1", "old-2", "old-3"}, readAll(t, fs))

	// A week later: same name, truncated, new and shorter contents.
	writeLog(t, dir, "postgresql-Sunday.json", "new-1")
	assert.Equal(t, []string{"new-1"}, readAll(t, fs),
		"a truncated file must be re-read from the start, not skipped")
	_ = path
}

func TestFileSetDoesNotDoubleCountAcrossRotation(t *testing.T) {
	// COR-007 stated as the property that matters: over a sequence of
	// rotations, every record appears exactly once in total.
	dir := t.TempDir()
	store := newMemoryOffsetStore(100)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store, TruncateOnRotation: true}

	seen := map[string]int{}
	collect := func() {
		for _, m := range readAll(t, fs) {
			seen[m]++
		}
	}

	writeLog(t, dir, "log-1.json", "r1", "r2")
	collect()
	appendLog(t, filepath.Join(dir, "log-1.json"), "r3")
	collect()
	writeLog(t, dir, "log-2.json", "r4")
	collect()
	writeLog(t, dir, "log-1.json", "r5") // same name, truncated and reused
	collect()
	collect() // nothing new

	for _, r := range []string{"r1", "r2", "r3", "r4", "r5"} {
		assert.Equal(t, 1, seen[r], "record %s appeared %d times", r, seen[r])
	}
	assert.Len(t, seen, 5)
}

func TestFileSetRotationReplacingTheFileBehindAName(t *testing.T) {
	// The rotation the size test cannot see, and the reason file identity
	// is tracked at all (COR-007).
	//
	// A rotation that renames the old file away and creates a new one leaves
	// a DIFFERENT file behind the same name. If the new file has already
	// grown past the old offset by the time we look, its size proves
	// nothing -- a size-only check would seek into it and silently skip
	// everything before the old offset.
	dir := t.TempDir()
	store := newMemoryOffsetStore(10)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store}

	writeLog(t, dir, "postgresql.json", "old-1", "old-2")
	require.Equal(t, []string{"old-1", "old-2"}, readAll(t, fs))

	// Rotate: move the old file aside, then create a new one under the old
	// name that is already LONGER than the offset we stored.
	require.NoError(t, os.Rename(
		filepath.Join(dir, "postgresql.json"),
		filepath.Join(dir, "postgresql-old.json.keep"), // outside the glob
	))
	writeLog(t, dir, "postgresql.json",
		"new-1", "new-2", "new-3", "new-4", "new-5", "new-6")

	assert.Equal(t, []string{"new-1", "new-2", "new-3", "new-4", "new-5", "new-6"},
		readAll(t, fs),
		"a replaced file must be read from the start, not seeked into")
}

func TestFileSetRotationIdentityIsCheckedNotJustSize(t *testing.T) {
	// The same rotation, arranged so the new file's size is EXACTLY the old
	// offset. A size comparison sees "nothing new" and skips the file
	// entirely; only identity distinguishes them.
	dir := t.TempDir()
	store := newMemoryOffsetStore(10)
	fs := &FileSet{Dir: dir, Glob: "*.json", Offsets: store}

	writeLog(t, dir, "postgresql.json", "aaaa", "bbbb")
	require.Equal(t, []string{"aaaa", "bbbb"}, readAll(t, fs))

	require.NoError(t, os.Remove(filepath.Join(dir, "postgresql.json")))
	// Same number of records of the same length, so the same byte length.
	writeLog(t, dir, "postgresql.json", "cccc", "dddd")

	assert.Equal(t, []string{"cccc", "dddd"}, readAll(t, fs),
		"a replaced file of identical size must still be re-read")
}
