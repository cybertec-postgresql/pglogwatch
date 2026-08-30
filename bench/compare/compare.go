// Package compare runs the §6.4 comparative benchmark: pglogwatch against
// pgbadger and pgweasel, over one corpus, on one machine.
//
// The design is shaped by TST-012 more than by anything else. pgbadger's
// default output is a full HTML report, pgweasel's subcommands are close to
// pglogwatch's but not identical, and comparing a text histogram against an
// HTML report as though they were the same work would be dishonest. So every
// workload records what each tool was ASKED to do and what it PRODUCED, and
// the results table carries both -- an unequal comparison is reported as
// unequal rather than quietly averaged away.
package compare

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tool is one program under comparison.
type Tool struct {
	Name    string
	Binary  string
	Version string // filled in by Detect
	Found   bool
}

// Tools are the three baselines §6.4 names.
//
// pglogwatch is run from source through "go run" when its binary is not built,
// so the harness works in a checkout without an install step.
var Tools = []string{"pglogwatch", "pgbadger", "pgweasel"}

// Workload is one row of the §6.4 table.
type Workload struct {
	ID   string
	Name string

	// Args per tool. A tool with no entry does not participate, which is
	// recorded rather than hidden.
	Args map[string][]string

	// Produces describes what each tool actually emits, for TST-012. The
	// results table prints it beside the timings so nobody reads two
	// different jobs as one comparison.
	Produces map[string]string
}

// Result is one measured run.
type Result struct {
	Workload string
	Tool     string
	Version  string

	Runs      int
	MedianSec float64
	MinSec    float64
	MaxSec    float64
	PeakRSSKB int64
	BytesIn   int64
	OutputKB  int64

	Skipped string // why this cell is empty, if it is
}

// MBPerSecond is the throughput figure the specification's thresholds are
// stated in.
func (r Result) MBPerSecond() float64 {
	if r.MedianSec <= 0 || r.BytesIn == 0 {
		return 0
	}
	return float64(r.BytesIn) / (1 << 20) / r.MedianSec
}

// Config controls a comparison run.
type Config struct {
	CorpusDir string
	Runs      int
	Warmup    int
	Timeout   time.Duration
	OutputDir string

	// Tools overrides detection, which is how a locally built pglogwatch
	// takes part.
	Tools []Tool
}

func (c *Config) normalize() {
	if c.Runs <= 0 {
		c.Runs = 10 // TST-011
	}
	if c.Warmup < 0 {
		c.Warmup = 0
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Minute
	}
	if c.OutputDir == "" {
		c.OutputDir = os.TempDir()
	}
}

// Detect finds the tools that are installed and records their versions.
//
// A missing tool is not an error. The comparison still runs and the table says
// which cells could not be measured, which is more useful than refusing to
// produce anything -- and it is what makes the harness usable on a developer's
// machine as well as on the pinned runner.
func Detect() []Tool {
	out := make([]Tool, 0, len(Tools))
	for _, name := range Tools {
		t := Tool{Name: name, Binary: name}
		if path, err := exec.LookPath(name); err == nil {
			t.Binary, t.Found = path, true
			t.Version = versionOf(name, path)
		}
		out = append(out, t)
	}
	return out
}

// BuildSelf compiles the pglogwatch CLI from the checkout, so the harness
// measures the code in the working tree rather than whatever happens to be
// installed -- and works at all in a fresh clone.
//
// It returns the binary's path and a cleanup function.
func BuildSelf(ctx context.Context, moduleDir string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "pglogwatch-bench-bin")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	bin := filepath.Join(dir, "pglogwatch")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	cmd.Dir = moduleDir
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("building pglogwatch: %w: %s", err, out)
	}
	return bin, cleanup, nil
}

// WithBinary replaces a tool's binary, which is how a freshly built pglogwatch
// enters the comparison.
func WithBinary(tools []Tool, name, path, version string) []Tool {
	for i := range tools {
		if tools[i].Name == name {
			tools[i].Binary, tools[i].Found, tools[i].Version = path, true, version
		}
	}
	return tools
}

