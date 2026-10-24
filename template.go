package pglogwatch

import (
	"bytes"
	"crypto/sha256"
	"strconv"
	"unicode/utf8"
)

// TemplateRulesVersion changes whenever a rule change can change a template.
//
// It is part of the input to [PatternID], so an upgrade that splits or merges
// templates gives new pattern ids instead of putting two definitions under
// one id. A consumer that stores ids can compare this value to tell old ids
// from new ones.
const TemplateRulesVersion = 1

// templateLimit is the most bytes a template keeps before its ellipsis
// (REQ-011). The ellipsis is not counted, so a cut template is at most
// templateLimit plus len(ellipsis) bytes long.
const templateLimit = 1024

// ellipsis ends a template that was cut (REQ-011) or that stops at a plan
// marker (REQ-012).
const ellipsis = "\u2026"

// AppendTemplate appends the masked template of r.Message to dst and returns
// the extended slice.
//
// The template is the message with every value (numbers, quoted literals,
// key=value values, ids, paths) replaced by $1, $2, and so on, while words
// and identifiers stay. Two records that differ only by values give the same
// template, which is what lets a consumer group and count log events.
//
// Every run of white space becomes one space, and a run at the start or the
// end is dropped. After a statement marker such as "statement:" or
// "execute S_1:" the rest of the message is SQL: its literals are masked, its
// comments removed, and its keywords and identifiers kept as written.
//
// When "plan:" follows a duration, as in auto_explain output, the template
// ends with "plan: …" and the plan text is not read (REQ-012). A template
// longer than 1,024 bytes keeps its first 1,024 bytes or fewer and ends with
// "…" (REQ-011). The 1,024 bytes do not count the ellipsis, so a template is
// at most 1,027 bytes. The cut falls on a UTF-8 boundary, never inside a
// placeholder, and drops white space and punctuation before the ellipsis;
// after a placeholder, " …" ends the template.
// Masking stops once the template passes the limit, so the rest of a long
// message is not read; long statements that share their first kilobyte
// share a template. The one exception is an IN or VALUES list of only
// literals that starts within the limit: it is read to its end, since it
// may still collapse to "(...)".
//
// r.Message is borrowed from the parser and becomes invalid on the next call
// to [Parser.Next]. The returned template is written into dst and never
// aliases r.Message, so it stays valid after that call. AppendTemplate
// performs no heap allocation when dst has enough capacity; a caller that
// reuses one buffer, for example dst[:0], pays for the allocation once.
// AppendTemplate never needs more than 1,100 bytes of spare capacity in dst,
// whatever the length of the message.
//
// Only r.Message is read for the template; r.SQLState decides how
// double-quoted tokens are treated. Other fields are ignored.
func AppendTemplate(dst []byte, r *Record) []byte {
	msg := r.Message
	z := tokenizer{s: msg}
	var p placeholders
	quotedData := isDataClass(r.SQLState)
	prev := tokSpace // kind of the token before t
	idNext := false  // the last word, before white space, is followed by an id
	var after forState
	// fuse is the start in dst of the run of copied words and '$' bytes that
	// dst ends with, and -1 when it ends with anything else. A placeholder
	// written right after that run would read back as part of a word ("a$1")
	// or as a dollar quote ("$$1"), so the placeholder replaces the run
	// (GUD-001).
	fuse := -1
	// glued is true while dst ends with a placeholder. A copied word that
	// starts with a digit would extend its number ("$1" and "0abc" read back
	// as "$10" and "abc"), so the placeholder takes that word too.
	glued := false
	// space is true when white space or a comment came after the last token
	// written. One space stands for the whole run and is written only before
	// the next token, so a run at the start or the end of the message is
	// dropped (REQ-009).
	space := false
	base := len(dst)
	// markerOK is true at the start of the message and right after a masked
	// number such as the duration, the two places where PostgreSQL writes a
	// statement marker.
	markerOK := true
	var list listState
	// plan is true when the message stopped at a plan marker (REQ-012).
	plan := false
	// collapse replaces the list that starts at list.start in dst with
	// "(...)" and gives back the placeholder numbers it used, so the
	// numbering after the list stays contiguous, as in pg_stat_statements.
	collapse := func() {
		dst = append(dst[:list.start], "(...)"...)
		p.n = list.n
		fuse, glued = -1, false
	}
	mask := func() {
		if fuse >= 0 {
			dst = dst[:fuse]
		}
		dst = p.appendNext(dst)
		fuse, glued = -1, true
	}
	flush := func() {
		if space {
			dst = append(dst, ' ')
			fuse, glued, space = -1, false, false
		}
	}
scan:
	for {
		if len(dst)-base > templateLimit {
			// REQ-011: the template is cut below, so stop reading, unless a
			// list that may collapse began within the limit. The bytes past
			// the limit are thrown away while such a list is read, so dst does
			// not grow with it.
			if list.step < listElem || list.start-base > templateLimit {
				break
			}
			dst = dst[:base+templateLimit+1]
		}
		if end, ok := z.pathAt(z.pos); ok {
			// REQ-007: a bare path is a value up to where pathLen ends it.
			flush()
			mask()
			z.pos = end
			prev, idNext, after, markerOK = tokParam, false, forNone, false
			continue
		}
		t, ok := z.next()
		if !ok {
			if list.step == listClosed {
				collapse()
			}
			break
		}
		tb := msg[t.start:t.end]
		if t.kind == tokSpace || t.kind == tokComment {
			space = len(dst) > base
			prev = tokSpace
			continue
		}
		var act listAction
		if z.stmt {
			act = list.next(t.kind, tb)
		}
		switch act {
		case listCollapse:
			collapse()
		case listCollapseClose:
			// The ')' ends an IN list; the space before it goes with it.
			collapse()
			space, prev, markerOK = false, tokPunct, false
			continue
		}
		flush()
		if act == listOpen {
			list.start, list.n = len(dst), p.n
		}
		if !z.stmt && markerOK && t.kind == tokWord {
			if len(dst) > base && string(tb) == "plan" && colonEnds(msg, t.end) {
				// REQ-012: auto_explain writes the plan after a duration and
				// "plan:". The template ends there and the plan is not read.
				dst = append(dst, "plan: "...)
				plan = true
				break scan
			}
			if head, colon, ok := stmtMarker(msg, t.start); ok {
				// REQ-009: keep the marker, mask the prepared statement name and
				// read the rest of the message as a statement.
				dst, fuse = append(dst, msg[t.start:head]...), -1
				if colon > head {
					mask()
				}
				dst, fuse, glued = append(dst, ':'), -1, false
				z.pos, z.stmt = colon+1, true
				prev = tokPunct
				continue
			}
		}
		switch {
		case !z.stmt && t.kind == tokPunct && tb[0] == '=' && prev == tokWord:
			// REQ-004: the word before '=' is a key and stays; everything up
			// to the end of the value becomes one placeholder. An empty value
			// gets one too, so "application_name=" and "application_name=psql"
			// give the same template. In a statement '=' is an operator.
			dst, fuse = append(dst, '='), -1
			mask()
			z.skipValue()
			prev, idNext, after, markerOK = tokParam, false, forNone, false
			continue
		case masksLiteral(t.kind), !z.stmt && t.kind == tokWord && idNext && hasDigit(tb):
			// REQ-006: a word with a digit after an id word, such as the
			// session id in "session 5f3c1a2b.4d2", is an id. Numeric ids
			// and the LSN-shaped virtual transaction id "3/1234" are already
			// literals. In a statement a word is a keyword or an identifier.
			mask()
		case !z.stmt && t.kind == tokIdent && (quotedData || after == forData || isQuotedPath(tb)):
			// REQ-005 and REQ-007: a double-quoted token is an identifier
			// and stays, unless it holds data or a path. In a statement it is
			// always an identifier, whatever the SQLSTATE class, so it stays
			// (REQ-009).
			mask()
		case glued && t.kind == tokWord && isDigit(tb[0]):
			// The placeholder before this word takes it; see glued.
		default:
			if t.kind != tokWord && (t.kind != tokPunct || tb[0] != '$') {
				fuse = -1
			} else if fuse < 0 {
				fuse = len(dst)
			}
			// A token longer than the room left before the cut is copied
			// only up to the cut, which keeps dst small for a huge word.
			out := tb
			if room := base + templateLimit + 1 - len(dst); len(out) > room {
				out = out[:max(room, 0)]
			}
			dst = append(dst, out...)
			glued = false
		}
		if t.kind == tokWord {
			idNext = isIDWord(tb)
		} else {
			idNext = false
		}
		after = after.next(t.kind, tb)
		markerOK = t.kind == tokNumber || t.kind == tokParam
		prev = t.kind
	}
	return cutTemplate(dst, base, plan)
}

