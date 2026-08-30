package main

import (
	"context"
	"io"
	"os"
	"runtime"

	"github.com/cybertec-postgresql/pglogwatch"
)

// --jobs support.
//
// Only the reports whose aggregation is order-independent use it. stats,
// errors, slow, connections, locks and bench all reduce records into counters,
// so the order they arrive in cannot change the answer. parse, grep, peaks and
// system are ordered -- grep prints context around a match, parse's output is
// expected to follow the log -- and ParallelScan explicitly does not preserve
// order (IFC-008), so forcing them through it would produce shuffled output
// for no benefit a person could use.
//
// Each worker gets its own accumulator, indexed by worker number, so nothing
// needs a lock on the hot path. That is what makes the parallelism worth
// having: a mutex per record would serialise the aggregation and leave only
// the parsing parallel.

// workerCount resolves --jobs.
func (o *options) workerCount() int {
	if o.jobs > 0 {
		return o.jobs
	}
	return runtime.NumCPU()
}

// eachRecordByWorker runs fn over every record, possibly in parallel, passing
// the worker number so a caller can keep per-worker state.
//
// It falls back to a serial scan -- with worker 0 for everything -- whenever
// parallelism is not available: one job, standard input, or a compressed file.
// A compressed stream has no random access, so it cannot be sharded at all;
// reading it serially is correct and the alternative is refusing to read it.
func (o *options) eachRecordByWorker(fn func(worker int, r *pglogwatch.Record) error) (int, error) {
	workers := o.workerCount()
	if workers <= 1 {
		// Checked BEFORE opening anything: opening the files and then
		// taking the serial path leaks every handle, which on Windows
		// also makes the directory undeletable afterwards.
		return 1, o.eachRecord(func(r *pglogwatch.Record) error { return fn(0, r) })
	}
	files, ok := o.openSeekableFiles()
	if !ok {
		return 1, o.eachRecord(func(r *pglogwatch.Record) error { return fn(0, r) })
	}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	srcs := make([]io.ReaderAt, len(files))
	for i, f := range files {
		srcs[i] = f
	}
	err := pglogwatch.ParallelScan(context.Background(), srcs, o.cfg, workers,
		func(worker int, r *pglogwatch.Record) error {
			if !o.inRange(r) {
				return nil
			}
			return fn(worker, r)
		})
	return workers, err
}

// openSeekableFiles opens the inputs when every one of them is a plain,
// randomly accessible file. It reports false when any is not.
func (o *options) openSeekableFiles() ([]*os.File, bool) {
	if len(o.paths) == 0 {
		return nil, false // standard input cannot be sharded
	}
	files := make([]*os.File, 0, len(o.paths))
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for _, path := range o.paths {
		if isCompressedPath(path) {
			closeAll()
			return nil, false
		}
		f, err := os.Open(path) //nolint:gosec // the caller named the file
		if err != nil {
			closeAll()
			return nil, false
		}
		files = append(files, f)
	}
	return files, true
}

// isCompressedPath reports whether a path is compressed by its extension.
//
// The extension is enough here even though compress.Open sniffs content: this
// only decides whether to attempt sharding, and being wrong costs a serial
// scan rather than a wrong answer.
func isCompressedPath(path string) bool {
	for _, ext := range []string{".gz", ".zst", ".zstd", ".bz2", ".xz"} {
		if len(path) >= len(ext) && path[len(path)-len(ext):] == ext {
			return true
		}
	}
	return false
}
