package pgremote_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybertec-postgresql/pglogwatch/pgremote"
)

// Follow mode.
//
// These read the reader's BYTES rather than parsing them, because what follow
// mode changes is framing: which bytes are handed over and when. A parser would
// hide exactly the failure worth testing for -- half a record delivered now and
// half next poll parses as two malformed records, and a test that only counted
// records would pass while the counts it produced were wrong.

const followPoll = 20 * time.Millisecond

// expectRead answers one pg_read_file call.
func expectRead(mock pgxmock.PgxPoolIface, path string, off, size int64, chunk string) {
	mock.ExpectQuery("pg_read_file").
		WithArgs(path, off, size).
		WillReturnRows(pgxmock.NewRows([]string{"pg_read_file"}).AddRow(chunk))
}

// readN reads until it has n bytes or the reader ends.
func readN(t *testing.T, rc io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, 0, n)
	tmp := make([]byte, n)
	for len(buf) < n {
		k, err := rc.Read(tmp)
		buf = append(buf, tmp[:k]...)
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	return string(buf)
}

func TestFollowPicksUpDataAppendedLater(t *testing.T) {
	const chunk = 1 << 20
	first := "{\"error_severity\":\"LOG\",\"message\":\"a\"}\n"
	second := "{\"error_severity\":\"LOG\",\"message\":\"b\"}\n"
	const p = "/var/log/pg/postgresql.json"

	mock := newMock(t)
	// First pass: the file holds one record.
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(first))})
	expectRead(mock, p, 0, chunk, first)
	// Second pass: it has grown by one more.
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(first + second))})
	expectRead(mock, p, int64(len(first)), chunk, second)

	// Offsets is deliberately left nil. A follower with nowhere to record
	// how far it has read would restart every file from zero on every poll
	// and deliver the whole directory again, so Open substitutes an
	// in-memory store. The proof is the second expectation above: it demands
	// a read at offset len(first), which only happens if the first pass's
	// progress was recorded.
	rc, err := pgremote.Open(t.Context(), mock, pgremote.Config{
		Dir:          "/var/log/pg",
		ChunkSize:    chunk,
		Follow:       true,
		PollInterval: followPoll,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Equal(t, first, readN(t, rc, len(first)),
		"the first record must arrive without waiting for the file to end")
	assert.Equal(t, second, readN(t, rc, len(second)),
		"a follower must deliver data appended after it caught up")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestFollowHoldsBackAPartialTrailingLine(t *testing.T) {
	// The reason finishFile branches on Follow. Reaching the end of what the
	// server has written is not reaching the end of the file, so a trailing
	// line with no newline is a record still being written -- not a record.
	const chunk = 1 << 20
	const p = "/var/log/pg/postgresql.json"
	whole := "{\"error_severity\":\"LOG\",\"message\":\"complete\"}\n"
	half := "{\"error_severity\":\"LOG\",\"messa" // no newline yet
	rest := "ge\":\"torn\"}\n"                    // arrives next poll

	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(whole + half))})
	expectRead(mock, p, 0, chunk, whole+half)
	// The partial line was rewound, so the next read resumes at its FIRST
	// byte and fetches it whole rather than fetching only the remainder.
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(whole + half + rest))})
	expectRead(mock, p, int64(len(whole)), chunk, half+rest)

	rc, err := pgremote.Open(t.Context(), mock, pgremote.Config{
		Dir:          "/var/log/pg",
		ChunkSize:    chunk,
		Follow:       true,
		PollInterval: followPoll,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Equal(t, whole, readN(t, rc, len(whole)),
		"the complete record must be delivered; the partial one must not")
	assert.Equal(t, half+rest, readN(t, rc, len(half+rest)),
		"the torn record must arrive in one piece once the server finishes it")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestFollowPicksUpANewFile(t *testing.T) {
	// Rotation: the directory listing is re-read on every poll, so a file
	// created after Open is found without reopening the reader.
	const chunk = 1 << 20
	a := "{\"error_severity\":\"LOG\",\"message\":\"old\"}\n"
	b := "{\"error_severity\":\"LOG\",\"message\":\"new\"}\n"

	mock := newMock(t)
	expectLogDir(mock, [2]any{"a.json", int64(len(a))})
	expectRead(mock, "/var/log/pg/a.json", 0, chunk, a)
	expectLogDir(mock,
		[2]any{"a.json", int64(len(a))}, // unchanged, must be skipped
		[2]any{"b.json", int64(len(b))},
	)
	expectRead(mock, "/var/log/pg/b.json", 0, chunk, b)

	rc, err := pgremote.Open(t.Context(), mock, pgremote.Config{
		Dir:          "/var/log/pg",
		ChunkSize:    chunk,
		Follow:       true,
		PollInterval: followPoll,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Equal(t, a, readN(t, rc, len(a)))
	assert.Equal(t, b, readN(t, rc, len(b)),
		"a file that appeared after Open must be picked up")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestFollowEndsCleanlyWhenContextIsDone(t *testing.T) {
	const chunk = 1 << 20
	one := "{\"error_severity\":\"LOG\",\"message\":\"only\"}\n"

	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(one))})
	expectRead(mock, "/var/log/pg/postgresql.json", 0, chunk, one)

	ctx, cancel := context.WithCancel(t.Context())
	rc, err := pgremote.Open(ctx, mock, pgremote.Config{
		Dir:       "/var/log/pg",
		ChunkSize: chunk,
		Follow:    true,
		// Long on purpose: the reader is parked in wait() when cancel
		// arrives, which is the state being tested. A short interval
		// would race the cancel against a re-list and make the number
		// of listings -- and so the mock's expectations -- depend on
		// scheduling.
		PollInterval: time.Hour,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Equal(t, one, readN(t, rc, len(one)))

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := rc.Read(buf); err != nil {
				done <- err
				return
			}
		}
	}()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, io.EOF,
			"a cancelled follower must end like any exhausted reader, not error")
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled follower did not return")
	}
}

func TestWithoutFollowAnUnterminatedTailIsStillFlushed(t *testing.T) {
	// FMT-009 for the one-shot reader, which the Follow branch must not have
	// changed: with no more data ever coming, a trailing line with no
	// newline IS the file's last line.
	const chunk = 1 << 20
	tail := "{\"error_severity\":\"LOG\",\"message\":\"unterminated\"}"

	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(tail))})
	expectRead(mock, "/var/log/pg/postgresql.json", 0, chunk, tail)

	rc, err := pgremote.Open(t.Context(), mock, pgremote.Config{
		Dir:       "/var/log/pg",
		ChunkSize: chunk,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, tail, string(got))
	assert.NoError(t, mock.ExpectationsWereMet())
}
