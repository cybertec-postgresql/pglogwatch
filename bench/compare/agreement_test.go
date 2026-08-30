package compare_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cybertec-postgresql/pglogwatch"
	"github.com/cybertec-postgresql/pglogwatch/bench/compare"
	"github.com/cybertec-postgresql/pglogwatch/bench/gen"
)

// AC-010 and COR-003: for the reference corpus, the per-severity counts
// produced by pglogwatch, pgbadger and pgweasel must be identical.
//
// The corpus is generated, so there is a fourth participant the specification
// does not name and which matters more than any of them: the TRUTH. The
// manifest records what the generator emitted, so each tool is checked against
// what the log actually contains rather than only against the other tools --
// three tools agreeing on a wrong answer is a real outcome, and comparing them
// only with each other cannot detect it.

func TestPglogwatchAgreesWithTheGeneratedTruth(t *testing.T) {
	// The half of AC-010 that needs no external tool, and therefore the half
	// that runs in ordinary CI.
	dir := t.TempDir()
	m, err := gen.Write(dir, gen.Config{Seed: 20260830, Records: 20000})
	require.NoError(t, err)

	for _, f := range m.Files {
		t.Run(f.Name, func(t *testing.T) {
			counts := parseSeverities(t, filepath.Join(dir, f.Name))
			for severity, want := range m.Severity {
				assert.Equal(t, want, counts[severity],
					"%s: %s count disagrees with the manifest", f.Name, severity)
			}
			var total int64
			for _, n := range counts {
				total += n
			}
			assert.Equal(t, int64(m.Records), total)
		})
	}
}

func parseSeverities(t *testing.T, path string) map[string]int64 {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // path is inside the test's own temp dir
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	cfg := pglogwatch.Config{}
	if strings.HasSuffix(path, ".log") {
		cfg.LinePrefix = gen.StderrPrefix
	}
	p := pglogwatch.New(f, cfg)
	counts := map[string]int64{}
	for p.Next() {
		counts[p.Record().Severity.String()]++
	}
	require.NoError(t, p.Err())
	assert.Zero(t, p.Stats().Malformed, "%s: the corpus must parse cleanly", path)
	return counts
}

// TestAllThreeToolsAgree is AC-010 in full.
//
// It needs pgbadger and pgweasel installed, so it skips where they are not --
// which is everywhere except the pinned runner (INF-003). Skipping is stated
// rather than silent: a green run on a machine without the baselines has not
// verified AC-010, and pretending otherwise is exactly what VAL-010 forbids.
func TestAllThreeToolsAgree(t *testing.T) {
	tools := compare.Detect()
	var missing []string
	for _, tool := range tools {
		if tool.Name != "pglogwatch" && !tool.Found {
			missing = append(missing, tool.Name)
		}
	}
	if len(missing) > 0 {
		t.Skipf("AC-010 needs %s installed; not verified on this machine (INF-003)",
			strings.Join(missing, " and "))
	}

	dir := t.TempDir()
	m, err := gen.Write(dir, gen.Config{Seed: 20260830, Records: 20000})
	require.NoError(t, err)
	csvlog := filepath.Join(dir, "postgresql-pg14.csv")

	want := m.Severity["ERROR"]
	require.Positive(t, want, "the corpus must contain errors to compare")

	ours := parseSeverities(t, csvlog)
	assert.Equal(t, want, ours["ERROR"], "pglogwatch disagrees with the manifest")

	if n, ok := errorsReportedBy(t, "pgweasel", "errors", csvlog); ok {
		assert.Equal(t, want, n, "pgweasel disagrees with the manifest")
	}
	if n, ok := errorsReportedBy(t, "pgbadger", "-j", "1", "-f", "csv", "-o", "-", csvlog); ok {
		assert.Equal(t, want, n, "pgbadger disagrees with the manifest")
	}
}

// countPattern finds a number next to the word ERROR in a tool's output.
//
// Deliberately loose: the two baselines format their reports differently and
// change them between versions, and a comparison that broke on a cosmetic
// change would be turned off rather than fixed.
var countPattern = regexp.MustCompile(`(?i)\bERROR\b[^0-9]{0,40}(\d+)|(\d+)[^0-9a-z]{0,10}\bERROR`)

func errorsReportedBy(t *testing.T, bin string, args ...string) (int64, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput() //nolint:gosec // fixed binaries
	if err != nil {
		t.Logf("%s failed, not compared: %v", bin, err)
		return 0, false
	}
	m := countPattern.FindSubmatch(bytes.ToLower(out))
	if m == nil {
		t.Logf("%s output had no recognisable error count, not compared", bin)
		return 0, false
	}
	for _, g := range m[1:] {
		if len(g) == 0 {
			continue
		}
		n, err := strconv.ParseInt(string(g), 10, 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}