func versionOf(name, path string) string {
	var args []string
	switch name {
	case "pgbadger":
		args = []string{"--version"}
	default:
		args = []string{"--version"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// Workloads builds the §6.4 table for a corpus directory.
//
// The csvlog file is used for every workload, because it is the format all
// three tools support: pgbadger cannot read jsonlog at all, so comparing on it
// would mean comparing two tools and calling it three.
func Workloads(corpusDir string) []Workload {
	csvlog := filepath.Join(corpusDir, "postgresql-pg14.csv")
	devnull := os.DevNull

	return []Workload{{
		ID:   "W1",
		Name: "parse and discard",
		Args: map[string][]string{
			"pglogwatch": {"bench", csvlog},
			// pgbadger has no parse-only mode; -o /dev/null is the
			// closest, and it still builds the full in-memory report
			// before discarding it. Recorded, not hidden.
			"pgbadger": {"-j", "1", "-o", devnull, "-f", "csv", csvlog},
			"pgweasel": {"stats", csvlog},
		},
		Produces: map[string]string{
			"pglogwatch": "throughput summary",
			"pgbadger":   "full report, written to /dev/null",
			"pgweasel":   "statistics summary",
		},
	}, {
		ID:   "W2",
		Name: "severity histogram",
		Args: map[string][]string{
			"pglogwatch": {"stats", csvlog},
			"pgbadger":   {"-j", "1", "-o", devnull, "-f", "csv", csvlog},
			"pgweasel":   {"stats", csvlog},
		},
		Produces: map[string]string{
			"pglogwatch": "severity and event counts",
			"pgbadger":   "full report including an error section",
			"pgweasel":   "statistics summary",
		},
	}, {
		ID:   "W3",
		Name: "errors report",
		Args: map[string][]string{
			"pglogwatch": {"errors", csvlog},
			"pgbadger":   {"-j", "1", "-o", devnull, "-f", "csv", csvlog},
			"pgweasel":   {"errors", csvlog},
		},
		Produces: map[string]string{
			"pglogwatch": "severity histogram and top messages",
			"pgbadger":   "full report including an error section",
			"pgweasel":   "error report",
		},
	}, {
		ID:   "W4",
		Name: "top slow queries",
		Args: map[string][]string{
			"pglogwatch": {"slow", csvlog},
			"pgbadger":   {"-j", "1", "-o", devnull, "-f", "csv", csvlog},
			"pgweasel":   {"slow", csvlog},
		},
		Produces: map[string]string{
			"pglogwatch": "slowest and most-total-time statements",
			"pgbadger":   "full report including slowest queries",
			"pgweasel":   "slow query report",
		},
	}, {
		ID:   "W5",
		Name: "parallel, all corpus files",
		Args: map[string][]string{
			"pglogwatch": append([]string{"stats", "--jobs", "8"}, csvFiles(corpusDir)...),
			"pgbadger":   append([]string{"-J", "8", "-o", devnull, "-f", "csv"}, csvFiles(corpusDir)...),
			"pgweasel":   append([]string{"stats"}, csvFiles(corpusDir)...),
		},
		Produces: map[string]string{
			"pglogwatch": "severity and event counts, 8 workers",
			"pgbadger":   "full report, 8 jobs",
			"pgweasel":   "statistics summary; parallelism as supported",
		},
	}}
}

func csvFiles(dir string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, "*.csv"))
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}

// Run measures every workload against every installed tool.
func Run(ctx context.Context, cfg Config) ([]Result, []Tool, error) {
	cfg.normalize()
	tools := cfg.Tools
	if tools == nil {
		tools = Detect()
	}

	size, err := corpusBytes(cfg.CorpusDir)
	if err != nil {
		return nil, nil, err
	}

	var results []Result
	for _, w := range Workloads(cfg.CorpusDir) {
		for _, tool := range tools {
			args, ok := w.Args[tool.Name]
			if !ok {
				results = append(results, Result{
					Workload: w.ID, Tool: tool.Name,
					Skipped: "not applicable to this workload",
				})
				continue
			}
			if !tool.Found {
				results = append(results, Result{
					Workload: w.ID, Tool: tool.Name,
					Skipped: "not installed",
				})
				continue
			}
			r := measure(ctx, cfg, tool, w, args)
			r.BytesIn = workloadBytes(w, tool.Name, size)
			results = append(results, r)
		}
	}
	return results, tools, nil
}

// measure times one tool on one workload.
func measure(ctx context.Context, cfg Config, tool Tool, w Workload, args []string) Result {
	r := Result{Workload: w.ID, Tool: tool.Name, Version: tool.Version, Runs: cfg.Runs}

	for range cfg.Warmup {
		_ = runOnce(ctx, cfg, tool, args)
	}
	times := make([]float64, 0, cfg.Runs)
	for range cfg.Runs {
		d, rss, err := runOnceMeasured(ctx, cfg, tool, args)
		if err != nil {
			r.Skipped = "failed: " + err.Error()
			return r
		}
		times = append(times, d.Seconds())
		if rss > r.PeakRSSKB {
			r.PeakRSSKB = rss
		}
	}
	sort.Float64s(times)
	r.MinSec = times[0]
	r.MaxSec = times[len(times)-1]
	// TST-011 asks for the median, not the mean: one scheduling hiccup in
	// ten runs moves a mean and leaves a median alone.
	r.MedianSec = times[len(times)/2]
	return r
}

func runOnce(ctx context.Context, cfg Config, tool Tool, args []string) error {
	_, _, err := runOnceMeasured(ctx, cfg, tool, args)
	return err
}

func runOnceMeasured(ctx context.Context, cfg Config, tool Tool, args []string) (time.Duration, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, tool.Binary, args...) //nolint:gosec // binaries and args are from the table above
	out, err := os.CreateTemp(cfg.OutputDir, "pglogwatch-bench-*")
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(out.Name())
	}()
	cmd.Stdout = out
	cmd.Stderr = nil

	start := time.Now()
	err = cmd.Run()
	elapsed := time.Since(start)
	if err != nil {
		return 0, 0, err
	}
	return elapsed, peakRSSKB(cmd), nil
}