// cutTemplate ends the template dst[base:] (REQ-011, REQ-012). A template
// of at most templateLimit bytes is returned as it is, with "…" added when
// it stopped at a plan marker. A trailing "…" is not counted, so a template
// that was already cut is a fixed point (CON-002).
//
// A longer template is cut at the last UTF-8 boundary at or before
// templateLimit bytes. A placeholder that the cut would split is dropped
// whole, so "$12" never reads back as "$1". White space and punctuation
// before the cut are dropped too. The ellipsis then joins the last word and
// reads back as part of it, so a second pass changes nothing; a lone "…"
// after a "/" would read back as a path, and after a closed VALUES tuple it
// would collapse the list. After a placeholder the ellipsis follows a
// space, since "user=$1…" would read back as one key=value value.
func cutTemplate(dst []byte, base int, plan bool) []byte {
	n := len(dst) - base
	if bytes.HasSuffix(dst[base:], []byte(ellipsis)) {
		n -= len(ellipsis)
	}
	if n <= templateLimit {
		if plan {
			dst = append(dst, ellipsis...)
		}
		return dst
	}
	cut := base + templateLimit
	for cut > base && !utf8.RuneStart(dst[cut]) {
		cut--
	}
	if isDigit(dst[cut]) {
		if j := placeholderBefore(dst, base, cut, true); j >= 0 {
			cut = j
		}
	}
	cut = trimNonWord(dst, base, cut)
	if j := placeholderBefore(dst, base, cut, false); j >= 0 {
		if cut-base == templateLimit {
			// No room for the space: drop the placeholder instead.
			cut = trimNonWord(dst, base, j)
		}
		if placeholderBefore(dst, base, cut, false) >= 0 {
			dst = append(dst[:cut], ' ')
			return append(dst, ellipsis...)
		}
	}
	return append(dst[:cut], ellipsis...)
}

