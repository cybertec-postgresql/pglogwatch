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

// templateCase is one unit-test case of AppendTemplate for a record with no
// SQLSTATE: in is the message and want the template.
type templateCase struct {
	name, in, want string
}

// templateStateCase is a templateCase whose record carries a SQLSTATE.
type templateStateCase struct {
	name, state, in, want string
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
	},
	{
		name:     "invalid input syntax masks the quoted data",
		severity: SeverityError,
		sqlState: "22P02",
		message:  `invalid input syntax for type integer: "abc"`,
		want:     `invalid input syntax for type integer: $1`,
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
	},
	{
		// Needs the prepared-statement name rule of T008 and the IN list
		// collapse of T009; T009 is the later of the two.
		name:     "duration with an extended-protocol execute",
		severity: SeverityLog,
		message:  `duration: 3.1 ms  execute S_7: select a from b where c in (1, 2, 3)`,
		want:     `duration: $1 execute $2: select a from b where c in (...)`,
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

// templateLiteralCases holds the cases of TestAppendTemplateLiterals.
var templateLiteralCases = []templateCase{
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

// TestAppendTemplateLiterals checks the literal rules of T004 one by one:
// standalone numbers with their units (REQ-001), hex and LSN tokens
// (REQ-002), and single- and dollar-quoted literals (REQ-003). The messages
// avoid key=value pairs, ids, paths and double quotes, whose rules come in
// later tasks.
func TestAppendTemplateLiterals(t *testing.T) {
	for _, c := range templateLiteralCases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// templateKeyValueCases holds the cases of TestAppendTemplateKeyValue.
var templateKeyValueCases = []templateCase{
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

// TestAppendTemplateKeyValue checks REQ-004: every key stays and every value,
// whatever its tokens, becomes one placeholder.
func TestAppendTemplateKeyValue(t *testing.T) {
	for _, c := range templateKeyValueCases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// templateIDCases holds the cases of TestAppendTemplateIDs.
var templateIDCases = []templateCase{
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

// TestAppendTemplateIDs checks REQ-006: the id after a process, transaction,
// relation, OID or session word becomes a placeholder, also when it is not a
// plain number. A quoted identifier or a plain word after such a word stays.
func TestAppendTemplateIDs(t *testing.T) {
	for _, c := range templateIDCases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// templateQuotedCases holds the cases of TestAppendTemplateQuoted.
var templateQuotedCases = []templateStateCase{
	{"identifier stays", "42P01", `relation "orders_2026" does not exist`, `relation "orders_2026" does not exist`},
	{"identifier stays without a SQLSTATE", "", `relation "orders" is locked`, `relation "orders" is locked`},
	{"class 22 masks every quoted token", "22023", `invalid value "SECRET-3" for "x"`, `invalid value $1 for $2`},
	{"class 23 masks the constraint", "23503", `insert on table "a" violates foreign key constraint "a_b_fkey"`,
		`insert on table $1 violates foreign key constraint $2`},
	{"class 42 is not a data class", "42703", `column "SECRET" does not exist`, `column "SECRET" does not exist`},
	{"input syntax for type in another class", "XX000", `invalid input syntax for type integer: "abc"`,
		`invalid input syntax for type integer: $1`},
	{"input syntax for without type", "XX000", `invalid input syntax for integer: "abc"`,
		`invalid input syntax for integer: $1`},
	{"quoted type name", "XX000", `invalid input syntax for type "MyType": "abc"`,
		`invalid input syntax for type "MyType": $1`},
	{"enum value", "XX000", `invalid input value for enum mood: "x"`, `invalid input value for enum mood: $1`},
	{"for without a colon keeps the role", "28P01", `password authentication failed for user "bob"`,
		`password authentication failed for user "bob"`},
	{"punctuation drops the for phrase", "28000", `no entry for host "h", user "bob": x "y"`,
		`no entry for host "h", user "bob": x "y"`},
}

// TestAppendTemplateQuoted checks REQ-005: a double-quoted token is an
// identifier and stays, except under SQLSTATE class 22 or 23, and after a
// "for <words>:" phrase in any class.
func TestAppendTemplateQuoted(t *testing.T) {
	for _, c := range templateQuotedCases {
		t.Run(c.name, func(t *testing.T) {
			r := &Record{SQLState: sqlState(t, c.state), Message: []byte(c.in)}
			got := AppendTemplate([]byte("keep:"), r)
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// templatePathCases holds the cases of TestAppendTemplatePaths.
var templatePathCases = []templateCase{
	{"quoted base path", `temporary file: path "base/pgsql_tmp/pgsql_tmp1234.0", size 1048576`,
		`temporary file: path $1, size $2`},
	{"quoted pg_wal path", `could not open file "pg_wal/000000010000000000000001": No such file or directory`,
		`could not open file $1: No such file or directory`},
	{"quoted absolute path", `could not open file "/var/lib/postgresql/16/main/postgresql.conf": Permission denied`,
		`could not open file $1: Permission denied`},
	{"quoted tablespace path", `could not stat "pg_tblspc/16385/PG_16_202307071/16384"`, `could not stat $1`},
	{"quoted global path", `could not read "global/pg_control"`, `could not read $1`},
	{"quoted relative path", `loaded "./conf.d/a.conf"`, `loaded $1`},
	{"windows path with backslashes", `could not open file "C:\Program Files\PostgreSQL\16\data\pg_hba.conf"`,
		`could not open file $1`},
	{"windows path with slashes", `could not open "c:/pgdata/base/1/2"`, `could not open $1`},
	{"bare absolute path", "listening on Unix socket /var/run/postgresql/.s.PGSQL.5432", "listening on Unix socket $1"},
	{"bare path before a colon", "could not open /tmp/x.log: denied", "could not open $1: denied"},
	{"bare path ends a sentence", "archived to /archive/0001.", "archived to $1."},
	{"bare path in parentheses", "data directory (/srv/pg) is fine", "data directory ($1) is fine"},
	{"bare windows path", `writing C:\pg\log\a.log now`, "writing $1 now"},
	{"bare pg_ path", "removing pg_wal/000000010000000000000002", "removing $1"},
	{"spaced division stays", "operator does not exist: integer / text", "operator does not exist: integer / text"},
	{"division between words stays", "t/s and (x)/y", "t/s and (x)/y"},
	{"lsn stays an lsn", "redo at 0/16B3748 done", "redo at $1 done"},
	{"pg_ word without a slash stays", "pg_stat_statements must be loaded", "pg_stat_statements must be loaded"},
	{"quoted identifier with a slash stays", `relation "a/b" is odd`, `relation "a/b" is odd`},
}

// TestAppendTemplatePaths checks REQ-007: a file path, bare or inside double
// quotes, becomes one placeholder, while a division or an LSN does not.
func TestAppendTemplatePaths(t *testing.T) {
	for _, c := range templatePathCases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// templateStatementCases holds the cases of TestAppendTemplateStatements.
var templateStatementCases = []templateStateCase{
	{"statement", "", "statement: SELECT * FROM t WHERE id = 42", "statement: SELECT * FROM t WHERE id = $1"},
	{"duration and statement", "", "duration: 0.5 ms  statement: select 1", "duration: $1 statement: select $2"},
	{"execute names a statement", "", "execute S_1: SELECT a FROM t WHERE b = $1",
		"execute $1: SELECT a FROM t WHERE b = $2"},
	{"execute the unnamed statement", "", "execute <unnamed>: SELECT 1", "execute $1: SELECT $2"},
	{"execute fetch from a portal", "", "execute fetch from S_2/C_3: SELECT 1", "execute fetch from $1: SELECT $2"},
	{"parse", "", "duration: 0.1 ms  parse S_3: INSERT INTO t VALUES ($1)",
		"duration: $1 parse $2: INSERT INTO t VALUES (...)"},
	{"bind", "", "duration: 0.1 ms  bind <unnamed>: UPDATE t SET a = 'x'", "duration: $1 bind $2: UPDATE t SET a = $3"},
	{"fastpath function call", "", `fastpath function call: "lo_open" (OID 952)`,
		`fastpath function call: "lo_open" (OID $1)`},
	{"line comment", "", "statement: SELECT 1 -- note 5\nFROM t", "statement: SELECT $1 FROM t"},
	{"block comment", "", "statement: SELECT /* a /* nested */ 5 */ x FROM t", "statement: SELECT x FROM t"},
	{"comment between tokens", "", "statement: SELECT a/**/FROM t", "statement: SELECT a FROM t"},
	{"trailing comment", "", "statement: SELECT 1 -- done", "statement: SELECT $1"},
	{"unterminated block comment", "", "statement: SELECT 1 /* 'SECRET-1'", "statement: SELECT $1"},
	{"newlines and tabs", "", "statement: SELECT a,\n\tb\r\nFROM   t\n", "statement: SELECT a, b FROM t"},
	{"e-mail address literal", "", "statement: SELECT id FROM users WHERE email = 'bob@example.com'",
		"statement: SELECT id FROM users WHERE email = $1"},
	{"keyword case stays", "", "statement: Select A From T", "statement: Select A From T"},
	{"quoted identifier stays", "", `statement: SELECT "Name" FROM "T"`, `statement: SELECT "Name" FROM "T"`},
	{"quoted identifier stays in class 23", "23505", `statement: INSERT INTO "users" VALUES (1)`,
		`statement: INSERT INTO "users" VALUES (...)`},
	{"equals is not a key", "", "statement: SELECT * FROM t WHERE a=b", "statement: SELECT * FROM t WHERE a=b"},
	{"id word is a column", "", "statement: SELECT pid FROM s WHERE relation = x1", "statement: SELECT pid FROM s WHERE relation = x1"},
	{"slash is a division", "", "statement: SELECT a /b, c/2 FROM t", "statement: SELECT a /b, c/$1 FROM t"},
	{"minus comment is not a sign", "", "statement: SELECT x --1\n", "statement: SELECT x"},
	{"placeholders are renumbered", "", "statement: SELECT $3, $1", "statement: SELECT $1, $2"},
	{"marker only at the start or after a duration", "", "could not parse error: 42", "could not parse error: $1"},
	{"name with a space is not a marker", "", "bind message supplies 2 parameters", "bind message supplies $1 parameters"},
	{"white space collapses in the message", "", "  wrote  3\tbuffers \n", "wrote $1 buffers"},
	{"template is a fixed point", "", "$1 statement: SELECT $2", "$1 statement: SELECT $2"},
	{"masked name survives", "", "duration: $1 execute $2: SELECT $3", "duration: $1 execute $2: SELECT $3"},
	{"IN list collapses", "", "statement: SELECT a FROM t WHERE c IN (1, 'x', $3)",
		"statement: SELECT a FROM t WHERE c IN (...)"},
	{"IN list keeps the numbering contiguous", "", "duration: 1 ms  execute S_1: select a from b where c in (1, 2, 3) and d = 4",
		"duration: $1 execute $2: select a from b where c in (...) and d = $3"},
	{"IN keyword case stays", "", "statement: SELECT 1 WHERE a In ( 1 ,2 )", "statement: SELECT $1 WHERE a In (...)"},
	{"NOT IN list collapses", "", "statement: SELECT 1 WHERE a NOT IN (1, 2)", "statement: SELECT $1 WHERE a NOT IN (...)"},
	{"IN list with a comment collapses", "", "statement: SELECT 1 WHERE a in (1, /* two */ 2)", "statement: SELECT $1 WHERE a in (...)"},
	{"IN list with a negative number collapses", "", "statement: SELECT 1 WHERE a in (-1, 2)", "statement: SELECT $1 WHERE a in (...)"},
	{"IN list with a column stays", "", "statement: SELECT 1 WHERE x IN (a, 1)", "statement: SELECT $1 WHERE x IN (a, $2)"},
	{"IN subquery stays", "", "statement: SELECT 1 WHERE x IN (select 1)", "statement: SELECT $1 WHERE x IN (select $2)"},
	{"IN list with a cast stays", "", "statement: SELECT 1 WHERE x IN (1::int)", "statement: SELECT $1 WHERE x IN ($2::int)"},
	{"empty IN list stays", "", "statement: SELECT 1 WHERE x IN ()", "statement: SELECT $1 WHERE x IN ()"},
	{"IN list without a closing paren stays", "", "statement: SELECT 1 WHERE x IN (1, 2", "statement: SELECT $1 WHERE x IN ($2, $3"},
	{"ANY array stays", "", "statement: SELECT 1 WHERE x = ANY(ARRAY[1, 2])", "statement: SELECT $1 WHERE x = ANY(ARRAY[$2, $3])"},
	{"VALUES tuples collapse", "", "statement: INSERT INTO t VALUES (1, 'a'), ($1, 'b') RETURNING id",
		"statement: INSERT INTO t VALUES (...) RETURNING id"},
	{"VALUES keeps the numbering contiguous", "", "statement: INSERT INTO t VALUES (1), (2) ON CONFLICT (a) DO UPDATE SET b = 3",
		"statement: INSERT INTO t VALUES (...) ON CONFLICT (a) DO UPDATE SET b = $1"},
	{"VALUES at the end collapses", "", "statement: insert into t values (1)", "statement: insert into t values (...)"},
	{"VALUES before a semicolon collapses", "", "statement: INSERT INTO t VALUES (1);", "statement: INSERT INTO t VALUES (...);"},
	{"VALUES with a function stays", "", "statement: INSERT INTO t VALUES (1, now())", "statement: INSERT INTO t VALUES ($1, now())"},
	{"VALUES with a bad later tuple stays", "", "statement: INSERT INTO t VALUES (1), (now())",
		"statement: INSERT INTO t VALUES ($1), (now())"},
	{"collapsed IN is a fixed point", "", "statement: SELECT 1 WHERE a IN (...)", "statement: SELECT $1 WHERE a IN (...)"},
	{"collapsed VALUES is a fixed point", "", "statement: INSERT INTO t VALUES (...)", "statement: INSERT INTO t VALUES (...)"},
	{"IN in a message stays", "", "value in (1, 2) is out of range", "value in ($1, $2) is out of range"},
}

// TestAppendTemplateStatements checks REQ-009: the marker stays, the
// prepared statement name and the literals become placeholders, IN and
// VALUES lists of only literals collapse, comments go, white space
// collapses, and keywords and identifiers stay as written.
func TestAppendTemplateStatements(t *testing.T) {
	for _, c := range templateStatementCases {
		t.Run(c.name, func(t *testing.T) {
			r := &Record{SQLState: sqlState(t, c.state), Message: []byte(c.in)}
			got := AppendTemplate([]byte("keep:"), r)
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
		{"comments in statement mode", "a -- x 1\nb /* 1 /* 2 */ 3 */ c --1", true,
			[]tok{{tokComment, "-- x 1"}, {tokComment, "/* 1 /* 2 */ 3 */"}, {tokComment, "--1"}}},
		{"no comments in a message", "a -- b /* c */", false, nil},
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
	inputs := []string{"", " ", "'", `"`, "$", "-", ".", "0x", "1e", "1e+", "E'\\", "U&", "$a", "..0", "-..5",
		"--", "/*", "/*/", "/* /* */", "-- a\r\nb"}
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

// templateIdempotentCases are templates, and messages whose first template
// once read back differently, for TestAppendTemplateIdempotent (CON-002).
// When in is a template, want equals in unless its placeholder numbers are
// out of order.
var templateIdempotentCases = []templateStateCase{
	{"id word before a placeholder", "", "process $1 waits on transaction $2", "process $1 waits on transaction $2"},
	{"key with a placeholder value", "", "user=$1 database=$2", "user=$1 database=$2"},
	{"placeholder before a colon and a path", "", "could not open $1: denied, see /tmp/x", "could not open $1: denied, see $2"},
	{"placeholder before a unit word", "", "took $1 s and $2 ms", "took $1 s and $2 ms"},
	{"placeholder with two digits", "", "a $1 b $2 c $3 d $4 e $5 f $6 g $7 h $8 i $9 j $10 k $11",
		"a $1 b $2 c $3 d $4 e $5 f $6 g $7 h $8 i $9 j $10 k $11"},
	{"placeholder before a digit word", "", "wrote $1 0abc", "wrote $1 0abc"},
	{"placeholders are renumbered", "", "from $7 to $3", "from $1 to $2"},
	{"placeholder in class 23", "23505", "violates unique constraint $1", "violates unique constraint $1"},
	{"placeholder after a for phrase", "22P02", "invalid input syntax for type integer: $1", "invalid input syntax for type integer: $1"},
	{"placeholder in parentheses", "", "wrote $1 buffers ($2); lsn=$3", "wrote $1 buffers ($2); lsn=$3"},
	{"signed placeholder", "", "offset -$1 and +$2", "offset -$1 and +$2"},
	{"quoted data right after a word takes the word", "22023", `abc"x" 5`, "$1 $2"},
	{"literal right after a dollar takes the dollar", "", "$'a' 5", "$1 $2"},
	{"digit word right after a literal joins it", "", "'a'1abc 5", "$1 $2"},
	{"quoted data takes a dollar and a word before it", "22023", `$A00"x" 5`, "$1 $2"},
}

// templateInputs collects every message the template tests use, with its
// SQLSTATE: the golden rows that run and the cases of each unit test. A new
// case added to one of those tables is covered by TestAppendTemplateIdempotent
// without further work.
func templateInputs() []templateStateCase {
	var out []templateStateCase
	for _, c := range templateGolden {
		if c.pending == "" {
			out = append(out, templateStateCase{"golden/" + c.name, c.sqlState, c.message, c.want})
		}
	}
	for _, set := range []struct {
		name  string
		cases []templateCase
	}{
		{"literal", templateLiteralCases},
		{"key-value", templateKeyValueCases},
		{"id", templateIDCases},
		{"path", templatePathCases},
	} {
		for _, c := range set.cases {
			out = append(out, templateStateCase{set.name + "/" + c.name, "", c.in, c.want})
		}
	}
	for _, c := range templateQuotedCases {
		out = append(out, templateStateCase{"quoted/" + c.name, c.state, c.in, c.want})
	}
	for _, c := range templateStatementCases {
		out = append(out, templateStateCase{"statement/" + c.name, c.state, c.in, c.want})
	}
	for _, c := range templateIdempotentCases {
		out = append(out, templateStateCase{"idempotent/" + c.name, c.state, c.in, c.want})
	}
	return out
}

// TestAppendTemplateIdempotent checks CON-002 and AC-005: masking a template
// again, under the same severity and SQLSTATE, returns it unchanged. It runs
// over the template of every input in templateInputs.
func TestAppendTemplateIdempotent(t *testing.T) {
	for _, c := range templateInputs() {
		t.Run(c.name, func(t *testing.T) {
			state := sqlState(t, c.state)
			first := AppendTemplate(nil, &Record{SQLState: state, Message: []byte(c.in)})
			assert.Equal(t, c.want, string(first))
			second := AppendTemplate(nil, &Record{SQLState: state, Message: first})
			assert.Equal(t, string(first), string(second))
		})
	}
}
