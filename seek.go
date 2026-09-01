package pglogwatch

import "io"

// ErrNotSeekable reports that [Parser.Seek] was called on a stream that cannot
// seek -- a pipe, a network connection, or a decompressing reader.
//
// It is the reason an [OffsetStore] is worth persisting only for sources that
// can act on it: resuming at a byte offset needs a reader that can go there.
var ErrNotSeekable = &parseError{msg: "pglogwatch: the underlying reader does not support seeking"}

// maxResyncLines bounds how far Seek will look for a record boundary.
//
// Landing mid-record costs at most one skipped line. Needing many more means
// the offset was not a record boundary in this file at all -- a stale offset
// after a rotation, say -- and scanning on would burn through the file looking
// for something that is not there. Stopping lets the caller notice.
const maxResyncLines = 1024

// resyncPeekBytes is how much input resynchronisation looks at per step.
//
// It only needs one whole line. Peeking the detection window instead -- 256 KiB
// -- costs an extra read of that size for every shard ParallelScan creates,
// which on a 32 MiB corpus split 64 ways is more added reading than parsing.
const resyncPeekBytes = 8 << 10

// Seek repositions the parser at a byte offset and resynchronises to the next
// record boundary. It implements [io.Seeker].
//
// This is what makes resumption O(1). The implementation this module replaces
// resumed by calling ReadString in a loop as many times as it had previously
// read lines, which re-reads the whole file after a restart -- for a rotated
// multi-gigabyte log, every time (§7.6). Offsets come from [Record.Offset] and
// are what an [OffsetStore] persists.
//
// The offset is interpreted according to whence: [io.SeekStart] means relative
// to the start of the file, [io.SeekCurrent] relative to the current position,
// and [io.SeekEnd] relative to the end. Note that SeekCurrent counts from the
// parser's own position -- the offset the next [Parser.Next] would report --
// not from the position of the underlying reader, which the buffer has already
// read past.
//
// The offset need not be a record boundary. Seek discards the partial record it
// lands in and continues from the next one, so a caller may seek to an
// approximate position. Seeking to 0 needs no resynchronisation and does none.
//
// The returned offset is where the parser actually landed, which is the
// boundary resynchronisation found and so is at or after the requested
// position. It is the value to persist if the seek itself is a resumption
// point. Seeking past the end is not an error: it returns the requested offset
// and the next Next reports end of input.
//
// Detection state is kept: the format and log_line_prefix belong to the file,
// not to the position within it, and re-detecting from the middle of a file
// would be both wasteful and less reliable. Counters are reset, since they
// describe what this parser has read.
//
// It returns [ErrNotSeekable] if the underlying reader cannot seek.
func (p *Parser) Seek(offset int64, whence int) (int64, error) {
	seeker, ok := p.buf.src.(io.Seeker)
	if !ok {
		return 0, ErrNotSeekable
	}
	abs, err := p.resolveOffset(seeker, offset, whence)
	if err != nil {
		return 0, err
	}
	if _, err := seeker.Seek(abs, io.SeekStart); err != nil {
		return 0, err
	}

	src := p.buf.src
	p.buf.reset(src)
	p.buf.consumed = abs
	p.rec.reset()
	p.stats = Stats{}
	p.err = nil
	p.done = false
	p.pendingFlags = 0

	if abs == 0 {
		return 0, nil
	}
	// Resynchronisation asks "does this line start a record", which is a
	// per-format question -- so the format has to be resolved first. It is
	// not, when Seek is the first thing called on a parser, which is
	// exactly what ParallelScan does for every shard after the first.
	if !p.ensureFormat() {
		return p.buf.consumed, nil // nothing left to read
	}
	p.resync()
	return p.buf.consumed, nil
}

// resolveOffset turns an (offset, whence) pair into an absolute position.
//
// io.SeekCurrent resolves against the parser's consumed count rather than
// asking the underlying reader where it is. The two differ by however much the
// buffer has read ahead, which is up to a full buffer and is not a quantity the
// caller can predict, so delegating would make a relative seek land somewhere
// arbitrary.
func (p *Parser) resolveOffset(seeker io.Seeker, offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = p.buf.consumed
	case io.SeekEnd:
		end, err := seeker.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, err
		}
		base = end
	default:
		return 0, errBadWhence
	}
	abs := base + offset
	if abs < 0 {
		return 0, errNegativeOffset
	}
	return abs, nil
}

// resync advances to the next record boundary.
//
// It checks the CURRENT position first. An offset taken from Record.Offset is
// already a boundary, and discarding a line unconditionally -- the obvious
// implementation -- would silently drop the first record after every resume.
// That is the failure this function exists to avoid, and it is invisible: the
// scan looks healthy and is simply missing one record per restart.
//
// Otherwise lines are skipped until one starts a record. Skipping to the next
// NEWLINE is not enough on its own: landing inside a multi-line record leaves
// the remaining lines of that record ahead, and they are not records.
func (p *Parser) resync() {
	for range maxResyncLines {
		sample := p.buf.peek(resyncPeekBytes)
		line, ok := firstNonEmptyLine(sample)
		if !ok {
			return // end of input; nothing to resynchronise to
		}
		if p.isRecordStart(line) {
			return
		}
		if !p.buf.discardToNewline() {
			return
		}
	}
}

// isRecordStart reports whether a line begins a record in the resolved format.
func (p *Parser) isRecordStart(line []byte) bool {
	switch p.format {
	case FormatCSV:
		return looksLikeCSVLine(line)
	case FormatJSON:
		return len(line) > 0 && line[0] == '{'
	default:
		// stderr: a line starts a record when it carries the prefix and
		// a label that is not a continuation. That is the same question
		// the framer asks, so ask it the same way rather than writing a
		// second answer that can disagree.
		return !p.isContinuationLine(line)
	}
}
