package pgremote_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybertec-postgresql/pglogwatch"
	"github.com/cybertec-postgresql/pglogwatch/pgremote"
)

// Remote reading against a mocked connection (§6.1's integration level).
//
// A mock is the right tool here rather than a compromise: the behaviour under
// test is how this package REACTS to what pg_read_file returns -- short reads,
// chunk boundaries in awkward places, empty results, stale offsets -- and a
// real server will not produce those on demand.

// logLines builds n jsonlog records, each identifiable.
func logLines(n int) string {
	var sb strings.Builder
	for i := range n {
		sb.WriteString(`{"error_severity":"LOG","message":"record-`)
		sb.WriteString(itoa(i))
		sb.WriteString(`"}` + "\n")
	}
	return sb.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// expectLogDir sets up the pg_ls_logdir listing.
func expectLogDir(mock pgxmock.PgxPoolIface, files ...[2]any) {
	rows := pgxmock.NewRows([]string{"name", "size"})
	for _, f := range files {
		rows.AddRow(f[0], f[1])
	}
	mock.ExpectQuery("pg_ls_logdir").WillReturnRows(rows)
}

// expectChunks answers pg_read_file for one file, serving content in chunks of
// the given size, exactly as the server would.
func expectChunks(mock pgxmock.PgxPoolIface, path, content string, chunkSize int) {
	for off := 0; off < len(content); off += chunkSize {
		end := min(off+chunkSize, len(content))
		mock.ExpectQuery("pg_read_file").
			WithArgs(path, int64(off), int64(chunkSize)).
			WillReturnRows(pgxmock.NewRows([]string{"pg_read_file"}).AddRow(content[off:end]))
	}
	if len(content)%chunkSize == 0 {
		// The file ends exactly on a chunk boundary, so the reader makes
		// one more call and the server returns nothing.
		mock.ExpectQuery("pg_read_file").
			WithArgs(path, int64(len(content)), int64(chunkSize)).
			WillReturnRows(pgxmock.NewRows([]string{"pg_read_file"}).AddRow(""))
	}
}

func newMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	return mock
}

// parseAll runs a real parser over the remote reader, which is the only thing
// that proves records survived the transport intact.
func parseAll(t *testing.T, rc io.ReadCloser) []string {
	t.Helper()
	p := pglogwatch.New(rc, pglogwatch.Config{Format: pglogwatch.FormatJSON})
	var msgs []string
	for p.Next() {
		msgs = append(msgs, string(p.Record().Message))
	}
	require.NoError(t, p.Err())
	assert.Zero(t, p.Stats().Malformed,
		"IFC-009: no record may be split by a chunk boundary")
	return msgs
}

func TestReadsAFileInChunks(t *testing.T) {
	// IFC-009, the case the whole design exists for. The chunk size is
	// chosen to fall in the MIDDLE of records rather than between them, so
	// every chunk boundary straddles one.
	const records = 200
	content := logLines(records)
	const chunk = 137 // deliberately not a multiple of any record length

	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(content))})
	expectChunks(mock, "/var/log/pg/postgresql.json", content, chunk)

	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir:       "/var/log/pg",
		ChunkSize: chunk,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	msgs := parseAll(t, rc)
	require.Len(t, msgs, records)
	for i := range records {
		assert.Equal(t, "record-"+itoa(i), msgs[i], "record %d", i)
	}
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestReadsSeveralFilesInOrder(t *testing.T) {
	a, b := logLines(30), logLines(20)
	const chunk = 500

	mock := newMock(t)
	expectLogDir(mock,
		[2]any{"log-1.json", int64(len(a))},
		[2]any{"log-2.json", int64(len(b))})
	expectChunks(mock, "/var/log/pg/log-1.json", a, chunk)
	expectChunks(mock, "/var/log/pg/log-2.json", b, chunk)

	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir: "/var/log/pg", ChunkSize: chunk,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Len(t, parseAll(t, rc), 50)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGlobFiltersTheListing(t *testing.T) {
	content := logLines(5)
	mock := newMock(t)
	expectLogDir(mock,
		[2]any{"postgresql.json", int64(len(content))},
		[2]any{"postgresql.csv", int64(999)})
	expectChunks(mock, "/var/log/pg/postgresql.json", content, 1000)

	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir: "/var/log/pg", Glob: "*.json", ChunkSize: 1000,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Len(t, parseAll(t, rc), 5)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestServerPathsUseForwardSlashes(t *testing.T) {
	// The path goes to the SERVER, whose separator has nothing to do with
	// the client's. On a Windows client, filepath.Join would produce
	// backslashes and pg_read_file would not find the file -- a bug that
	// appears only in a mixed deployment.
	content := logLines(3)
	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(content))})
	// One call is enough: the content is shorter than the chunk, so the
	// reader knows the file ended and does not ask again.
	mock.ExpectQuery("pg_read_file").
		WithArgs("/var/log/pg/postgresql.json", int64(0), int64(1000)).
		WillReturnRows(pgxmock.NewRows([]string{"pg_read_file"}).AddRow(content))

	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir: "/var/log/pg", ChunkSize: 1000,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Len(t, parseAll(t, rc), 3)
	assert.NoError(t, mock.ExpectationsWereMet(),
		"the server path must be built with forward slashes")
}