// placeholderBefore returns the index of the '$' of the placeholder whose
// digits end at dst[i-1], or -1 when there is none. With split set, the
// placeholder may also have no digits before i, so a cut right after its
// '$' finds it too.
func placeholderBefore(dst []byte, base, i int, split bool) int {
	j := i
	for j > base && isDigit(dst[j-1]) {
		j--
	}
	if (j < i || split) && j > base && dst[j-1] == '$' {
		return j - 1
	}
	return -1
}

// trimNonWord returns i moved back over the bytes of dst[base:i] that are
// not word bytes.
func trimNonWord(dst []byte, base, i int) int {
	for i > base && !isWordByte(dst[i-1]) {
		i--
	}
	return i
}

// listState finds an IN or VALUES list in a statement whose elements are
// all literals, which REQ-009 collapses to "(...)". It reads one token at a
// time, white space and comments left out. An IN list is one parenthesised
// list; a VALUES list is one or more of them separated by ','. An element
// is one literal token: a nested '(', a cast, an operator or a word makes
// the list stay as written. "NOT IN (1, 2)" collapses too, since the IN
// keyword starts the list. "= ANY(ARRAY[1, 2])" does not.
//
// A VALUES list is collapsed only at the first token after its last ')',
// so a later tuple that does not qualify keeps the whole list as written.
type listState struct {
	step   listStep
	values bool  // the list follows VALUES, so more tuples may follow
	start  int   // where the list's '(' is in dst
	n      int64 // the placeholder count before the list
}

type listStep uint8

const (
	listNone    listStep = iota
	listKeyword          // IN or VALUES seen, waiting for '('
	listElem             // '(' or ',' seen, waiting for a literal
	listSep              // a literal seen, waiting for ',' or ')'
	listClosed           // a VALUES tuple closed, waiting for ',' or the end
	listNext             // ',' after a VALUES tuple, waiting for '('
)

// listAction tells AppendTemplate what to do with the token passed to next.
type listAction uint8

const (
	listKeep          listAction = iota // write the token as usual
	listOpen                            // the token is the list's '(', record where it goes
	listCollapse                        // collapse the list, then write the token as usual
	listCollapseClose                   // the token is the ')' that ends an IN list; collapse and drop it
)

// next moves the state past a token of kind k with text b, which is not
// white space or a comment, and returns what to do with the token.
func (l *listState) next(k tokKind, b []byte) listAction {
	punct := func(c byte) bool { return k == tokPunct && b[0] == c }
	act := listKeep
	switch l.step {
	case listKeyword, listNext:
		if punct('(') {
			open := l.step == listKeyword
			l.step = listElem
			if open {
				return listOpen
			}
			return listKeep
		}
	case listElem:
		if masksLiteral(k) {
			l.step = listSep
			return listKeep
		}
	case listSep:
		switch {
		case punct(','):
			l.step = listElem
			return listKeep
		case punct(')') && l.values:
			l.step = listClosed
			return listKeep
		case punct(')'):
			l.step = listNone
			return listCollapseClose
		}
	case listClosed:
		if punct(',') {
			l.step = listNext
			return listKeep
		}
		act = listCollapse
	}
	l.step = listNone
	if k == tokWord {
		if equalFoldASCII(b, "in") {
			l.step, l.values = listKeyword, false
		} else if equalFoldASCII(b, "values") {
			l.step, l.values = listKeyword, true
		}
	}
	return act
}

