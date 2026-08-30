package gen_test

import (
	"bytes"
	"encoding/csv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybertec-postgresql/pglogwatch"
	"github.com/cybertec-postgresql/pglogwatch/bench/gen"
)

// TestEveryFormatDescribesTheSameActivity is TST-002 and COR-004 applied to
// the corpus itself.
//
// If the three renderings disagreed, every comparative result drawn from them
// would be measuring the corpus rather than the parsers -- and the disagreement
// would look exactly like a parser bug.
func TestEveryFormatDescribesTheSameActivity(t *testing.T) {
	cfg := gen.Config{Seed: 20260830, Records: 3000}
	events := gen.Generate(cfg)
	rendered := renderAll(t, cfg)

	want := gen.SeverityHistogram(events)
	wantTotal := int64(len(events))

	for name, data := range rendered {
		t.Run(name, func(t *testing.T) {
			var pcfg pglogwatch.Config
			if name == "stderr" {
				// The one rendering whose fields depend on a
				// setting the file does not record.
				pcfg.LinePrefix = gen.StderrPrefix
			}
			got, total := severityCounts(t, data, pcfg)

			assert.Equal(t, wantTotal, total,
				"%s: %d records parsed from %d events", name, total, wantTotal)
			for severity, n := range want {
				assert.Equal(t, n, got[severity],
					"%s: %s count", name, severity)
			}
		})
	}
}

func TestEveryFormatIsDetectedCorrectly(t *testing.T) {
	// The corpus must also be usable WITHOUT being told the format, since
	// that is how the CLI and the comparative harness read it.
	cfg := gen.Config{Seed: 5, Records: 1000}
	for name, data := range renderAll(t, cfg) {
		t.Run(name, func(t *testing.T) {
			p := pglogwatch.New(bytes.NewReader(data), pglogwatch.Config{})
			n := 0
			for p.Next() {
				n++
			}
			require.NoError(t, p.Err())
			assert.Equal(t, 1000, n)

			want := pglogwatch.FormatCSV
			switch name {
			case "stderr":
				want = pglogwatch.FormatStderr
			case "jsonlog":
				want = pglogwatch.FormatJSON
			}
			assert.Equal(t, want, p.DetectedFormat())
		})
	}
}

func TestCorpusExercisesTheAwkwardCases(t *testing.T) {
	// A corpus that avoided what makes parsing hard would measure a parser
	// on input it never meets, and report the number as throughput.
	cfg := gen.Config{Seed: 3, Records: 5000}
	rendered := renderAll(t, cfg)

	csv := rendered["csvlog-pg14"]
	assert.Greater(t, bytes.Count(csv, []byte("\n")), 5000,
		"csvlog must contain records spanning several physical lines")
	assert.Contains(t, string(csv), `""`,
		"csvlog must contain doubled quotes")

	js := rendered["jsonlog"]
	assert.Equal(t, 5000, bytes.Count(js, []byte("\n")),
		"jsonlog must be exactly one object per line")
	assert.Contains(t, string(js), `\n`,
		"jsonlog must contain escaped newlines")

	stderr := rendered["stderr"]
	assert.Contains(t, string(stderr), "STATEMENT:",
		"stderr must contain continuation lines")
	assert.Contains(t, string(stderr), "DETAIL:")
}

// TestCSVLayoutsHaveTheRightColumnCount is the check that would have caught the
// off-by-two in the renderer, and did not exist when it was written.
//
// Two columns short is invisible in a severity histogram: severity is column
// 12, so it stays correct while everything after column 16 shifts. The 26-column
// layout came out at 24 and was read as the 24-column one, producing exactly
// right counts from the wrong layout -- and only the narrowest layout, which
// fell below the 23-column minimum, failed loudly enough to notice.
func TestCSVLayoutsHaveTheRightColumnCount(t *testing.T) {
	events := gen.Generate(gen.Config{Seed: 11, Records: 200})

	for _, layout := range gen.AllLayouts {
		t.Run(layout.String(), func(t *testing.T) {
			var b bytes.Buffer
			_, err := gen.WriteCSV(&b, events, layout)
			require.NoError(t, err)

			r := csv.NewReader(bytes.NewReader(b.Bytes()))
			r.FieldsPerRecord = -1
			rows, err := r.ReadAll()
			require.NoError(t, err)
			require.Len(t, rows, len(events))

			for i, row := range rows {
				require.Len(t, row, int(layout),
					"record %d has %d columns, want %d", i, len(row), int(layout))
			}
			// Column 12 is the severity, and column 20 the query: the two
			// the shift moved past without disturbing.
			assert.Contains(t, []string{"LOG", "ERROR"}, rows[0][11])
		})
	}
}
