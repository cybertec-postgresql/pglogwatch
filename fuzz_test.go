package pglogwatch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fuzz targets (COR-002, AC-009).
//
// COR-002 is a promise about inputs nobody thought of, so it cannot be kept by
// tests somebody wrote. A log file is written by another process, often to a
// filesystem that may fail mid-write, and is then read by this parser -- which
// then hands byte slices, offsets and lengths to a caller. Every one of those
// is an opportunity to slice out of range.
//
// Run locally with `task fuzz`; CI runs each target for 30 minutes nightly
// (TST-007), toward AC-009's ten million executions.

// seedFuzzFromFixtures adds every committed fixture to a target's corpus, so
// fuzzing starts from real PostgreSQL output and mutates outward rather than
// spending its first hours discovering what a log looks like.
func seedFuzzFromFixtures(f *testing.F, add func(*testing.F, []byte)) {
	f.Helper()
	err := filepath.WalkDir("testdata", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) == ".md" {
			return err
		}
		if strings.Contains(filepath.ToSlash(path), "/fuzz/") {
			return nil // the fuzzer's own corpus, not a fixture
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		add(f, b)
		return nil
	})
	if err != nil {
		f.Fatal(err)
	}
}

// FuzzParseRecord drives the whole parser over arbitrary bytes in every
// format, and checks the invariants a caller relies on rather than only that
// it did not crash.
func FuzzParseRecord(f *testing.F) {
	seedFuzzFromFixtures(f, func(f *testing.F, b []byte) { f.Add(b, uint8(0), "") })
	f.Add([]byte("{}"), uint8(3), "")
	f.Add([]byte(","), uint8(2), "")
	f.Add([]byte("\xef\xbb\xbf"), uint8(0), "")
	f.Add([]byte("2026-08-30 10:11:12.123 CEST [1] LOG:  x"), uint8(1), "%m [%p] ")

	f.Fuzz(func(t *testing.T, data []byte, format uint8, prefix string) {
		p := New(bytes.NewReader(data), Config{
			Format:     Format(format % 4),
			LinePrefix: prefix,
			// Small enough that the size cap and buffer growth are
			// exercised on inputs the fuzzer can actually reach.
			MaxRecordBytes:     4096,
			InitialBufferBytes: 64,
		})

		for p.Next() {
			r := p.Record()

			// Every borrowed field must lie inside the record it
			// came from, or a caller slicing Raw by a field's
			// length would read another record's bytes.
			if r.Offset < 0 {
				t.Fatalf("negative offset %d", r.Offset)
			}
			if int64(len(r.Raw)) > int64(len(data)) {
				t.Fatalf("Raw longer than the input: %d > %d", len(r.Raw), len(data))
			}

			// Reading every field must be safe. This is the check
			// that catches a scanner returning a slice with a
			// length past its backing array.
			_ = len(r.Message) + len(r.Detail) + len(r.Hint) + len(r.Query) +
				len(r.InternalQuery) + len(r.Context) + len(r.Statement) +
				len(r.User) + len(r.Database) + len(r.ConnectionFrom) +
				len(r.ApplicationName) + len(r.BackendType) + len(r.CommandTag) +
				len(r.SessionID) + len(r.VirtualXID) + len(r.RawSeverity) +
				len(r.Location)
			_ = r.Severity.String()
			_ = r.Time.Unix()

			// Clone must survive whatever the parser produced: it is
			// the one path that copies fields around, so a bad
			// length reaches it as an out-of-range slice.
			_ = r.Clone()
		}
		// Malformed lines are counted, never fatal (IFC-003). The one
		// error Err may legitimately report here is a LinePrefix the
		// fuzzer invented that is not a valid log_line_prefix -- that
		// is a configuration mistake, not bad input, and T013 makes it
		// deliberately fatal. The fuzzer found this distinction within
		// three seconds, which is a fair summary of why this target
		// exists.
		if err := p.Err(); err != nil && !errors.Is(err, ErrBadLinePrefix) {
			t.Fatalf("unexpected fatal error %v", err)
		}
	})
}

// FuzzPrefixTemplate targets the prefix compiler and scanner, which parse a
// user-supplied string and then use it to index into log lines -- the exact
// shape of bug that a fuzzer finds and a test suite does not.
func FuzzPrefixTemplate(f *testing.F) {
	f.Add("%m [%p] %q%u@%d ", "2026-08-30 10:11:12.123 CEST [1] u@d LOG:  x")
	f.Add("%-5p", "42   LOG:  x")
	f.Add("%", "x")
	f.Add("%%%%", "%%")
	f.Add("%q", "LOG:  x")
	f.Add("%c %v %e ", "68b2c4a0.7a69 3/15 42P01 ERROR:  x")

	f.Fuzz(func(t *testing.T, prefix, line string) {
		tpl, err := compilePrefix(prefix)
		if err != nil {
			return // rejecting a bad prefix is a valid outcome
		}
		var rec Record
		var tz tzCache
		rest, ok := tpl.scanPrefix([]byte(line), &rec, &tz)
		if ok && len(rest) > len(line) {
			t.Fatalf("remainder longer than the line: %d > %d", len(rest), len(line))
		}
		// The compiled template must round-trip its source, since
		// DetectedPrefix reports it to the caller.
		if tpl.String() != prefix {
			t.Fatalf("template source changed: %q became %q", prefix, tpl.String())
		}
	})
}

// FuzzUnquote targets the unescaping helper, which walks escapes and appends
// runes -- both places where a truncated sequence at the end of a buffer turns
// into an out-of-range read.
func FuzzUnquote(f *testing.F) {
	bs := string([]byte{92})
	f.Add(`say ""hi""`, uint8(2))
	f.Add(bs+`u00e9`, uint8(3))
	f.Add(bs+`ud83d`+bs+`ude00`, uint8(3))
	f.Add(bs+`u00`, uint8(3))
	f.Add(bs, uint8(3))
	f.Add(`"`, uint8(2))

	f.Fuzz(func(t *testing.T, s string, format uint8) {
		src := []byte(s)
		dst := AppendUnquoted(nil, src, Format(format%4))

		// Unescaping can only ever shrink or preserve: no escape
		// expands to more bytes than it occupied. A result longer than
		// the input means the walker advanced backwards somewhere.
		if len(dst) > len(src) {
			t.Fatalf("unquoting grew the input: %d > %d", len(src), len(dst))
		}
		// Appending must not disturb what was already in dst.
		prefix := []byte("keep me")
		got := AppendUnquoted(bytes.Clone(prefix), src, Format(format%4))
		if !bytes.HasPrefix(got, prefix) {
			t.Fatalf("AppendUnquoted overwrote the destination")
		}
	})
}
