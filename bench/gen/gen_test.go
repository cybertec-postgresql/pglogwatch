package gen_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybertec-postgresql/pglogwatch"
	"github.com/cybertec-postgresql/pglogwatch/bench/gen"
)

// Corpus determinism (TST-003).
//
// A benchmark figure cites a corpus version (TST-013) and must be reproducible
// (GUD-006). Neither means anything unless the same seed produces byte-identical
// output -- otherwise two people running "make corpus" measure different things
// and compare the results anyway.

func TestSameSeedProducesIdenticalOutput(t *testing.T) {
	cfg := gen.Config{Seed: 42, Records: 500}

	first := renderAll(t, cfg)
	second := renderAll(t, cfg)

	for name, want := range first {
		assert.Equal(t, want, second[name],
			"%s differs between two runs with the same seed", name)
	}
}

func TestDifferentSeedsProduceDifferentOutput(t *testing.T) {
	// The other half: a seed that changed nothing would make determinism
	// trivially true and the corpus useless for varying a benchmark.
	a := renderAll(t, gen.Config{Seed: 1, Records: 500})
	b := renderAll(t, gen.Config{Seed: 2, Records: 500})
	assert.NotEqual(t, a["stderr"], b["stderr"])
}

func TestManifestMatchesWhatWasWritten(t *testing.T) {
	dir := t.TempDir()
	m, err := gen.Write(dir, gen.Config{Seed: 7, Records: 1000})
	require.NoError(t, err)

	require.Len(t, m.Files, 5, "stderr, jsonlog and three csvlog layouts")
	var total int64
	for _, f := range m.Files {
		info, err := os.Stat(filepath.Join(dir, f.Name))
		require.NoError(t, err, "manifest names %s", f.Name)
		assert.Equal(t, info.Size(), f.Bytes,
			"%s: manifest says %d bytes, file is %d", f.Name, f.Bytes, info.Size())
		assert.Equal(t, 1000, f.Records)
		total += f.Bytes
	}
	assert.Equal(t, total, m.TotalSize)

	var severityTotal int64
	for _, n := range m.Severity {
		severityTotal += n
	}
	assert.Equal(t, int64(1000), severityTotal,
		"the histogram must account for every record")

	text, err := m.MarshalText()
	require.NoError(t, err)
	assert.Contains(t, string(text), "version corpus-v1")
	assert.Contains(t, string(text), "seed 7")
}

func TestManifestIsStableForTheSameSeed(t *testing.T) {
	// The manifest is committed, so a regeneration that changed nothing
	// must produce no diff.
	a, err := gen.Write(t.TempDir(), gen.Config{Seed: 99, Records: 300})
	require.NoError(t, err)
	b, err := gen.Write(t.TempDir(), gen.Config{Seed: 99, Records: 300})
	require.NoError(t, err)

	ta, err := a.MarshalText()
	require.NoError(t, err)
	tb, err := b.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, string(ta), string(tb))
}

// renderAll renders one configuration into every format.
func renderAll(t *testing.T, cfg gen.Config) map[string][]byte {
	t.Helper()
	events := gen.Generate(cfg)
	out := map[string][]byte{}

	var b bytes.Buffer
	_, err := gen.WriteStderr(&b, events)
	require.NoError(t, err)
	out["stderr"] = bytes.Clone(b.Bytes())

	b.Reset()
	_, err = gen.WriteJSON(&b, events)
	require.NoError(t, err)
	out["jsonlog"] = bytes.Clone(b.Bytes())

	for _, layout := range gen.AllLayouts {
		b.Reset()
		_, err = gen.WriteCSV(&b, events, layout)
		require.NoError(t, err)
		out["csvlog-"+layout.String()] = bytes.Clone(b.Bytes())
	}
	return out
}

// severityCounts parses a rendering and counts severities the way any consumer
// would.
func severityCounts(t *testing.T, data []byte, cfg pglogwatch.Config) (map[string]int64, int64) {
	t.Helper()
	p := pglogwatch.New(bytes.NewReader(data), cfg)
	counts := map[string]int64{}
	var total int64
	for p.Next() {
		counts[p.Record().Severity.String()]++
		total++
	}
	require.NoError(t, p.Err())
	assert.Zero(t, p.Stats().Malformed,
		"the generated corpus must contain nothing the parser rejects")
	return counts, total
}