// stmtMarker reports whether a statement marker starts at i (REQ-009), and
// returns the end of its words and the index of its ':'. The markers are
// "statement:", "fastpath function call:", and "execute", "parse" or "bind"
// with a prepared statement name before the ':'; "execute fetch from" names
// a statement too. The ':' must end the input or come before white space.
// When the marker has a name, it is s[head:colon], and s[i:head] ends with
// the space before it; otherwise head equals colon.
//
// The name is masked, "<unnamed>" included. A driver such as pgJDBC runs a
// query as the unnamed statement a few times before it prepares it as S_n,
// so masking the name keeps both in one template; the marker word still
// tells execute, parse and bind apart. A name that holds white space or ':'
// is not recognised.
func stmtMarker(s []byte, i int) (head, colon int, ok bool) {
	rest := s[i:]
	for _, m := range [...]string{"statement", "fastpath function call"} {
		if hasPrefix(rest, m) && colonEnds(s, i+len(m)) {
			return i + len(m), i + len(m), true
		}
	}
	head = -1
	for _, m := range [...]string{"execute fetch from ", "execute ", "parse ", "bind "} {
		if hasPrefix(rest, m) {
			head = i + len(m)
			break
		}
	}
	if head < 0 {
		return 0, 0, false
	}
	j := head
	for j < len(s) && s[j] != ':' && !isTemplateSpace(s[j]) {
		j++
	}
	if j == head || !colonEnds(s, j) {
		return 0, 0, false
	}
	return head, j, true
}

// colonEnds reports whether s[j] is a ':' at the end of s or before white
// space.
func colonEnds(s []byte, j int) bool {
	return j < len(s) && s[j] == ':' && (j+1 == len(s) || isTemplateSpace(s[j+1]))
}

// isDataClass reports whether a SQLSTATE is in class 22 (data exception) or
// 23 (integrity constraint violation). In those classes PostgreSQL puts data
// in double quotes, so every double-quoted token is masked (REQ-005). The
// constraint name of a 23505 goes with it, which GUD-001 accepts.
func isDataClass(code [5]byte) bool {
	return code[0] == '2' && (code[1] == '2' || code[1] == '3')
}

// forState tracks the phrase that introduces a data value in any SQLSTATE
// class (REQ-005): the word "for", then only words, white space and
// double-quoted tokens, then ':'. The first double-quoted token after that
// ':' is data. This covers "invalid input syntax for type integer: "abc"",
// the older "invalid input syntax for integer: "abc"" and, by GUD-001,
// "invalid input value for enum mood: "x"". Any other token on the way
// drops the state, so "for user "bob"" keeps the role.
type forState uint8

const (
	forNone  forState = iota
	forWords          // "for" seen, waiting for ':'
	forData           // ':' seen, the next double-quoted token is data
)

// next returns the state after a token of kind k with text b.
func (f forState) next(k tokKind, b []byte) forState {
	switch k {
	case tokSpace:
		return f
	case tokWord:
		if equalFoldASCII(b, "for") {
			return forWords
		}
		if f == forWords {
			return f
		}
	case tokIdent:
		if f == forWords {
			return f
		}
	case tokPunct:
		if f == forWords && b[0] == ':' {
			return forData
		}
	}
	return forNone
}

// pathAt reports whether a file path starts at i, and where it ends
// (REQ-007). It applies only in message mode, so "a /b" in a statement stays
// a division. The byte before i must not be a word byte, '/', '.', a closing
// bracket, '\' or '$', so "a/b", "(x)/y" and the '/' inside a token are
// not paths. An LSN never reaches here: the tokenizer takes "0/16B3748" as
// one token.
func (z *tokenizer) pathAt(i int) (int, bool) {
	s := z.s
	if z.stmt || i >= len(s) {
		return 0, false
	}
	if i > 0 {
		switch c := s[i-1]; {
		case isWordByte(c), c == '/', c == '.', c == ')', c == ']', c == '\\', c == '$':
			return 0, false
		}
	}
	if !pathPrefix(s[i:]) {
		return 0, false
	}
	return i + pathLen(s[i:]), true
}

// pathPrefix reports whether s starts with a file path: "/" before a word
// byte or '.', "./", "../", "base/", "global/", "pg_<word>/", or a drive
// letter with ":\" or ":/". PostgreSQL on Windows writes both separators.
// "global/" is not in REQ-007, but global/pg_control is a data directory
// path like base/, so GUD-001 masks it too.
func pathPrefix(s []byte) bool {
	n := len(s)
	if n == 0 {
		return false
	}
	switch {
	case s[0] == '/':
		return n > 1 && (isWordByte(s[1]) || s[1] == '.')
	case bytes.HasPrefix(s, []byte("./")), bytes.HasPrefix(s, []byte("../")):
		return true
	case n >= 3 && s[0]|0x20 >= 'a' && s[0]|0x20 <= 'z' && s[1] == ':' && (s[2] == '\\' || s[2] == '/'):
		return true
	}
	j := 0
	for j < n && isWordByte(s[j]) {
		j++
	}
	if j == 0 || j >= n || s[j] != '/' {
		return false
	}
	w := s[:j]
	return string(w) == "base" || string(w) == "global" || (j > 3 && string(w[:3]) == "pg_")
}

