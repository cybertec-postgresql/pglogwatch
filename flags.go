package pglogwatch

// Flags describes cheap facts about a [Record] that the parser discovered
// while scanning it, and that the caller would otherwise have to rescan the
// record to learn.
type Flags uint16

// Record flags. Test them with Flags&FlagX != 0.
const (
	// FlagNeedsUnquote reports that at least one field of the record still
	// carries source-level escaping: a doubled quote in csvlog, or a
	// backslash escape in jsonlog. Unescaping is deferred so that the
	// parser never allocates on behalf of a caller who does not need it;
	// use [AppendUnquoted] into a buffer you own.
	FlagNeedsUnquote Flags = 1 << iota

	// FlagMultiline reports that the record spanned more than one physical
	// line: a newline inside a quoted csvlog field, or stderr continuation
	// lines folded into this record.
	FlagMultiline

	// FlagTruncated reports that the record is incomplete: it either hit
	// Config.MaxRecordBytes, or input ended in the middle of it.
	FlagTruncated

	// FlagHasDuration reports that Record.Duration was populated from a
	// "duration: N.NNN ms" message. Without it, a zero Duration is merely
	// absent rather than genuinely zero.
	FlagHasDuration

	// FlagHasStatement reports that Record.Statement was populated.
	FlagHasStatement
)
