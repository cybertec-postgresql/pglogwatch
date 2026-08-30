package pglogwatch

import (
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
)

// bs is a single backslash. Written this way because the surrounding tests are
// entirely about backslashes, and a Go literal that escapes them is unreadable
// at exactly the moment the reader most needs to count them.
const bs = "\\"

func TestAppendUnquotedCSV(t *testing.T) {
	cases := []struct{ in, want string }{
		{`say ""hi""`, `say "hi"`},
		{`""`, `"`},
		{`""""`, `""`},
		{`no escapes here`, `no escapes here`},
		{``, ``},
		// The reason AppendUnquoted takes a Format: csvlog writes
		// backslashes literally, so a Windows path and a regular
		// expression must survive untouched.
		{`C:` + bs + `data` + bs + `pg_log`, `C:` + bs + `data` + bs + `pg_log`},
		{`\n is two characters`, `\n is two characters`},
		// A newline inside a quoted field is data (E2).
		{"line one\nline two", "line one\nline two"},
	}
	for _, c := range cases {
		got := string(AppendUnquoted(nil, []byte(c.in), FormatCSV))
		assert.Equal(t, c.want, got, "csv %q", c.in)
	}
}

func TestAppendUnquotedJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{bs + `"quoted` + bs + `"`, `"quoted"`},
		{`C:` + bs + bs + `data`, `C:` + bs + `data`},
		{bs + `/`, `/`},
		{bs + `b`, "\b"},
		{bs + `f`, "\f"},
		{bs + `n`, "\n"},
		{bs + `r`, "\r"},
		{bs + `t`, "\t"},
		{bs + `u00e9`, "é"},
		{bs + `u0041`, "A"},
		{bs + `ud83d` + bs + `ude00`, "\U0001F600"},
		{`plain`, `plain`},
	}
	for _, c := range cases {
		got := string(AppendUnquoted(nil, []byte(c.in), FormatJSON))
		assert.Equal(t, c.want, got, "json %q", c.in)
	}
}

func TestAppendUnquotedJSONMalformedIsPreserved(t *testing.T) {
	// COR-005's reasoning applied to escapes: this function undoes
	// PostgreSQL's escaping, it does not judge the log's contents. An
	// escape it does not recognise comes back intact so the original text
	// is still recoverable.
	cases := []struct{ in, want string }{
		{bs + `q`, bs + `q`},
		{bs + `uZZZZ`, bs + `uZZZZ`},
		{bs + `u00`, bs + `u00`}, // truncated
		{bs, bs},                 // trailing lone backslash
		{bs + `ud83d`, "�"},      // lone high surrogate
		{bs + `ude00`, "�"},      // lone low surrogate
	}
	for _, c := range cases {
		got := string(AppendUnquoted(nil, []byte(c.in), FormatJSON))
		assert.Equal(t, c.want, got, "json %q", c.in)
	}
}

func TestAppendUnquotedPassthrough(t *testing.T) {
	// stderr output carries no escaping at all.
	for _, f := range []Format{FormatStderr, FormatAuto} {
		got := string(AppendUnquoted(nil, []byte(bs+`n and ""`), f))
		assert.Equal(t, bs+`n and ""`, got, "format %s", f)
	}
}

func TestAppendUnquotedAppends(t *testing.T) {
	// The dst-append shape is what lets a caller reuse one buffer across a
	// whole scan; check it actually appends rather than replacing.
	dst := []byte("prefix:")
	dst = AppendUnquoted(dst, []byte(`a""b`), FormatCSV)
	assert.Equal(t, `prefix:a"b`, string(dst))
}

// TestAllocUnquote checks the promise the deferred design rests on: a caller
// that reuses its own buffer pays nothing after it has grown (PERF-009).
func TestAllocUnquote(t *testing.T) {
	src := []byte(`a very long message with ""quotes"" in the middle of it`)
	buf := make([]byte, 0, 256)
	allocs.Zero(t, 100, func() {
		buf = AppendUnquoted(buf[:0], src, FormatCSV)
	})
}

func TestHex4(t *testing.T) {
	v, ok := hex4([]byte(bs + `u00e9`))
	assert.True(t, ok)
	assert.Equal(t, uint32(0xe9), v)

	v, ok = hex4([]byte(bs + `uFFFF`))
	assert.True(t, ok)
	assert.Equal(t, uint32(0xFFFF), v)

	_, ok = hex4([]byte(bs + `uzz00`))
	assert.False(t, ok)

	_, ok = hex4([]byte(`u00e9`))
	assert.False(t, ok, "must require the backslash")
}