// pathLen returns the length of the bare path at the start of s. The path
// ends at white space or a quote, or at ',', ';', ':', ')' or '.' when that
// byte ends the input or comes before white space, so the punctuation of the
// sentence around the path stays. A bare path with a space in it, such as
// C:/Program Files/..., is cut at the space; quoted paths have no such limit.
func pathLen(s []byte) int {
	for i, c := range s {
		switch {
		case isTemplateSpace(c), c == '"', c == '\'':
			return i
		case c == ',', c == ';', c == ':', c == ')', c == '.':
			if i+1 == len(s) || isTemplateSpace(s[i+1]) {
				return i
			}
		}
	}
	return len(s)
}

// isQuotedPath reports whether the double-quoted token b, "x" or U&"x",
// holds a file path (REQ-007), as in path "base/pgsql_tmp/pgsql_tmp1234.0".
func isQuotedPath(b []byte) bool {
	if b[0] != '"' {
		b = b[2:] // U&
	}
	return pathPrefix(b[1:])
}

// skipValue moves pos past the value of a key=value pair that starts at pos.
// The value ends at white space, ',', ';', ')' or the end of the input. It
// is made of whole tokens, so a quoted value with spaces in it and a number
// with a spaced unit ("write=12.345 s,") are one value, and "SECRET-2" is
// skipped whole rather than split at the '-'.
func (z *tokenizer) skipValue() {
	for {
		start := z.pos
		t, ok := z.next()
		if !ok {
			return
		}
		if t.kind == tokSpace || t.kind == tokPunct && endsValue(z.s[t.start]) {
			z.pos = start
			return
		}
	}
}

func endsValue(c byte) bool {
	return c == ',' || c == ';' || c == ')'
}

// idWords are the words PostgreSQL writes before a process, transaction,
// virtual transaction, object or session id (REQ-006). They match without
// regard to ASCII case, so "PID", "pid" and "Process" all count.
var idWords = [...]string{
	"process", "pid", "backend", "worker",
	"transaction", "subtransaction", "xid", "multixact",
	"relation", "database", "tablespace", "role", "oid",
	"session",
}

func isIDWord(w []byte) bool {
	for _, s := range idWords {
		if equalFoldASCII(w, s) {
			return true
		}
	}
	return false
}

// equalFoldASCII reports whether b equals s, which is lower case, ignoring
// ASCII case.
func equalFoldASCII(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != s[i] {
			return false
		}
	}
	return true
}

func hasDigit(b []byte) bool {
	for _, c := range b {
		if isDigit(c) {
			return true
		}
	}
	return false
}

// masksLiteral reports whether a token of kind k is a literal value that
// always becomes a placeholder: a standalone number with its unit (REQ-001),
// a hex or LSN token (REQ-002), or a single- or dollar-quoted literal
// (REQ-003). An existing $n placeholder is written afresh, which renumbers
// it and makes masking idempotent (CON-002). A number inside a word is a
// tokWord and is not masked.
func masksLiteral(k tokKind) bool {
	switch k {
	case tokNumber, tokHex, tokString, tokDollar, tokParam:
		return true
	}
	return false
}

// PatternID returns the pattern identity of a template made by
// [AppendTemplate] for r.
//
// The id is the first 8 bytes of SHA-256 over the severity name, a 0x00
// byte, the five SQLSTATE bytes, a 0x00 byte, [TemplateRulesVersion] in
// decimal, a 0x00 byte and the template (REQ-010). The severity name is
// r.Severity.String(), so SeverityUnknown hashes as an empty name and all
// records of unknown severity share ids by SQLSTATE and template. A zero
// r.SQLState hashes as no bytes at all, not as five zero bytes.
//
// The same message text under a different severity or SQLSTATE is therefore
// a different pattern. The id is a fixed-size array so that it can be a map
// key. PatternID does not allocate for any template AppendTemplate makes
// (CON-001); a template longer than 1,027 bytes, which only a caller can
// supply, may cost one hash state on the heap. template may be the borrowed
// result of AppendTemplate; PatternID does not retain it.
//
// To print the id as 16 lowercase hex characters, use encoding/hex:
//
//	id := PatternID(r, tmpl)
//	s := hex.EncodeToString(id[:])
//
// or hex.AppendEncode(dst, id[:]) to append it without a new string.
func PatternID(r *Record, template []byte) [8]byte {
	// The hash input is built in one stack buffer and hashed with Sum256,
	// because sha256.New escapes to the heap. Every template AppendTemplate
	// makes fits (REQ-011); a longer one passed by hand goes through
	// sha256.New, which allocates.
	var buf [patternHeadMax + templateLimit + len(ellipsis)]byte
	in := patternHead(buf[:0], r)
	var sum [sha256.Size]byte
	if len(in)+len(template) <= len(buf) {
		sum = sha256.Sum256(append(in, template...))
	} else {
		h := sha256.New()
		_, _ = h.Write(in)
		_, _ = h.Write(template)
		h.Sum(sum[:0])
	}
	var id [8]byte
	copy(id[:], sum[:])
	return id
}

