package bench_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cybertec-postgresql/pglogwatch"
	"github.com/cybertec-postgresql/pglogwatch/bench/gen"
)

// Throughput against the real corpus (PERF-020 through PERF-023, AC-015).
//
// The unit benchmarks in the root package measure a fixture repeated to 1 MB,
// which stays in L2 and reports the parser's best case. These measure the
// generated corpus at a size that does not, which is the number a PERF-0xx
// threshold is actually about.
//
// The corpus is generated into a temporary directory on first use rather than
// read from bench/corpus, so this runs in a clean checkout. Set
// PGLOGWATCH_BENCH_CORPUS to point at a larger one.

const benchRecords = 200000

// corpusDir returns a directory holding the corpus, generating it once.
func corpusDir(tb testing.TB) string {
	tb.Helper()
	if dir := os.Getenv("PGLOGWATCH_BENCH_CORPUS"); dir != "" {
		return dir
	}
	dir := filepath.Join(os.TempDir(), "pglogwatch-corpus-"+gen.CorpusVersion)
	marker := filepath.Join(dir, "postgresql.json")
	if _, err := os.Stat(marker); err == nil {
		return dir
	}
	if _, err := gen.Write(dir, gen.Config{Seed: 20260830, Records: benchRecords}); err != nil {
		tb.Fatalf("generating corpus: %v", err)
	}
	return dir
}

func benchmarkCorpus(b *testing.B, file string, cfg pglogwatch.Config, touch func(*pglogwatch.Record)) {
	path := filepath.Join(corpusDir(b), file)
	info, err := os.Stat(path)
	if err != nil {
		b.Fatalf("corpus file %s: %v", file, err)
	}

	b.SetBytes(info.Size())
	b.ReportAllocs()
	for b.Loop() {
		f, err := os.Open(path) //nolint:gosec // generated corpus
		if err != nil {
			b.Fatal(err)
		}
		p := pglogwatch.New(f, cfg)
		for p.Next() {
			touch(p.Record())
		}
		if err := p.Err(); err != nil {
			b.Fatal(err)
		}
		if err := f.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

var sink int64

// BenchmarkCorpusCSVFullParse is PERF-020: floor 250 MB/s.
func BenchmarkCorpusCSVFullParse(b *testing.B) {
	benchmarkCorpus(b, "postgresql-pg14.csv",
		pglogwatch.Config{Format: pglogwatch.FormatCSV},
		func(r *pglogwatch.Record) {
			sink += r.Time.UnixNano() + int64(r.Severity) + int64(len(r.Message)) +
				int64(len(r.User)) + int64(len(r.Database)) + int64(r.ProcessID) +
				int64(len(r.Statement)) + r.QueryID
		})
}

// BenchmarkCorpusCSVSeverityOnly is PERF-021: floor 800 MB/s.
func BenchmarkCorpusCSVSeverityOnly(b *testing.B) {
	benchmarkCorpus(b, "postgresql-pg14.csv",
		pglogwatch.Config{Format: pglogwatch.FormatCSV},
		func(r *pglogwatch.Record) { sink += int64(r.Severity) })
}

// BenchmarkCorpusStderrFullParse is PERF-022: floor 200 MB/s.
func BenchmarkCorpusStderrFullParse(b *testing.B) {
	benchmarkCorpus(b, "postgresql.log",
		pglogwatch.Config{Format: pglogwatch.FormatStderr, LinePrefix: gen.StderrPrefix},
		func(r *pglogwatch.Record) {
			sink += r.Time.UnixNano() + int64(r.Severity) + int64(len(r.Message)) +
				int64(len(r.User)) + int64(len(r.Database)) + int64(r.ProcessID) +
				int64(len(r.Statement))
		})
}

// BenchmarkCorpusJSONFullParse is PERF-023: floor 150 MB/s.
func BenchmarkCorpusJSONFullParse(b *testing.B) {
	benchmarkCorpus(b, "postgresql.json",
		pglogwatch.Config{Format: pglogwatch.FormatJSON},
		func(r *pglogwatch.Record) {
			sink += r.Time.UnixNano() + int64(r.Severity) + int64(len(r.Message)) +
				int64(len(r.User)) + int64(len(r.Database)) + int64(r.ProcessID) +
				int64(len(r.Statement)) + r.QueryID
		})
}

// BenchmarkCorpusAutoDetected measures the configuration a caller actually
// uses -- Config{} -- so the cost of detection is visible rather than assumed
// to be zero.
func BenchmarkCorpusAutoDetected(b *testing.B) {
	benchmarkCorpus(b, "postgresql-pg14.csv", pglogwatch.Config{},
		func(r *pglogwatch.Record) { sink += int64(r.Severity) })
}
