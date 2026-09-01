// Command pglogwatch reads PostgreSQL log files and reports what is in them.
//
// It is the reference CLI for the pglogwatch parser, and its subcommands
// deliberately mirror pgweasel's so that the two can be benchmarked
// like-for-like (§7.9). Without a comparable CLI there is no way to run the
// comparison in §6.4, and no way for a reader to reproduce a published claim.
//
// Usage:
//
//	pglogwatch <command> [flags] [paths...]
//
// With no paths it reads standard input (IFC-010).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Exit codes (IFC-012).
const (
	exitOK       = 0
	exitError    = 1 // usage or I/O error
	exitNoInput  = 2 // nothing matched
	exitInternal = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main with its environment passed in, so tests can drive the whole CLI
// without a subprocess and can inspect both output streams separately.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitError
	}
	name := args[0]
	if name == "-h" || name == "--help" || name == "help" {
		usage(stdout)
		return exitOK
	}

	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(stderr, "pglogwatch: unknown command %q\n\n", name) //nolint:errcheck // diagnostic
		usage(stderr)
		return exitError
	}

	opts := &options{stdin: stdin, stdout: stdout, stderr: stderr}
	fs := flag.NewFlagSet("pglogwatch "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts.register(fs)
	cmd.flags(fs, opts)

	if err := fs.Parse(args[1:]); err != nil {
		return exitError
	}
	opts.paths = fs.Args()

	if err := opts.normalize(); err != nil {
		fmt.Fprintf(stderr, "pglogwatch: %v\n", err) //nolint:errcheck // diagnostic
		return exitError
	}

	if err := cmd.run(opts); err != nil {
		if err == errNoInput {
			fmt.Fprintln(stderr, "pglogwatch: no input matched") //nolint:errcheck // diagnostic
			return exitNoInput
		}
		fmt.Fprintf(stderr, "pglogwatch: %v\n", err) //nolint:errcheck // diagnostic
		return exitError
	}
	return exitOK
}

// command is one subcommand.
type command struct {
	summary string
	flags   func(*flag.FlagSet, *options) // subcommand-specific flags
	run     func(*options) error
}

// noFlags is the flags function for a subcommand that has none of its own.
func noFlags(*flag.FlagSet, *options) {}

// usage writes the help text.
//
// The writes are unchecked. The only response to a failed write of the help
// text would be to report it, and the writer that would carry that report is
// the one that just failed.
//
//nolint:errcheck // help text; a failed write has nowhere to be reported
func usage(w io.Writer) {
	fmt.Fprintln(w, "pglogwatch - read PostgreSQL log files")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  pglogwatch <command> [flags] [paths...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "With no paths, reads standard input.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, name := range commandNames() {
		fmt.Fprintf(w, "  %-12s %s\n", name, commands[name].summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --format string        stderr, csvlog or jsonlog (default: detect)")
	fmt.Fprintln(w, "  --line-prefix string   log_line_prefix for stderr (default: detect)")
	fmt.Fprintln(w, "  --lang string          lc_messages of the server (default: en)")
	fmt.Fprintln(w, "  --begin time           ignore records before this time")
	fmt.Fprintln(w, "  --end time             ignore records at or after this time")
	fmt.Fprintln(w, "  --jobs int             parallel workers (default: one per CPU)")
	fmt.Fprintln(w, "  --output string        text or json (default: text)")
	fmt.Fprintln(w, "  --no-color             never colourise output")
	fmt.Fprintln(w, "  --top int              how many rows in a top-N report (default 10)")
}

// timeFormats are accepted for --begin and --end, most specific first.
//
// A person typing a time on a command line writes as little as they can get
// away with, so a date alone has to work.
var timeFormats = []string{
	"2006-01-02 15:04:05.999",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	time.RFC3339,
}

func parseTimeArg(s string) (time.Time, error) {
	for _, layout := range timeFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q; try 2006-01-02 15:04:05", s)
}

func commandNames() []string {
	// A fixed order rather than a sorted one, so related commands read
	// together in the help text.
	return []string{
		"parse", "stats", "errors", "slow", "connections",
		"locks", "peaks", "system", "grep", "bench",
	}
}

// truncate shortens s for display, marking that it was cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// oneLine collapses a multi-line message so a report stays tabular.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	r := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
	return strings.Join(strings.Fields(r.Replace(s)), " ")
}