// memStore is a minimal OffsetStore for the resumption tests.
type memStore map[string]int64

func (m memStore) Get(p string) (int64, bool) { v, ok := m[p]; return v, ok }
func (m memStore) Set(p string, off int64)    { m[p] = off }

func TestResumesFromAStoredOffset(t *testing.T) {
	// IFC-006: pg_read_file is offset-based, so resumption is a different
	// starting offset rather than a re-read.
	content := logLines(10)
	half := int64(len(content) / 2)
	// Round the offset up to a record boundary, as a real store would hold.
	for content[half] != '\n' {
		half++
	}
	half++

	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(content))})
	mock.ExpectQuery("pg_read_file").
		WithArgs("/var/log/pg/postgresql.json", half, int64(1<<20)).
		WillReturnRows(pgxmock.NewRows([]string{"pg_read_file"}).AddRow(content[half:]))

	store := memStore{"/var/log/pg/postgresql.json": half}
	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir: "/var/log/pg", ChunkSize: 1 << 20, Offsets: store,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	msgs := parseAll(t, rc)
	assert.NotEmpty(t, msgs)
	assert.Less(t, len(msgs), 10, "a resumed read must not return the whole file")
	assert.Equal(t, "record-9", msgs[len(msgs)-1])
	assert.Equal(t, int64(len(content)), store["/var/log/pg/postgresql.json"],
		"a fully read file's offset must equal its size")
}

func TestSkipsAFileWithNothingNew(t *testing.T) {
	// COR-007: a file whose size equals its stored offset has nothing new,
	// and must not cost a round trip to discover that.
	content := logLines(10)
	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(content))})
	// No pg_read_file expectation at all: any call fails the test.

	store := memStore{"/var/log/pg/postgresql.json": int64(len(content))}
	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir: "/var/log/pg", Offsets: store,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Empty(t, parseAll(t, rc))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestRereadsATruncatedFile(t *testing.T) {
	// E15: the file is shorter than the stored offset, so it was truncated
	// and reused. Honouring the offset would skip its new contents.
	content := logLines(4)
	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(content))})
	expectChunks(mock, "/var/log/pg/postgresql.json", content, 1<<20)

	store := memStore{"/var/log/pg/postgresql.json": 99999}
	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir: "/var/log/pg", ChunkSize: 1 << 20, Offsets: store,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	assert.Len(t, parseAll(t, rc), 4, "a truncated file must be re-read from the start")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestFinalLineWithoutANewlineIsDelivered(t *testing.T) {
	// A file whose last line is unterminated is finished, so the held-back
	// fragment must be released rather than dropped.
	content := logLines(3) + `{"error_severity":"LOG","message":"unterminated"}`
	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(content))})
	expectChunks(mock, "/var/log/pg/postgresql.json", content, 1<<20)

	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir: "/var/log/pg", ChunkSize: 1 << 20,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	msgs := parseAll(t, rc)
	require.Len(t, msgs, 4)
	assert.Equal(t, "unterminated", msgs[3])
}

func TestOpenRequiresADirectory(t *testing.T) {
	mock := newMock(t)
	_, err := pgremote.Open(context.Background(), mock, pgremote.Config{})
	assert.ErrorIs(t, err, pgremote.ErrNoDir)
}

func TestListingErrorIsReturned(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery("pg_ls_logdir").WillReturnError(assertErr)
	_, err := pgremote.Open(context.Background(), mock, pgremote.Config{Dir: "/var/log/pg"})
	assert.ErrorIs(t, err, assertErr)
}

func TestReadErrorIsReturned(t *testing.T) {
	// A failed read must surface rather than looking like end of input: a
	// permissions problem is the most likely cause, and silently reporting
	// no records would send an operator looking in the wrong place.
	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(100)})
	mock.ExpectQuery("pg_read_file").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(assertErr)

	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{Dir: "/var/log/pg"})
	require.NoError(t, err)
	defer func() { require.NoError(t, rc.Close()) }()

	_, err = io.ReadAll(rc)
	assert.ErrorIs(t, err, assertErr)
}

func TestCloseDoesNotCloseTheConnection(t *testing.T) {
	// The connection belongs to the caller. Closing it here would break any
	// caller using a pool, which is what pgwatch does.
	content := logLines(2)
	mock := newMock(t)
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(content))})
	expectChunks(mock, "/var/log/pg/postgresql.json", content, 1<<20)

	rc, err := pgremote.Open(context.Background(), mock, pgremote.Config{
		Dir: "/var/log/pg", ChunkSize: 1 << 20,
	})
	require.NoError(t, err)
	require.Len(t, parseAll(t, rc), 2)
	require.NoError(t, rc.Close())

	// Still usable afterwards.
	expectLogDir(mock, [2]any{"postgresql.json", int64(len(content))})
	_, err = pgremote.Open(context.Background(), mock, pgremote.Config{Dir: "/var/log/pg"})
	assert.NoError(t, err)
}

var assertErr = errTest("remote failure")

type errTest string

func (e errTest) Error() string { return string(e) }
