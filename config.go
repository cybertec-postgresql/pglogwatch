package pglogwatch

// Default values applied by [Config] to any field left at its zero value.
const (
	defaultDetectLines        = 200
	defaultMessagesLang       = "en"
	defaultMaxRecordBytes     = 16 << 20
	defaultInitialBufferBytes = 64 << 10

	// The buffer must be able to hold a whole record, so a MaxRecordBytes
	// below this cannot parse even a trivial one and is treated as a
	// mistake rather than as an intent to reject everything.
	minMaxRecordBytes = 512
)

// Config configures a [Parser]. Its zero value is valid and selects every
// default: auto-detect the format and, for stderr, auto-detect
// log_line_prefix; assume English severities; emit a truncated final record;
// and scan messages for a statement duration.
type Config struct {
	// Format selects the log destination to parse. The zero value,
	// FormatAuto, detects it from the first non-empty line.
	Format Format

	// LinePrefix is the server's log_line_prefix, and applies only to the
	// stderr format. Empty means auto-detect it from the first DetectLines
	// lines; see [Parser.DetectedPrefix].
	LinePrefix string

	// DetectLines bounds how many leading lines log_line_prefix detection
	// may inspect. Zero means 200.
	DetectLines int

	// MessagesLang is the two-letter lc_messages prefix of the server that
	// wrote the log ("de", "ru", ...), used to normalise localised
	// severities to English. Zero means "en". An unrecognised value is not
	// an error: severities are then passed through unchanged.
	MessagesLang string

	// MaxRecordBytes caps how large a single record may become. Zero means
	// 16 MiB. A record that exceeds the cap is skipped and counted in
	// Stats.Truncated rather than growing the buffer without bound.
	MaxRecordBytes int

	// InitialBufferBytes is the read buffer's starting capacity. Zero means
	// 64 KiB. The buffer doubles as needed up to MaxRecordBytes; sizing it
	// correctly up front only saves the first few growth steps.
	InitialBufferBytes int

	// SplitContinuations makes stderr DETAIL/HINT/STATEMENT/QUERY/CONTEXT
	// lines their own records instead of folding them into the record they
	// belong to. Off by default, because folding is what callers counting
	// events almost always want.
	SplitContinuations bool

	// NoTruncatedTail discards a final record that ended without a newline
	// instead of emitting it with FlagTruncated set.
	//
	// The specification names this option EmitTruncatedTail, defaulting to
	// true. It is inverted here so that the documented default survives a
	// partially-specified Config: with a positive-polarity bool, writing
	// Config{Format: FormatStderr} would silently disable a feature the
	// specification says is on by default. This matches the convention of
	// net/http.Transport.DisableKeepAlives.
	NoTruncatedTail bool

	// NoDuration skips scanning messages for "duration: N.NNN ms", leaving
	// Record.Duration zero and FlagHasDuration clear.
	//
	// Inverted from the specification's ParseDuration for the same reason
	// as NoTruncatedTail.
	NoDuration bool

	// OnMalformed, if non-nil, is called for each line the parser could not
	// interpret. The line is a borrowed slice with the same lifetime as a
	// Record's fields: it is invalid once the callback returns.
	//
	// A malformed line is never fatal. It is counted in Stats.Malformed and
	// parsing continues with the next line (FMT-010).
	OnMalformed func(line []byte, err error)
}

// normalize replaces zero-valued fields with their defaults and clamps values
// that would leave the parser unable to make progress. It is called once by
// [New] and [Parser.Reset], never on the hot path.
func (c *Config) normalize() {
	if c.DetectLines <= 0 {
		c.DetectLines = defaultDetectLines
	}
	if c.MessagesLang == "" {
		c.MessagesLang = defaultMessagesLang
	}
	if c.MaxRecordBytes <= 0 {
		c.MaxRecordBytes = defaultMaxRecordBytes
	}
	if c.MaxRecordBytes < minMaxRecordBytes {
		c.MaxRecordBytes = minMaxRecordBytes
	}
	if c.InitialBufferBytes <= 0 {
		c.InitialBufferBytes = defaultInitialBufferBytes
	}
	// Starting larger than the cap would make the very first read look like
	// an over-large record.
	if c.InitialBufferBytes > c.MaxRecordBytes {
		c.InitialBufferBytes = c.MaxRecordBytes
	}
}

// emitTruncatedTail reports the specification's EmitTruncatedTail setting.
func (c *Config) emitTruncatedTail() bool { return !c.NoTruncatedTail }

// parseDuration reports the specification's ParseDuration setting.
func (c *Config) parseDuration() bool { return !c.NoDuration }
