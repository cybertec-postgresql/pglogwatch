package pglogwatch

import (
	"context"
	"io"
	"io/fs"
	"runtime"
	"sync"
)

// Parallel scanning (IFC-008).
//
// A Parser is single-goroutine by design (PERF-012) -- it hands out slices of
// one reusable buffer, which is what makes it allocation-free. ParallelScan
// therefore does not make a Parser concurrent; it gives each worker its own,
// over a different part of the input.
//
// The whole difficulty is at the shard boundaries. A record straddling one
// must be processed by exactly one worker, and both failures are silent: a
// lost record and a doubled record each produce counts that are nearly right.
//
// The rule that makes it exact needs no coordination between workers:
//
//	a worker owns every record whose FIRST BYTE falls in [start, end)
//
// Each worker seeks to its start, which resynchronises forward to the next
// record boundary, and stops at the first record beginning at or after its
// end. A record straddling the boundary begins before it, so the earlier
// worker owns it and finishes it; the later worker's resynchronisation skips
// past it because it started mid-record. Neither worker has to know what the
// other decided, which is what keeps the shards independent.

// sizedReaderAt is an io.ReaderAt whose length is known. bytes.Reader,
// strings.Reader and io.SectionReader all satisfy it.
type sizedReaderAt interface {
	io.ReaderAt
	Size() int64
}

// statReaderAt is an *os.File, whose length comes from a stat. Declared
// structurally, and against io/fs rather than os, because os.FileInfo is an
// alias for fs.FileInfo and this way the package does not depend on os to
// recognise a file.
type statReaderAt interface {
	io.ReaderAt
	Stat() (fs.FileInfo, error)
}

// shard is one worker's slice of one source.
type shard struct {
	src        io.ReaderAt
	size       int64
	start, end int64
}

// ParallelScan reads srcs with several goroutines, calling fn for every record.
//
// Each worker has its own [Parser], because a Parser is not safe for concurrent
// use (PERF-012). fn is called from all of them, so it must be safe to call
// concurrently; the *[Record] it receives belongs to the calling worker and
// follows the usual borrowing rules -- it is valid until fn returns.
//
// ORDERING IS NOT GUARANTEED. Records arrive in whatever order the workers
// reach them, which is the point: a caller that needs order should use a
// [Parser] directly, or sort what it collects.
//
// workers of zero or less means one per CPU. Returning a non-nil error from fn
// stops the scan and that error is returned; so does cancelling ctx.
func ParallelScan(ctx context.Context, srcs []io.ReaderAt, cfg Config, workers int,
	fn func(worker int, r *Record) error,
) error {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	shards := planShards(srcs, workers)
	if len(shards) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancel() // stop the other workers; their results are moot
		})
	}

	// Shards are handed out round-robin rather than in contiguous blocks, so
	// that a set of files of unequal size does not leave one worker with all
	// the large ones.
	for worker := range workers {
		wg.Go(func() {
			p := New(nil, cfg) // one parser per worker, reused across its shards
			for i := worker; i < len(shards); i += workers {
				if ctx.Err() != nil {
					return
				}
				if err := scanShard(ctx, p, shards[i], worker, fn); err != nil {
					fail(err)
					return
				}
			}
		})
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// scanShard parses one shard with one parser.
func scanShard(ctx context.Context, p *Parser, s shard, worker int,
	fn func(worker int, r *Record) error,
) error {
	// A section over the whole source, so Record.Offset stays a position in
	// the FILE and the parser can resynchronise using bytes before the
	// shard's start.
	p.Reset(io.NewSectionReader(s.src, 0, s.size))

	// Resolve the format -- and, for stderr, log_line_prefix -- from the
	// HEAD of the source, before seeking into the shard.
	//
	// Detection reads the first non-empty line, and at a shard's start that
	// line is almost always a fragment: a jsonlog file detects as stderr
	// because the fragment does not begin with a brace, and every shard but
	// the first then parses the whole file wrongly while reporting no
	// errors. Seek preserves detection state once it is resolved, so doing
	// it here costs one peek per shard and fixes it.
	p.ensureFormat()

	if _, err := p.Seek(s.start, io.SeekStart); err != nil {
		return err
	}

	// Checking cancellation once per record would put a context read in the
	// hot loop. Once per batch keeps the loop clean and still stops within
	// a few microseconds of a cancel.
	const cancelCheckInterval = 1024
	n := 0

	for p.Next() {
		r := p.Record()
		if s.end >= 0 && r.Offset >= s.end {
			// Owned by the next shard, which will start exactly here.
			return nil
		}
		if err := fn(worker, r); err != nil {
			return err
		}
		if n++; n%cancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil // ParallelScan reports ctx.Err itself
			}
		}
	}
	return p.Err()
}

// minShardBytes is the smallest slice worth giving a worker.
//
// Below this, the per-shard cost -- opening a section reader, resolving the
// format, resynchronising to a record boundary -- outweighs the parsing saved,
// and splitting a small file many ways mostly produces empty shards. A file
// under this size goes to one worker whole.
const minShardBytes = 64 << 10

// planShards divides the sources into work.
//
// A source whose size cannot be determined is not split: without a length
// there is nowhere to cut, and reading it whole in one worker is correct if
// not parallel. That is why it is a fallback rather than an error.
func planShards(srcs []io.ReaderAt, workers int) []shard {
	var shards []shard
	for _, src := range srcs {
		size, ok := sizeOf(src)
		if !ok {
			shards = append(shards, shard{src: src, size: unknownSize, start: 0, end: -1})
			continue
		}
		if size == 0 {
			continue
		}
		// Split the source into byte ranges. Each range is snapped to a
		// record boundary by the worker that reads it, so the cut
		// points here can be arbitrary.
		parts := int(size / minShardBytes)
		parts = min(max(parts, 1), workers)

		per := size / int64(parts)
		for i := range parts {
			start := int64(i) * per
			end := start + per
			if i == parts-1 {
				// The last shard runs to the end of the source, so
				// integer division leaving a remainder cannot
				// strand the final bytes.
				end = -1
			}
			shards = append(shards, shard{src: src, size: size, start: start, end: end})
		}
	}
	return shards
}

// unknownSize is the length used for a source that cannot report one.
// io.SectionReader treats it as "read until the reader stops".
const unknownSize = int64(1) << 62

func sizeOf(src io.ReaderAt) (int64, bool) {
	switch s := src.(type) {
	case sizedReaderAt:
		return s.Size(), true
	case statReaderAt:
		info, err := s.Stat()
		if err != nil {
			return 0, false
		}
		return info.Size(), true
	}
	return 0, false
}
