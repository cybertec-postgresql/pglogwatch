package pglogwatch

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/require"
)

// templateSpare is the spare dst capacity that the AppendTemplate doc
// comment promises is always enough.
const templateSpare = 1100

// templatePrefix is written into dst before each call, so the gate checks
// spare capacity after existing bytes and not only from an empty buffer.
const templatePrefix = "prefix: "

// templateRecord is one record to template, with a name for the subtest.
type templateRecord struct {
	name string
	rec  *Record
}

// templateFixtureRecords parses every log under testdata/csv, testdata/json
// and testdata/stderr once and returns owned copies of the records, so the
// gate measures templating and not parsing.
func templateFixtureRecords(t *testing.T) []templateRecord {
	t.Helper()
	var out []templateRecord
	for _, dir := range []struct {
		name   string
		format Format
	}{
		{"csv", FormatCSV},
		{"json", FormatJSON},
		{"stderr", FormatStderr},
	} {
		paths, err := filepath.Glob(filepath.Join("testdata", dir.name, "*"))
		require.NoError(t, err)
		require.NotEmpty(t, paths, "no fixtures in testdata/%s", dir.name)
		for _, path := range paths {
			in, err := os.ReadFile(path)
			require.NoError(t, err)
			// Some fixtures end in a damaged record on purpose; the records
			// before it are still worth templating, so p.Err is not checked.
			p := New(bytes.NewReader(in), Config{Format: dir.format})
			for i := 0; p.Next(); i++ {
				out = append(out, templateRecord{
					name: filepath.Base(path) + "/" + strconv.Itoa(i),
					rec:  &p.Record().Clone().Record,
				})
			}
		}
	}
	return out
}

// TestAllocTemplate is AC-004 and CON-001: with templateSpare bytes of spare
// capacity in dst, AppendTemplate and PatternID perform no heap allocation
// and AppendTemplate never grows dst. It runs over every input of the
// template unit tests and every record of the fixture logs.
func TestAllocTemplate(t *testing.T) {
	if !allocs.Meaningful {
		t.Skip("allocation counts are not meaningful in this build")
	}
	var recs []templateRecord
	for _, c := range templateInputs() {
		recs = append(recs, templateRecord{
			name: "input/" + c.name,
			rec:  &Record{Message: []byte(c.in), SQLState: sqlState(t, c.state), Severity: SeverityError},
		})
	}
	// TODO(T013): add the privacy corpus under testdata/template/ once it has
	// a loader.
	recs = append(recs, templateFixtureRecords(t)...)

	for _, c := range recs {
		t.Run(c.name, func(t *testing.T) {
			r := c.rec
			dst := make([]byte, 0, len(templatePrefix)+templateSpare)
			dst = append(dst, templatePrefix...)
			want := cap(dst)

			allocs.Zero(t, 10, func() {
				dst = AppendTemplate(dst[:len(templatePrefix)], r)
			})
			if cap(dst) != want {
				t.Errorf("dst grew from cap %d to %d for a %d-byte message", want, cap(dst), len(r.Message))
			}
			if string(dst[:len(templatePrefix)]) != templatePrefix {
				t.Errorf("AppendTemplate changed the bytes already in dst: %q", dst[:len(templatePrefix)])
			}

			tmpl := dst[len(templatePrefix):]
			var id [8]byte
			allocs.Zero(t, 10, func() { id = PatternID(r, tmpl) })
			_ = id
		})
	}
}
