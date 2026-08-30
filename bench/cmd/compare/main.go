// Command compare runs the §6.4 comparative benchmark and writes the results
// table (TST-011, TST-013).
//
// Run through the Makefile:
//
//	make corpus         # generate the corpus first
//	make bench-compare  # measure against whatever is installed
//
// A tool that is not installed is reported as not measured rather than failing
// the run, so the harness is usable in a checkout as well as on the pinned
// benchmark runner.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/cybertec-postgresql/pglogwatch/bench/compare"
	"github.com/cybertec-postgresql/pglogwatch/bench/gen"
)

func main() {
	var (
		corpus = flag.String("corpus", "corpus", "corpus directory")
		runs   = flag.Int("runs", 10, "measured runs per cell (TST-011 requires at least 10)")
		warmup = flag.Int("warmup", 3, "unmeasured warm-up runs per cell")
		out    = flag.String("out", "RESULTS.md", "results table path; - for standard output")
		cli    = flag.String("cli", "../cmd/pglogwatch", "directory of the pglogwatch CLI module")
	)
	flag.Parse()

	ctx := context.Background()

	// Build the CLI from this checkout, so the comparison measures the code
	// in the working tree rather than whatever is installed -- and so the
	// harness works in a fresh clone with no install step.
	tools := compare.Detect()
	if bin, cleanup, err := compare.BuildSelf(ctx, *cli); err == nil {
		defer cleanup()
		tools = compare.WithBinary(tools, "pglogwatch", bin, "from "+*cli)
	} else {
		fmt.Fprintln(os.Stderr, "compare:", err)
	}

	results, tools, err := compare.Run(ctx, compare.Config{
		CorpusDir: *corpus,
		Runs:      *runs,
		Warmup:    *warmup,
		Tools:     tools,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		os.Exit(1)
	}

	env := compare.DescribeEnvironment(gen.CorpusVersion)

	w := os.Stdout
	if *out != "-" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "compare:", err)
			os.Exit(1)
		}
		defer func() { _ = f.Close() }()
		w = f
	}
	if err := compare.WriteReport(w, env, tools, results); err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		os.Exit(1)
	}
	if *out != "-" {
		fmt.Fprintf(os.Stderr, "results written to %s\n", *out)
	}
}