// peakRSSKB reports the child's peak resident set, where the platform can.
//
// Go exposes it through ProcessState.SysUsage on Unix and not at all on
// Windows, so a cell is reported as unmeasured rather than as zero -- a zero
// would read as "used no memory", which is worse than an admitted gap.
func peakRSSKB(cmd *exec.Cmd) int64 {
	if cmd.ProcessState == nil {
		return 0
	}
	return platformPeakRSSKB(cmd)
}

func corpusBytes(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading corpus %s: %w (run: make corpus)", dir, err)
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	if total == 0 {
		return 0, fmt.Errorf("corpus %s is empty (run: make corpus)", dir)
	}
	return total, nil
}

// workloadBytes is how many bytes a workload actually read, which is not the
// whole corpus for the single-file workloads.
func workloadBytes(w Workload, tool string, corpusSize int64) int64 {
	args := w.Args[tool]
	var total int64
	for _, a := range args {
		if info, err := os.Stat(a); err == nil && !info.IsDir() {
			total += info.Size()
		}
	}
	if total == 0 {
		return corpusSize
	}
	return total
}

// Environment describes where a measurement was taken, for TST-013 and TST-014.
type Environment struct {
	OS      string
	Arch    string
	CPUs    int
	Go      string
	Corpus  string
	Machine string
}

// DescribeEnvironment gathers what the results table has to cite.
func DescribeEnvironment(corpusVersion string) Environment {
	return Environment{
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		CPUs:    runtime.NumCPU(),
		Go:      runtime.Version(),
		Corpus:  corpusVersion,
		Machine: os.Getenv("PGLOGWATCH_BENCH_MACHINE_NAME"),
	}
}

func formatSeconds(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }
func formatMBps(v float64) string    { return strconv.FormatFloat(v, 'f', 1, 64) }
