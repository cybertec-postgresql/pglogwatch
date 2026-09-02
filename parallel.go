package pglogwatch

import (
	"context"
	"io"
	"io/fs"
	"runtime"
	"sync"
	"sync/atomic"
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
	shards := planShards(srcs)
	if len(shards) == 0 {
		return nil
	}
	// A worker that could never draw a shard is a goroutine and a 64 KiB
	// buffer spent on nothing.
	workers = min(workers, len(shards))

	// A log_line_prefix the caller got wrong is a configuration error, and
	// compiling it once up here is what lets ParallelScan report it.
	// Detecting it inside a worker cannot: scanShard's Reset clears the err
	// New set, and the scan then silently auto-detects instead.
	tpl, err := workerPrefix(cfg)
	if err != nil {
		return err
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

	// Shards are drawn from a shared cursor rather than dealt out in advance.
	// They are near-equal in BYTES by construction, but equal bytes are not
	// equal time -- a shard dense in continuation lines costs more per byte
	// than one of short records -- and wg.Wait means the slowest worker sets
	// the wall clock. Drawing on demand bounds the tail at one shard instead
	// of at the worst accumulation over a worker's whole share. The cursor is
	// touched once per shard, not once per record.
	var next atomic.Int64
	for worker := range workers {
		wg.Go(func() {
			// Built HERE, on the worker's own goroutine, and deliberately
			// so. Its read buffer is the hottest memory in the scan, and
			// on a NUMA machine the page lands on the node that first
			// touches it. Allocating all of them together on the parent --
			// one slab, one allocation, which is otherwise the tidier
			// shape -- puts every worker's buffer on one node and makes
			// the scan 1.8x slower on a two-node part.
			p := newParser(nil, cfg, tpl)
			var rd shardReader
			for {
				i := int(next.Add(1)) - 1
				if i >= len(shards) || ctx.Err() != nil {
					return
				}
				if err := scanShard(ctx, p, &rd, shards[i], worker, fn); err != nil {
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

// workerPrefix compiles Config.LinePrefix once for every worker to share.
//
// Sharing the template is safe for the same reason compiledCandidates is:
// scanPrefix reads it and writes only into the caller's Record and tzCache,
// both of which are per-parser. Compiling it here rather than in each worker
// also means a prefix the caller got wrong is reported, instead of every
// worker quietly falling back to detection.
func workerPrefix(cfg Config) (*prefixTemplate, error) {
	if cfg.LinePrefix == "" {
		return nil, nil
	}
	return compilePrefix(cfg.LinePrefix)
}

// shardReader is io.SectionReader without an allocation per shard.
//
// A parser needs only Read and Seek from its source (see Parser.Seek), never
// ReadAt or Size, so one of these is re-pointed at each shard in turn rather
// than a fresh SectionReader being built for every one.
type shardReader struct {
	src  io.ReaderAt
	size int64
	off  int64 // where the next Read starts
}

func (r *shardReader) reset(src io.ReaderAt, size int64) {
	r.src, r.size, r.off = src, size, 0
}

func (r *shardReader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	if over := r.size - r.off; int64(len(p)) > over {
		p = p[:over]
	}
	n, err := r.src.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}

func (r *shardReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.off + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, errBadWhence
	}
	if abs < 0 {
		return 0, errNegativeOffset
	}
	r.off = abs
	return abs, nil
}

// scanShard parses one shard with one parser.
func scanShard(ctx context.Context, p *Parser, rd *shardReader, s shard, worker int,
	fn func(worker int, r *Record) error,
) error {
	// The reader spans the whole source, not just the shard, so Record.Offset
	// stays a position in the FILE and the parser can resynchronise using
	// bytes before the shard's start.
	rd.reset(s.src, s.size)
	p.Reset(rd)

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
// Below this, the per-shard cost -- re-pointing the reader, resynchronising to
// a record boundary -- outweighs the parsing saved, and splitting a small file
// many ways mostly produces empty shards. A file under this size goes to one
// worker whole.
const minShardBytes = 64 << 10

// targetShardBytes is how much input one shard aims to cover.
//
// It is a property of the INPUT, not of the worker count, and that is the
// whole point. planShards used to clamp the parts per source to the number of
// workers, so the same eight 4 MiB files became 8 shards for --jobs 1 and 64
// for --jobs 8: the parallel side paid eight times the per-shard prologue, and
// the ratio between the two was not a measure of parallelism at all. The two
// must divide the corpus identically and differ only in how many goroutines
// consume it (AC-019, issue #3).
//
// The size is set by the tail rather than by the prologue. wg.Wait means wall
// time is the slowest worker, so one shard is the smallest imbalance the plan
// can have: at 256 KiB an 8-worker run of the AC-019 corpus gets sixteen
// shards apiece and a tail under a tenth of the work. A megabyte would be a
// quarter of it.
const targetShardBytes = 256 << 10

// maxShardsPerSource bounds the plan for a very large source. At 256 KiB a
// 10 GB file would otherwise plan forty thousand shards, and the plan is
// itself memory that PERF-026 counts.
const maxShardsPerSource = 8192

// planShards divides the sources into work.
//
// It deliberately takes no worker count: two calls over the same sources must
// produce the same plan whatever --jobs says, or the sides of the AC-019 ratio
// are not doing equal work.
//
// A source whose size cannot be determined is not split: without a length
// there is nowhere to cut, and reading it whole in one worker is correct if
// not parallel. That is why it is a fallback rather than an error.
func planShards(srcs []io.ReaderAt) []shard {
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
		//
		// Round up, so that the last shard is the short one rather than
		// every shard being oversized; then refuse to cut below
		// minShardBytes, which is what keeps a small file whole.
		parts := int((size + targetShardBytes - 1) / targetShardBytes)
		parts = min(parts, int(size/minShardBytes), maxShardsPerSource)
		parts = max(parts, 1)

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