// patternHeadMax is the longest input patternHead appends: a severity name
// of at most 7 bytes, a five-byte SQLSTATE, a decimal int of at most 20
// bytes and three 0x00 separators.
const patternHeadMax = 7 + 5 + 20 + 3

// patternHead appends the part of the PatternID hash input that comes
// before the template: severity, 0x00, SQLSTATE or nothing, 0x00, rule
// version, 0x00.
func patternHead(dst []byte, r *Record) []byte {
	dst = append(dst, r.Severity.String()...)
	dst = append(dst, 0)
	if r.SQLState != [5]byte{} {
		dst = append(dst, r.SQLState[:]...)
	}
	dst = append(dst, 0)
	dst = strconv.AppendInt(dst, TemplateRulesVersion, 10)
	return append(dst, 0)
}

// Tokenizer contract, for the masking rules built on it.
//
// tokenizer splits a message into tokens that tile it: every byte belongs to
// exactly one token, in order, so copying every token's bytes reproduces the
// input. A rule decides per token whether to copy it or write a placeholder.
// The tokenizer never allocates and never looks at more than the slice it is
// given.
//
// Word bytes are ASCII letters, digits, '_' and every byte above 0x7F. A word
// also takes '$' after its first byte, as a PostgreSQL identifier does, and
// takes a '.' that is followed by a word byte, so "t.id", "postgresql.conf"
// and "pgsql_tmp1234.0" are one word each. A digit inside a word therefore
// never starts a number (REQ-001: "pg_stat_16" and "xid8" stay).
//
// The kinds a rule will mask are:
//   - tokNumber: a decimal integer, decimal or scientific number with an
//     optional sign and an optional unit (see isUnit). The unit is attached
//     ("76kB", "0.8%") or, outside statement mode, separated by one space
//     ("12.345 ms", "16384 kB"); the token covers number and unit. A sign
//     belongs to the number only when the byte before it (in statement mode,
//     the previous non-space byte) is not a word byte, a closing bracket or
//     a quote, so "a-1" is "a", "-", "1" in both modes, and "a -1" is a
//     signed number in a message but a subtraction in a statement.
//     A number followed by word bytes that are not a unit is a word ("1abc").
//   - tokHex: "0x1F", and outside statement mode an LSN "0/16B3748". An LSN
//     followed by '/' is left alone so the path rule can take it.
//   - tokString: a single-quoted literal with its prefix: 'x', E'x' (backslash
//     escapes), U&'x', B'x', X'x', N'x'. A doubled quote does not end it. A
//     quote right after a word byte is punctuation, so "can't" is not a
//     literal. An unterminated literal runs to the end of the input.
//   - tokDollar: a dollar-quoted literal $tag$...$tag$ (the tag may be empty),
//     running to the end of the input when unterminated.
//   - tokIdent: a double-quoted token, "x" or U&"x", with doubled quotes. Class
//     22 and 23 rules and the path rule decide whether it stays.
//   - tokParam: an existing $n placeholder. A rule writes a fresh placeholder
//     for it, which renumbers it and makes masking idempotent (CON-002).
//
// The other kinds are tokWord, tokSpace (a run of white space), tokComment
// (statement mode only, see below) and tokPunct (one byte that is none of the
// above).
//
// A key=value pair is a tokWord followed by a tokPunct '='; the tokenizer
// does not know where a value ends. A rule that needs a span the tokenizer
// does not know may set pos to the end of that span and continue from there.
//
// Statement mode (stmt) is for the SQL after a statement marker: it turns off
// the spaced unit, so "SELECT 1 s" keeps the alias, and the LSN, so "c/2" is a
// division. It adds tokComment: a "--" comment up to the next line break, or a
// "/* */" comment, which nests as in PostgreSQL and runs to the end of the
// input when unterminated. A rule treats a comment as white space.

// tokKind is the class of one token.
type tokKind uint8

const (
	tokPunct tokKind = iota
	tokSpace
	tokWord
	tokNumber
	tokHex
	tokString
	tokDollar
	tokIdent
	tokParam
	tokComment
)

// token is the byte range s[start:end] of the tokenizer's input.
type token struct {
	start, end int
	kind       tokKind
}

