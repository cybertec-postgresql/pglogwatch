// Command corpus generates the benchmark corpus (TST-001, TST-003).
//
// Run through the Taskfile:
//
//	task corpus                 # the default corpus
//	task corpus RECORDS=5000000 # a larger one, same seed
//
// The payload is written to bench/corpus/ and is not committed; the manifest
// is (DAT-001).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cybertec-postgresql/pglogwatch/bench/gen"
)

func main() {
	var (
		dir      = flag.String("dir", "corpus", "directory to write the corpus into")
		manifest = flag.String("manifest", "corpus-v1.manifest", "manifest path")
		seed     = flag.Uint64("seed", 20260830, "generator seed")
		records  = flag.Int("records", 200000, "number of log events")
	)
	flag.Parse()

	m, err := gen.Write(*dir, gen.Config{Seed: *seed, Records: *records})
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}
	text, err := m.MarshalText()
	if err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*manifest, text, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "corpus:", err)
		os.Exit(1)
	}

	fmt.Printf("%s: %d records, %d bytes across %d files in %s\n",
		m.Version, m.Records, m.TotalSize, len(m.Files), filepath.Clean(*dir))
	fmt.Printf("manifest written to %s\n", *manifest)
}
