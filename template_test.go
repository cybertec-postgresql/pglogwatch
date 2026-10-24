package pglogwatch

import (
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pglogwatch/internal/allocs"
	"github.com/stretchr/testify/assert"
)

// sqlState turns a five-character code such as "42P01" into the fixed-size
// array that Record carries. An empty string gives the zero value, which is
// what a LOG record without a SQLSTATE has.
func sqlState(t *testing.T, code string) [5]byte {
	t.Helper()
	var s [5]byte
	if code == "" {
		return s
	}
	if len(code) != len(s) {
		t.Fatalf("SQLSTATE %q is not five characters", code)
	}
	copy(s[:], code)
	return s
}

// templateGolden holds every row of the table in section 4 of
// spec/message-template.md (AC-001).
//
// pending names the user story and task that make a row pass. A row with a
// pending value is skipped with that reason, so the task that delivers the
// rule turns its rows on by clearing the field. Rows whose template equals
// the message pass with the identity placeholder of T001 and run from the
// start, which keeps them guarded while the masking rules arrive.
var templateGolden = []struct {
	name     string
	severity Severity
	sqlState string
	message  string
	want     string
	pending  string
}{
	{
		name:     "undefined table keeps the identifier",
		severity: SeverityError,
		sqlState: "42P01",
		message:  `relation "orders_2026" does not exist`,
		want:     `relation "orders_2026" does not exist`,
	},
	{
		// Class 23 masks every double-quoted token, the constraint name
		// included (REQ-005, GUD-001).
		name:     "unique violation masks the constraint in class 23",
		severity: SeverityError,
		sqlState: "23505",
		message:  `duplicate key value violates unique constraint "users_email_key"`,
		want:     `duplicate key value violates unique constraint $1`,
		pending:  "US1/T006",
	},
	{
		name:     "invalid input syntax masks the quoted data",
		severity: SeverityError,
		sqlState: "22P02",
		message:  `invalid input syntax for type integer: "abc"`,
		want:     `invalid input syntax for type integer: $1`,
		pending:  "US1/T006",
	},
	{
		// The role name stays: failed authentication is rare and the role
		// is what an operator acts on (spec section 7).
		name:     "failed password keeps the role",
		severity: SeverityFatal,
		sqlState: "28P01",
		message:  `password authentication failed for user "bob"`,
		want:     `password authentication failed for user "bob"`,
	},
	{
		name:     "connection authorized masks key=value values",
		severity: SeverityLog,
		message:  `connection authorized: user=bob database=shop application_name=psql`,
		want:     `connection authorized: user=$1 database=$2 application_name=$3`,
	},
	{
		// The two spaces before "statement:" are what PostgreSQL writes;
		// the template collapses them (REQ-009, T008).
		name:     "duration with a simple-protocol statement",
		severity: SeverityLog,
		message:  `duration: 12.345 ms  statement: SELECT * FROM t WHERE id = 42 AND name = 'x'`,
		want:     `duration: $1 statement: SELECT * FROM t WHERE id = $2 AND name = $3`,
		pending:  "US2/T008",
	},
	{
		// Needs the prepared-statement name rule of T008 and the IN list
		// collapse of T009; T009 is the later of the two.
		name:     "duration with an extended-protocol execute",
		severity: SeverityLog,
		message:  `duration: 3.1 ms  execute S_7: select a from b where c in (1, 2, 3)`,
		want:     `duration: $1 execute $2: select a from b where c in (...)`,
		pending:  "US2/T009",
	},
	{
		// The spec abbreviates this row with an ellipsis. This is the full
		// PostgreSQL 16 message, which added the lsn and redo lsn fields.
		// It expands the row under the same rules: every number is masked
		// with its unit (a unit after a space counts, as "12.345 ms" does
		// in the duration rows), and every key=value value is masked,
		// including the LSNs (REQ-001, REQ-002, REQ-004).
		name:     "checkpoint complete masks counts, times and LSNs",
		severity: SeverityLog,
		message: `checkpoint complete: wrote 123 buffers (0.8%); 0 WAL file(s) added, 0 removed, 1 recycled; ` +
			`write=12.345 s, sync=0.012 s, total=12.401 s; sync files=7, longest=0.004 s, average=0.002 s; ` +
			`distance=16384 kB, estimate=16384 kB; lsn=0/3000060, redo lsn=0/3000028`,
		want: `checkpoint complete: wrote $1 buffers ($2); $3 WAL file(s) added, $4 removed, $5 recycled; ` +
			`write=$6, sync=$7, total=$8; sync files=$9, longest=$10, average=$11; ` +
			`distance=$12, estimate=$13; lsn=$14, redo lsn=$15`,
	},
	{
		// The path is a value even inside double quotes (REQ-007).
		name:     "temporary file masks the path and size",
		severity: SeverityLog,
		message:  `temporary file: path "base/pgsql_tmp/pgsql_tmp1234.0", size 1048576`,
		want:     `temporary file: path $1, size $2`,
		pending:  "US1/T006",
	},
	{
		// auto_explain writes the plan on the lines after "plan:". None of
		// it may reach the template (REQ-012).
		name:     "auto_explain plan is cut at the marker",
		severity: SeverityLog,
		message: "duration: 1500.2 ms  plan:\n" +
			"Query Text: SELECT o.id, c.email FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.total > 1000\n" +
			"Hash Join  (cost=35.50..1442.10 rows=3200 width=40) (actual time=0.512..1498.733 rows=3187 loops=1)\n" +
			"  Hash Cond: (o.customer_id = c.id)\n" +
			"  ->  Seq Scan on orders o  (cost=0.00..1398.00 rows=3200 width=12) (actual time=0.010..1495.120 rows=3187 loops=1)\n" +
			"        Filter: (total > '1000'::numeric)\n" +
			"        Rows Removed by Filter: 96813\n" +
			"  ->  Hash  (cost=22.00..22.00 rows=1080 width=36) (actual time=0.480..0.481 rows=1080 loops=1)\n" +
			"        Buckets: 2048  Batches: 1  Memory Usage: 76kB\n" +
			"        ->  Seq Scan on customers c  (cost=0.00..22.00 rows=1080 width=36) (actual time=0.004..0.210 rows=1080 loops=1)",
		want:    `duration: $1 plan: …`,
		pending: "US2/T010",
	},
	{
		name:     "deadlock has nothing to mask",
		severity: SeverityError,
		sqlState: "40P01",
		message:  `deadlock detected`,
		want:     `deadlock detected`,
	},
}