// tokenizer walks s from pos. The zero value with s set is in message mode.
type tokenizer struct {
	s    []byte
	pos  int
	stmt bool
}

// next returns the token at pos and moves past it. It reports false at the
// end of the input.
func (z *tokenizer) next() (token, bool) {
	s, start := z.s, z.pos
	if start >= len(s) {
		return token{}, false
	}
	kind, end := tokPunct, start+1
	switch c := s[start]; {
	case isTemplateSpace(c):
		kind, end = tokSpace, start+1
		for end < len(s) && isTemplateSpace(s[end]) {
			end++
		}
	case z.stmt && c == '-' && start+1 < len(s) && s[start+1] == '-':
		kind, end = tokComment, start+2
		for end < len(s) && s[end] != '\n' && s[end] != '\r' {
			end++
		}
	case z.stmt && c == '/' && start+1 < len(s) && s[start+1] == '*':
		kind, end = tokComment, scanBlockComment(s, start+2)
	case c == '"':
		kind, end = tokIdent, scanQuoted(s, start+1, '"', false)
	case c == '\'':
		if start == 0 || !isWordByte(s[start-1]) {
			kind, end = tokString, scanQuoted(s, start+1, '\'', false)
		}
	case c == '$':
		kind, end = z.scanDollar(start)
	case isDigit(c),
		c == '.' && start+1 < len(s) && isDigit(s[start+1]) && (start == 0 || !isWordByte(s[start-1])),
		(c == '+' || c == '-') && z.numberAt(start+1) && z.signAllowed(start):
		kind, end = z.scanNumeric(start)
	case isWordByte(c):
		kind, end = z.scanWordish(start)
	}
	z.pos = end
	return token{start: start, end: end, kind: kind}, true
}

// numberAt reports whether a number body starts at i: a digit, or a '.'
// followed by a digit.
func (z *tokenizer) numberAt(i int) bool {
	s := z.s
	if i < len(s) && isDigit(s[i]) {
		return true
	}
	return i+1 < len(s) && s[i] == '.' && isDigit(s[i+1])
}

// signAllowed reports whether the sign at i belongs to the number after it.
// In statement mode it looks back past white space, so "= -1" is signed and
// "a -1" is not. In a message a sign after white space is always a sign, so
// "offset -5" masks the sign with the number.
func (z *tokenizer) signAllowed(i int) bool {
	j := i - 1
	if j >= 0 && isTemplateSpace(z.s[j]) && !z.stmt {
		return true
	}
	for j >= 0 && isTemplateSpace(z.s[j]) {
		j--
	}
	if j < 0 {
		return true
	}
	switch c := z.s[j]; c {
	case ')', ']', '"', '\'', '$':
		return false
	default:
		return !isWordByte(c)
	}
}

// scanNumeric scans a token that starts with a digit, a '.' before a digit,
// or a sign before either.
func (z *tokenizer) scanNumeric(start int) (tokKind, int) {
	s := z.s
	i := start
	signed := s[i] == '+' || s[i] == '-'
	if signed {
		i++
	}
	if !signed {
		if i+2 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') && isHexDigit(s[i+2]) {
			j := i + 3
			for j < len(s) && isHexDigit(s[j]) {
				j++
			}
			if !continuesWord(s, j) {
				return tokHex, j
			}
			return tokWord, scanWord(s, start)
		}
		if !z.stmt {
			if end, ok := scanLSN(s, start); ok {
				return tokHex, end
			}
		}
	}
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i+1 < len(s) && s[i] == '.' && isDigit(s[i+1]) {
		i += 2
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	if i+1 < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && isDigit(s[j]) {
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			i = j
		}
	}
	switch {
	case i < len(s) && s[i] == '%':
		return tokNumber, i + 1
	case i < len(s) && isWordByte(s[i]):
		j := i
		for j < len(s) && isWordByte(s[j]) {
			j++
		}
		if isUnit(s[i:j]) && !continuesWord(s, j) {
			return tokNumber, j
		}
		if signed {
			return tokPunct, start + 1
		}
		return tokWord, scanWord(s, start)
	case !z.stmt && i+1 < len(s) && s[i] == ' ' && isWordByte(s[i+1]):
		j := i + 1
		for j < len(s) && isWordByte(s[j]) {
			j++
		}
		if isUnit(s[i+1:j]) && !continuesWord(s, j) {
			return tokNumber, j
		}
	}
	return tokNumber, i
}

