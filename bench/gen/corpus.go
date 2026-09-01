package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CorpusVersion names the corpus a benchmark result was measured against.
//
// TST-013 requires every published figure to cite it, and GUD-006 requires the
// figure to be reproducible. Bumping this is how a change to the generator is
// made visible: a result citing corpus-v1 and one citing corpus-v2 are not
// comparable, and the version is the only thing that says so.
const CorpusVersion = "corpus-v1"

// File describes one generated file in the manifest.
type File struct {
	Name    string
	Format  string
	Layout  string
	Bytes   int64
	Records int
}

// Manifest describes a generated corpus (TST-003).
//
// It is committed; the payload is not (DAT-001). That is what lets a reviewer
// confirm two runs used the same corpus without either of them shipping
// gigabytes of log files.
type Manifest struct {
	Version   string
	Seed      uint64
	Records   int
	Files     []File
	Severity  map[string]int64
	TotalSize int64
}

// Write generates a corpus into dir and returns its manifest.
//
// Every format and every csvlog layout is written from the SAME event stream
// (TST-002), so a difference between two parses of this corpus is a difference
// between parsers rather than between inputs.
func Write(dir string, cfg Config) (*Manifest, error) {
	cfg.normalize()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	events := Generate(cfg)

	m := &Manifest{
		Version:  CorpusVersion,
		Seed:     cfg.Seed,
		Records:  len(events),
		Severity: SeverityHistogram(events),
	}

	write := func(name, format, layout string, fn func(*os.File) (Written, error)) error {
		path := filepath.Join(dir, name)
		f, err := os.Create(path) //nolint:gosec // path is built from dir
		if err != nil {
			return err
		}
		w, err := fn(f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		m.Files = append(m.Files, File{
			Name: name, Format: format, Layout: layout,
			Bytes: w.Bytes, Records: w.Records,
		})
		m.TotalSize += w.Bytes
		return nil
	}

	if err := write("postgresql.log", "stderr", "", func(f *os.File) (Written, error) {
		return WriteStderr(f, events)
	}); err != nil {
		return nil, err
	}
	if err := write("postgresql.json", "jsonlog", "", func(f *os.File) (Written, error) {
		return WriteJSON(f, events)
	}); err != nil {
		return nil, err
	}
	for _, layout := range AllLayouts {
		name := "postgresql-" + layout.String() + ".csv"
		if err := write(name, "csvlog", layout.String(), func(f *os.File) (Written, error) {
			return WriteCSV(f, events, layout)
		}); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// MarshalText renders the manifest.
//
// Written by hand as sorted key-value lines rather than as JSON, because this
// file is committed and read in diffs: a stable line-oriented format shows a
// changed record count as one changed line, where a re-marshalled JSON object
// can reorder everything.
func (m *Manifest) MarshalText() ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("# pglogwatch benchmark corpus manifest (TST-003, DAT-001).\n")
	sb.WriteString("# The payload is NOT committed; regenerate it with: task corpus\n")
	sb.WriteString("# A benchmark figure must cite this version (TST-013, GUD-006).\n\n")
	fmt.Fprintf(&sb, "version %s\n", m.Version)
	fmt.Fprintf(&sb, "seed %d\n", m.Seed)
	fmt.Fprintf(&sb, "records %d\n", m.Records)
	fmt.Fprintf(&sb, "total_bytes %d\n", m.TotalSize)

	sb.WriteString("\n# file  format  layout  bytes  records\n")
	for _, f := range m.Files {
		layout := f.Layout
		if layout == "" {
			layout = "-"
		}
		fmt.Fprintf(&sb, "file %s %s %s %d %d\n", f.Name, f.Format, layout, f.Bytes, f.Records)
	}

	sb.WriteString("\n# severity histogram: the counts every tool must agree on (AC-010)\n")
	keys := make([]string, 0, len(m.Severity))
	for k := range m.Severity {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&sb, "severity %s %s\n", k, strconv.FormatInt(m.Severity[k], 10))
	}
	return []byte(sb.String()), nil
}
