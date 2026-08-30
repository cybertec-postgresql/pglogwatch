package pglogwatch

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Follow mode (IFC-005).

func TestFileSetFollowWaitsForNewData(t *testing.T) {
	// Follow mode: the reader stays open and delivers records as they are
	// written, rather than ending at the last byte present when it started.
	dir := t.TempDir()
	path := writeLog(t, dir, "postgresql.json", "before")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	fs := &FileSet{Dir: dir, Glob: "*.json", Follow: true, PollInterval: 10 * time.Millisecond}
	rc, err := fs.Open(ctx)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got := make(chan string, 4)
	go func() {
		p := New(rc, Config{Format: FormatJSON})
		for p.Next() {
			got <- string(p.Record().Message)
		}
		close(got)
	}()

	assert.Equal(t, "before", waitFor(t, got))
	appendLog(t, path, "after")
	assert.Equal(t, "after", waitFor(t, got), "a follower must deliver records written after it started")
}

func TestFileSetFollowPicksUpANewFile(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "log-1.json", "old file")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	fs := &FileSet{Dir: dir, Glob: "*.json", Follow: true, PollInterval: 10 * time.Millisecond}
	rc, err := fs.Open(ctx)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got := make(chan string, 4)
	go func() {
		p := New(rc, Config{Format: FormatJSON})
		for p.Next() {
			got <- string(p.Record().Message)
		}
		close(got)
	}()

	assert.Equal(t, "old file", waitFor(t, got))
	writeLog(t, dir, "log-2.json", "new file")
	assert.Equal(t, "new file", waitFor(t, got),
		"a follower must notice a file that appears after it started")
}

func TestFileSetFollowWithholdsAPartialRecord(t *testing.T) {
	// IFC-005: a follower must not hand over a partially written record.
	// PostgreSQL writes a line in more than one write syscall, so a reader
	// that returned whatever was present would routinely split records --
	// and the parser would count each half as malformed.
	dir := t.TempDir()
	path := writeLog(t, dir, "postgresql.json", "complete")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	fs := &FileSet{Dir: dir, Glob: "*.json", Follow: true, PollInterval: 10 * time.Millisecond}
	rc, err := fs.Open(ctx)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	got := make(chan string, 4)
	malformed := make(chan struct{}, 8)
	go func() {
		p := New(rc, Config{
			Format:      FormatJSON,
			OnMalformed: func([]byte, error) { malformed <- struct{}{} },
		})
		for p.Next() {
			got <- string(p.Record().Message)
		}
		close(got)
	}()
	require.Equal(t, "complete", waitFor(t, got))

	// Write half a record, wait past several polls, then finish it.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test fixture
	require.NoError(t, err)
	_, err = f.WriteString(`{"error_severity":"LOG","message":"halves`)
	require.NoError(t, err)
	time.Sleep(60 * time.Millisecond)

	select {
	case <-malformed:
		t.Fatal("IFC-005: a partially written record was handed to the parser")
	default:
	}

	_, err = f.WriteString(`"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	assert.Equal(t, "halves", waitFor(t, got))
}

func TestFileSetFollowStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "postgresql.json", "one")

	ctx, cancel := context.WithCancel(t.Context())
	fs := &FileSet{Dir: dir, Glob: "*.json", Follow: true, PollInterval: 10 * time.Millisecond}
	rc, err := fs.Open(ctx)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	done := make(chan int, 1)
	go func() {
		p := New(rc, Config{Format: FormatJSON})
		n := 0
		for p.Next() {
			n++
		}
		done <- n
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case n := <-done:
		assert.Equal(t, 1, n)
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled follower must stop")
	}
}

// waitFor reads one value from a channel with a generous timeout, so a broken

func waitFor(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("the reader ended before producing the expected record")
		}
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a record")
		return ""
	}
}
