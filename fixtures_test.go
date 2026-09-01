package pglogwatch

import (
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
		// testdata/fuzz is the fuzzer's own corpus of failing inputs,
		// written by the go tool rather than committed as a fixture, so
		// it is not part of DAT-002's budget.
		if strings.Contains(filepath.ToSlash(path), "/fuzz/") {
			return nil
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

// TestFixturesCarryNoRealIdentifiers is TST-005 and COM-002.
//
// Every fixture in the repository today is hand-written and synthetic:
// app_user, appdb, psql, orders, and addresses in 10.0.0.0/8. Nothing needed
// scrubbing. The check exists for the fixture that has not been added yet.
//
// TST-005 permits real-world samples, and a real-world sample is exactly what
// a contributor reaches for the first time a customer reports a log this
// parser mishandles. That file arrives by copy-and-paste from a support
// ticket, and the identifiers in it -- a username, a hostname, a client
// address, a query parameter -- are personal data the moment they are pushed
// to a public repository. Catching it at `go test` is the only point in that
// sequence where it is still cheap.
func TestFixturesCarryNoRealIdentifiers(t *testing.T) {
	err := filepath.WalkDir("testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// testdata/fuzz holds the fuzzer's corpus: machine-generated
		// mutations of the seeds in fuzz_test.go, not samples anyone
		// pasted in. A byte sequence in there that happens to look like
		// an address came out of the mutator, and flagging it would
		// train a reader to ignore this test.
		if strings.Contains(filepath.ToSlash(path), "/fuzz/") {
			return nil
		}
		body, err := os.ReadFile(path) //nolint:gosec // a fixture path from the walk
		if err != nil {
			return err
		}
		checkNoIdentifiers(t, path, string(body))
		return nil
	})
	require.NoError(t, err)
}

// publicTLDs is the suffix list the hostname check works from.
//
// A denylist rather than an allowlist, because a fixture is full of dotted
// names that are not hostnames -- parse_relation.c, pg13-basic.csv, 12.0.1 --
// and requiring every one of them to end in a reserved suffix produces a test
// that cries wolf until somebody deletes it. These are the suffixes a
// copy-and-pasted hostname from a real deployment actually carries.
var publicTLDs = []string{
	"com", "net", "org", "io", "co", "eu", "dev", "app", "cloud", "ai",
	"de", "at", "ch", "uk", "fr", "nl", "pl", "ua", "ru", "us",
	"info", "biz", "gov", "edu",
}

func checkNoIdentifiers(t *testing.T, path, body string) {
	t.Helper()

	for _, addr := range reIPv4.FindAllString(body, -1) {
		if !isNonIdentifyingIP(addr) {
			t.Errorf("%s contains the routable address %s; TST-005 requires "+
				"real-world samples to be scrubbed. Use 10.0.0.0/8 or one of the "+
				"RFC 5737 documentation ranges instead", path, addr)
		}
	}
	for _, addr := range reEmail.FindAllString(body, -1) {
		t.Errorf("%s contains the e-mail address %s; TST-005 requires it to be scrubbed", path, addr)
	}
	for _, host := range reHostname.FindAllString(body, -1) {
		last := strings.ToLower(host[strings.LastIndexByte(host, '.')+1:])
		if slices.Contains(publicTLDs, last) {
			t.Errorf("%s contains the hostname %s, which names a real host; "+
				"TST-005 requires real-world samples to be scrubbed", path, host)
		}
	}
}

var (
	reIPv4  = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	reEmail = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
	// A hostname here is a dotted name whose last label is alphabetic, which
	// is what separates host.example.com from parse_relation.c and from a
	// version number.
	reHostname = regexp.MustCompile(`\b(?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?\.)+[A-Za-z]{2,}\b`)
)

// isNonIdentifyingIP reports whether an address is one that cannot name a real
// host on the internet: the RFC 1918 private ranges, loopback, link-local, and
// the RFC 5737 ranges reserved for documentation.
func isNonIdentifyingIP(s string) bool {
	ip, err := netip.ParseAddr(s)
	if err != nil {
		// Not an address at all -- a version number, or a file offset.
		return true
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	for _, block := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return true
		}
	}
	return false
}