// scanWordish scans a token that starts with a word byte: a prefixed string
// or identifier, an LSN, or a word.
func (z *tokenizer) scanWordish(start int) (tokKind, int) {
	s := z.s
	at := func(i int, c byte) bool { return i < len(s) && s[i] == c }
	switch s[start] {
	case 'E', 'e':
		if at(start+1, '\'') {
			return tokString, scanQuoted(s, start+2, '\'', true)
		}
	case 'B', 'b', 'X', 'x', 'N', 'n':
		if at(start+1, '\'') {
			return tokString, scanQuoted(s, start+2, '\'', false)
		}
	case 'U', 'u':
		if at(start+1, '&') && at(start+2, '\'') {
			return tokString, scanQuoted(s, start+3, '\'', false)
		}
		if at(start+1, '&') && at(start+2, '"') {
			return tokIdent, scanQuoted(s, start+3, '"', false)
		}
	}
	if !z.stmt {
		if end, ok := scanLSN(s, start); ok {
			return tokHex, end
		}
	}
	return tokWord, scanWord(s, start)
}

// scanDollar scans a token that starts with '$': a placeholder, a
// dollar-quoted literal, or a lone '$'.
func (z *tokenizer) scanDollar(start int) (tokKind, int) {
	s := z.s
	j := start + 1
	if j < len(s) && isDigit(s[j]) {
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		return tokParam, j
	}
	if j < len(s) && s[j] != '$' {
		if isDigit(s[j]) || !isWordByte(s[j]) {
			return tokPunct, j
		}
		for j < len(s) && isWordByte(s[j]) {
			j++
		}
	}
	if j >= len(s) || s[j] != '$' {
		return tokPunct, start + 1
	}
	open := j + 1
	tag := s[start:open]
	if k := bytes.Index(s[open:], tag); k >= 0 {
		return tokDollar, open + k + len(tag)
	}
	return tokDollar, len(s)
}

// scanBlockComment returns the end of a "/* */" comment whose body starts at
// i. Comments nest, so each "/*" needs its own "*/". An unterminated comment
// runs to the end of s.
func scanBlockComment(s []byte, i int) int {
	depth := 1
	for i+1 < len(s) {
		switch {
		case s[i] == '/' && s[i+1] == '*':
			depth++
			i += 2
		case s[i] == '*' && s[i+1] == '/':
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(s)
}

// scanQuoted returns the end of a quoted token whose body starts at i: the
// index after the closing quote q, or len(s) when there is none. A doubled
// q is part of the body; with backslash set, so is the byte after a '\'.
func scanQuoted(s []byte, i int, q byte, backslash bool) int {
	for i < len(s) {
		switch c := s[i]; {
		case backslash && c == '\\':
			i += 2
		case c == q:
			if i+1 < len(s) && s[i+1] == q {
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return len(s)
}

// scanLSN reports whether an LSN such as "0/16B3748" starts at i, and where
// it ends. Each half is one to eight hex digits. An LSN followed by a word
// byte or by '/' is not one: it is part of a word or of a path.
func scanLSN(s []byte, i int) (int, bool) {
	hexRun := func(i int) int {
		j := i
		for j < len(s) && j-i < 9 && isHexDigit(s[j]) {
			j++
		}
		return j
	}
	j := hexRun(i)
	if j == i || j-i > 8 || j >= len(s) || s[j] != '/' {
		return 0, false
	}
	k := hexRun(j + 1)
	if k == j+1 || k-j-1 > 8 || continuesWord(s, k) || (k < len(s) && s[k] == '/') {
		return 0, false
	}
	return k, true
}

// scanWord returns the end of the word that starts at i, following the word
// rules of the tokenizer contract.
func scanWord(s []byte, i int) int {
	for i < len(s) {
		switch {
		case isWordByte(s[i]) || s[i] == '$':
			i++
		case s[i] == '.' && i+1 < len(s) && isWordByte(s[i+1]):
			i += 2
		default:
			return i
		}
	}
	return i
}

// continuesWord reports whether the byte at i would extend a word, which
// means the token before it is not a standalone value.
func continuesWord(s []byte, i int) bool {
	if i >= len(s) {
		return false
	}
	return isWordByte(s[i]) || s[i] == '$' || (s[i] == '.' && i+1 < len(s) && isWordByte(s[i+1]))
}

// isUnit reports whether b is a unit PostgreSQL writes after a number: the
// memory and time units of its GUCs and of size and duration messages.
func isUnit(b []byte) bool {
	switch string(b) {
	case "B", "kB", "MB", "GB", "TB", "PB", "us", "ms", "s", "min", "h", "d":
		return true
	}
	return false
}

func isWordByte(c byte) bool {
	return c >= 0x80 || c == '_' || isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isTemplateSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// placeholders numbers the placeholders of one template from $1, left to
// right (REQ-008). The zero value starts at $1.
type placeholders struct {
	n int64
}

// appendNext appends the next placeholder to dst. strconv.AppendInt writes
// into dst, so this allocates only when dst has to grow.
func (p *placeholders) appendNext(dst []byte) []byte {
	p.n++
	return strconv.AppendInt(append(dst, '$'), p.n, 10)
}