// TestAppendTemplateGolden checks every example of spec section 4 (AC-001).
// The prefix in dst checks that AppendTemplate appends rather than
// overwrites.
func TestAppendTemplateGolden(t *testing.T) {
	const prefix = "keep:"
	for _, c := range templateGolden {
		t.Run(c.name, func(t *testing.T) {
			if c.pending != "" {
				t.Skip("delivered by " + c.pending)
			}
			r := &Record{
				Severity: c.severity,
				SQLState: sqlState(t, c.sqlState),
				Message:  []byte(c.message),
			}
			got := AppendTemplate([]byte(prefix), r)
			assert.Equal(t, prefix+c.want, string(got))
		})
	}
}

// TestAppendTemplateLiterals checks the literal rules of T004 one by one:
// standalone numbers with their units (REQ-001), hex and LSN tokens
// (REQ-002), and single- and dollar-quoted literals (REQ-003). The messages
// avoid key=value pairs, ids, paths and double quotes, whose rules come in
// later tasks.
func TestAppendTemplateLiterals(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"integer", "sorted 42 rows", "sorted $1 rows"},
		{"number inside a word stays", "pg_stat_16 stays", "pg_stat_16 stays"},
		{"digit at the end of a word stays", "type xid8 is wide", "type xid8 is wide"},
		{"signed integer", "offset -5 is negative", "offset $1 is negative"},
		{"scientific", "cost 1.5e3 and 2.5E-3", "cost $1 and $2"},
		{"decimal without a leading digit", "ratio .25 used", "ratio $1 used"},
		{"spaced unit", "took 12 ms to run", "took $1 to run"},
		{"attached unit and percent", "used 76kB (0.8%)", "used $1 ($2)"},
		{"hex", "flags 0x1F set", "flags $1 set"},
		{"lsn", "redo starts at 0/16B3748", "redo starts at $1"},
		{"single-quoted literal", "value 'secret' rejected", "value $1 rejected"},
		{"doubled quote", "value 'it''s' rejected", "value $1 rejected"},
		{"backslash escape", `value E'a\'b' rejected`, "value $1 rejected"},
		{"unicode literal", `value U&'d\0061' rejected`, "value $1 rejected"},
		{"dollar-quoted literal", "body $q$x$q$ rejected", "body $1 rejected"},
		{"empty dollar tag", "body $$it's$$ rejected", "body $1 rejected"},
		{"apostrophe in a word stays", "can't open 3 files", "can't open $1 files"},
		{"numbering runs left to right", "1 and 'a' and 0x2 and $t$b$t$", "$1 and $2 and $3 and $4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// TestAppendTemplateKeyValue checks REQ-004: every key stays and every value,
// whatever its tokens, becomes one placeholder.
func TestAppendTemplateKeyValue(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"word value", "user=bob database=shop", "user=$1 database=$2"},
		{"value with punctuation and digits", "user=SECRET-2 database=x", "user=$1 database=$2"},
		{"empty value at the end", "user=bob application_name=", "user=$1 application_name=$2"},
		{"empty value before the next key", "application_name= user=bob", "application_name=$1 user=$2"},
		{"value ends at a comma", "protocol=TLSv1.3, cipher=TLS_AES_256_GCM_SHA384, bits=256)",
			"protocol=$1, cipher=$2, bits=$3)"},
		{"value ends at a semicolon", "total=12.401 s; sync files=7", "total=$1; sync files=$2"},
		{"number with a spaced unit is one value", "write=12.345 s, distance=16384 kB", "write=$1, distance=$2"},
		{"lsn value", "lsn=0/3000060, redo lsn=0/3000028", "lsn=$1, redo lsn=$2"},
		{"single-quoted value with a space", "options='-c x=1' user=a", "options=$1 user=$2"},
		{"double-quoted value with a space", `name="a b" user=a`, "name=$1 user=$2"},
		{"host and port", "host=[local] port=5432", "host=$1 port=$2"},
		{"ipv6 host", "host=::1 port=5432", "host=$1 port=$2"},
		{"value inside parentheses", "(user=bob)", "(user=$1)"},
		{"spaced equals is not a pair", "x = bob", "x = bob"},
		{"numbering continues after a pair", "wrote 3 user=bob 4", "wrote $1 user=$2 $3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// TestAppendTemplateIDs checks REQ-006: the id after a process, transaction,
// relation, OID or session word becomes a placeholder, also when it is not a
// plain number. A quoted identifier or a plain word after such a word stays.
func TestAppendTemplateIDs(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"lock wait", "process 1234 waits for ShareLock on transaction 7890; blocked by process 4321",
			"process $1 waits for ShareLock on transaction $2; blocked by process $3"},
		{"virtual transaction", "process 12 waits for ExclusiveLock on virtual transaction 3/1234",
			"process $1 waits for ExclusiveLock on virtual transaction $2"},
		{"session id", "session 5f3c1a2b.4d2 ended", "session $1 ended"},
		{"PID in upper case", "terminating PID 1234", "terminating PID $1"},
		{"relation oid", "could not open relation 16384", "could not open relation $1"},
		{"with OID", "cache lookup failed for relation with OID 16384", "cache lookup failed for relation with OID $1"},
		{"database oid", "database 16384 must be vacuumed", "database $1 must be vacuumed"},
		{"backend", "backend 5f3c1a2b crashed", "backend $1 crashed"},
		{"hex word without a leading digit", "session ab12cd.4 ended", "session $1 ended"},
		{"quoted identifier stays", `relation "orders" does not exist`, `relation "orders" does not exist`},
		{"plain word stays", "database system is ready", "database system is ready"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// tok is a token as the tests write it: its kind and its text.
type tok struct {
	kind tokKind
	text string
}

// tokenize runs the tokenizer over s and returns every token.
func tokenize(s string, stmt bool) []tok {
	z := tokenizer{s: []byte(s), stmt: stmt}
	var out []tok
	for {
		t, ok := z.next()
		if !ok {
			return out
		}
		out = append(out, tok{t.kind, s[t.start:t.end]})
	}
}

// valueTokens keeps the tokens a masking rule may replace, which is what
// most cases care about; spaces, words and punctuation are left out.
func valueTokens(ts []tok) []tok {
	var out []tok
	for _, t := range ts {
		switch t.kind {
		case tokSpace, tokWord, tokPunct:
		default:
			out = append(out, t)
		}
	}
	return out
}

func TestTokenizerValues(t *testing.T) {
	cases := []struct {
		name string
		in   string
		stmt bool
		want []tok
	}{
		{"integer", "wrote 123 buffers", false, []tok{{tokNumber, "123"}}},
		{"number inside a word stays", "pg_stat_16 xid8 a1b2", false, nil},
		{"decimal and scientific", "1.5 .25 1e10 2.5E-3", false,
			[]tok{{tokNumber, "1.5"}, {tokNumber, ".25"}, {tokNumber, "1e10"}, {tokNumber, "2.5E-3"}}},
		{"attached unit", "76kB 0.8% 5s", false,
			[]tok{{tokNumber, "76kB"}, {tokNumber, "0.8%"}, {tokNumber, "5s"}}},
		{"spaced unit", "duration: 12.345 ms  statement", false, []tok{{tokNumber, "12.345 ms"}}},
		{"spaced unit before punctuation", "distance=16384 kB, write=0.004 s;", false,
			[]tok{{tokNumber, "16384 kB"}, {tokNumber, "0.004 s"}}},
		{"word after a number is not a unit", "123 buffers 1 msx", false,
			[]tok{{tokNumber, "123"}, {tokNumber, "1"}}},
		{"no spaced unit in statement mode", "SELECT 1 s", true, []tok{{tokNumber, "1"}}},
		{"number followed by letters is a word", "1abc 12.5abc", false, nil},
		{"sign after an operator", "x = -5 AND y=+2", true,
			[]tok{{tokNumber, "-5"}, {tokNumber, "+2"}}},
		{"minus after an operand is an operator", "a-1 a -1 (b)-2", true,
			[]tok{{tokNumber, "1"}, {tokNumber, "1"}, {tokNumber, "2"}}},
		{"sign after a space in a message", "a-1 a -1 a - 1", false,
			[]tok{{tokNumber, "1"}, {tokNumber, "-1"}, {tokNumber, "1"}}},
		{"version string", "16.4.1", false, []tok{{tokNumber, "16.4"}, {tokNumber, "1"}}},
		{"hex", "0x1F 0Xff 0x1g", false, []tok{{tokHex, "0x1F"}, {tokHex, "0Xff"}}},
		{"lsn", "lsn=0/3000060, redo lsn=A/16B3748.", false,
			[]tok{{tokHex, "0/3000060"}, {tokHex, "A/16B3748"}}},
		{"lsn-shaped path prefix is not an lsn", "0/16B3748/x", false, []tok{{tokNumber, "0"}}},
		{"no lsn in statement mode", "c/2 1/2", true,
			[]tok{{tokNumber, "2"}, {tokNumber, "1"}, {tokNumber, "2"}}},
		{"single quotes", "name = 'x' AND s = 'it''s'", true,
			[]tok{{tokString, "'x'"}, {tokString, "'it''s'"}}},
		{"prefixed literals", `E'a\'b' U&'d\0061' B'101' X'1F' N'n'`, true,
			[]tok{{tokString, `E'a\'b'`}, {tokString, `U&'d\0061'`}, {tokString, "B'101'"},
				{tokString, "X'1F'"}, {tokString, "N'n'"}}},
		{"backslash is literal without E", `'a\' b`, true, []tok{{tokString, `'a\'`}}},
		{"apostrophe after a word is punctuation", "can't connect", false, nil},
		{"unterminated literal runs to the end", "x = 'abc", true, []tok{{tokString, "'abc"}}},
		{"dollar quotes", "$$a'b$$ $fn$ x $y$ $fn$ $q$open", true,
			[]tok{{tokDollar, "$$a'b$$"}, {tokDollar, "$fn$ x $y$ $fn$"}, {tokDollar, "$q$open"}}},
		{"placeholders", "id = $1 AND x=$12", true, []tok{{tokParam, "$1"}, {tokParam, "$12"}}},
		{"dollar inside an identifier", "a$1 b$c", true, nil},
		{"lone dollar", "costs $ x $a", true, nil},
		{"double quotes", `relation "orders_2026" and "a""b" and U&"d"`, false,
			[]tok{{tokIdent, `"orders_2026"`}, {tokIdent, `"a""b"`}, {tokIdent, `U&"d"`}}},
		{"unterminated double quote", `x "abc`, false, []tok{{tokIdent, `"abc`}}},
		{"high bytes are word bytes", "größe 5 Größe5", false, []tok{{tokNumber, "5"}}},
		{"dotted words", "t.id postgresql.conf pgsql_tmp1234.0", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, valueTokens(tokenize(c.in, c.stmt)))
		})
	}
}

// TestTokenizerKeyValue pins what the key=value rule builds on: the key is a
// word directly followed by '='.
func TestTokenizerKeyValue(t *testing.T) {
	got := tokenize("user=bob database=shop", false)
	assert.Equal(t, []tok{
		{tokWord, "user"}, {tokPunct, "="}, {tokWord, "bob"}, {tokSpace, " "},
		{tokWord, "database"}, {tokPunct, "="}, {tokWord, "shop"},
	}, got)
}

// TestTokenizerTiles checks that the tokens of every golden message, in both
// modes, cover the input exactly once and in order, so a rule that copies
// every token it does not mask reproduces the rest of the message.
func TestTokenizerTiles(t *testing.T) {
	inputs := []string{"", " ", "'", `"`, "$", "-", ".", "0x", "1e", "1e+", "E'\\", "U&", "$a"}
	for _, c := range templateGolden {
		inputs = append(inputs, c.message)
	}
	for _, in := range inputs {
		for _, stmt := range []bool{false, true} {
			var b strings.Builder
			for _, tk := range tokenize(in, stmt) {
				assert.NotEmpty(t, tk.text, "empty token in %q", in)
				b.WriteString(tk.text)
			}
			assert.Equal(t, in, b.String(), "stmt=%v", stmt)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	var p placeholders
	dst := []byte("keep:")
	for range 12 {
		dst = p.appendNext(dst)
		dst = append(dst, ' ')
	}
	assert.Equal(t, "keep:$1 $2 $3 $4 $5 $6 $7 $8 $9 $10 $11 $12 ", string(dst))
}

// TestTokenizerNoAllocs guards CON-001 for the parts T003 adds: tokenizing a
// message and writing placeholders into a buffer with room.
func TestTokenizerNoAllocs(t *testing.T) {
	msgs := make([][]byte, 0, len(templateGolden))
	for _, c := range templateGolden {
		msgs = append(msgs, []byte(c.message))
	}
	dst := make([]byte, 0, 4096)
	allocs.Zero(t, 100, func() {
		for _, m := range msgs {
			var p placeholders
			z := tokenizer{s: m}
			dst = dst[:0]
			for {
				tk, ok := z.next()
				if !ok {
					break
				}
				if tk.kind != tokWord && tk.kind != tokSpace && tk.kind != tokPunct {
					dst = p.appendNext(dst)
				} else {
					dst = append(dst, m[tk.start:tk.end]...)
				}
			}
		}
	})
}
