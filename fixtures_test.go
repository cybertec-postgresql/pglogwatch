package pglogwatch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixture helpers and the guards that keep the fixtures themselves honest.
//
// Several of these files exist only because of a property that is invisible in
// a text editor -- CRLF endings, a byte order mark, invalid UTF-8. A checkout
// that normalises line endings would silently strip the property and leave the
// tests passing for the wrong reason, so the properties are asserted here.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

// hugeStatementCSV writes a csvlog file whose single record is bigger than
// size bytes, and returns its path.
//
// TST-004 asks for an 8 MiB single-statement fixture, and DAT-002 caps the
// whole of testdata at 1 MB. Both hold if the file is generated rather than
// committed: the buffer growth it exercises is a function of the size, not of
// the exact bytes, so there is nothing to gain from freezing it in the
// repository and 8 MiB of git history to lose.
func hugeStatementCSV(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "huge.csv")
	var sb strings.Builder
	sb.Grow(size + 512)
	sb.WriteString(`2026-08-30 10:11:12.123 CEST,"app_user","appdb",31337,"10.0.0.5:52344",`)
	sb.WriteString(`68b2c4a0.7a69,7,"SELECT",2026-08-30 10:10:00 CEST,3/15,0,LOG,00000,"statement: SELECT `)
	sb.WriteString(strings.Repeat("column_name_that_is_reasonably_long, ", size/37))
	sb.WriteString(`1 FROM t",,,,,,,,"psql","client backend",,0` + "\n")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o600))
	return path
}

func TestFixturesStayWithinBudget(t *testing.T) {
	// DAT-002: 1 MB for everything committed under testdata.
	var total int64
	require.NoError(t, filepath.WalkDir("testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	}))
	assert.Less(t, total, int64(1<<20), "committed fixtures total %d bytes, DAT-002 allows 1 MB", total)
}

func TestFixtureBytesSurvivedCheckout(t *testing.T) {
	// .gitattributes marks testdata as -text precisely so these hold. If
	// one of these fails, the checkout normalised the fixtures and every
	// test that depends on them is passing for the wrong reason.
	t.Run("CRLF preserved (COR-006, E10)", func(t *testing.T) {
		b := fixture(t, "csv/crlf.csv")
		assert.Contains(t, string(b), "\r\n")
	})
	t.Run("LF fixtures have no CR", func(t *testing.T) {
		b := fixture(t, "csv/pg14-basic.csv")
		assert.NotContains(t, string(b), "\r")
	})
	t.Run("byte order mark preserved (E17)", func(t *testing.T) {
		b := fixture(t, "bom-only.log")
		assert.Equal(t, []byte{0xEF, 0xBB, 0xBF}, b)
	})
	t.Run("empty file is empty (E16)", func(t *testing.T) {
		assert.Empty(t, fixture(t, "empty.log"))
	})
	t.Run("invalid UTF-8 preserved (COR-005, E11)", func(t *testing.T) {
		for _, name := range []string{"csv/invalid-utf8.csv", "stderr/invalid-utf8.log"} {
			assert.False(t, utf8.Valid(fixture(t, name)), "%s should contain invalid UTF-8", name)
		}
	})
	t.Run("truncated tail has no final newline (E8)", func(t *testing.T) {
		b := fixture(t, "csv/truncated-tail.csv")
		require.NotEmpty(t, b)
		assert.NotEqual(t, byte('\n'), b[len(b)-1])
	})
	t.Run("embedded newline inside a quoted field (E2)", func(t *testing.T) {
		b := fixture(t, "csv/quotes-newlines-commas.csv")
		// More physical lines than records: the file holds four
		// records, one of which spans three lines.
		assert.Equal(t, 6, strings.Count(string(b), "\n"))
	})
}
