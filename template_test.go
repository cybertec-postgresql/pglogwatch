package pglogwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
		want: `duration: $1 plan: …`,
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
	{"French elision is not a prefixed literal", "n'a pas pu ouvrir le fichier « /tmp/x » : 5",
		"n'a pas pu ouvrir le fichier $1 : $2"},
	{"elision before an x is not a prefixed literal", "lu x'abc' et B'1'", "lu x'abc' et B'$1'"},
	{"dollar-quoted literal", "body $q$x$q$ rejected", "body $1 rejected"},
	{"empty dollar tag", "body $$it's$$ rejected", "body $1 rejected"},
	{"apostrophe in a word stays", "can't open 3 files", "can't open $1 files"},
	{"numbering runs left to right", "1 and 'a' and 0x2 and $t$b$t$", "$1 and $2 and $3 and $4"},
	{"digit separators", "limit 1_000_000 rows", "limit $1 rows"},
	{"octal and binary", "flags 0o17 and 0b101", "flags $1 and $2"},
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
	{"quoted WAL file name", "", `restored log file "000000010000000000000042" from archive`,
		`restored log file $1 from archive`},
	{"quoted setting value", "", `parameter "work_mem" changed to "64MB"`, `parameter "work_mem" changed to $1`},
	{"quoted address", "", `listening on IPv6 address "::1", port 5432`, `listening on IPv6 address $1, port $2`},
	{"literal near a syntax error", "42601", `syntax error at or near "'abc'"`, `syntax error at or near $1`},
	{"prefixed literal near a syntax error", "42601", `syntax error at or near "E'abc'"`, `syntax error at or near $1`},
	{"number near a syntax error", "42601", `syntax error at or near "42"`, `syntax error at or near $1`},
	{"dollar quote near a syntax error", "42601", `unterminated dollar-quoted string at or near "$$abc"`,
		`unterminated dollar-quoted string at or near $1`},
	{"keyword near a syntax error stays", "42601", `syntax error at or near "FROM"`, `syntax error at or near "FROM"`},
	{"German quotes in class 22", "22P02", `ungültige Eingabesyntax für Typ integer: »abc«`,
		`ungültige Eingabesyntax für Typ integer: $1`},
	{"French quotes in class 23", "23505", "la valeur d'une clé dupliquée rompt la contrainte unique « users_pkey »",
		"la valeur d'une clé dupliquée rompt la contrainte unique $1"},
	{"German quoted identifier stays", "42P01", `Relation »orders« existiert nicht`, `Relation »orders« existiert nicht`},
	{"German quoted path", "58P01", `konnte Datei »base/1/2« nicht öffnen`, `konnte Datei $1 nicht öffnen`},
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
	{"prefixed literals", "", `statement: SELECT E'a\'b', U&'d\0061', B'101', X'1F', N'n'`,
		"statement: SELECT $1, $2, $3, $4, $5"},
	{"typed literal right after its type", "", "statement: SELECT * FROM t WHERE d = date'2026-01-01'",
		"statement: SELECT * FROM t WHERE d = $1"},
	{"literal right after a keyword", "", "statement: SELECT'x'", "statement: $1"},
	{"digit separators in a statement", "", "statement: SELECT 1_000, 0x1F_FF", "statement: SELECT $1, $2"},
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

// templatePlanCases holds the cases of TestAppendTemplatePlan.
var templatePlanCases = []templateCase{
	{"plan at the end of the message", "duration: 1.5 ms plan:", "duration: $1 plan: …"},
	{"plan text on the same line", "duration: 1.5 ms plan: Seq Scan on t (cost=0.00..1.01 rows=1)", "duration: $1 plan: …"},
	{"plan in JSON format", "duration: 0.2 ms  plan:\n{\"Query Text\": \"SELECT 'secret'\"}", "duration: $1 plan: …"},
	{"plan template is a fixed point", "duration: $1 plan: …", "duration: $1 plan: …"},
	{"plan without a duration stays", "plan: 5 rows", "plan: $1 rows"},
	{"plan after a word stays", "query plan: 5 rows", "query plan: $1 rows"},
	{"plan without a space after the colon stays", "duration: 1 ms plan:x", "duration: $1 plan:x"},
}

// TestAppendTemplatePlan checks REQ-012: after a duration, "plan:" ends the
// template with "plan: …" and no plan text reaches it.
func TestAppendTemplatePlan(t *testing.T) {
	for _, c := range templatePlanCases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
		})
	}
}

// templateCutCases holds the cases of TestAppendTemplateCut. Each message
// puts a different kind of byte at the 1,024-byte limit.
var templateCutCases = func() []templateCase {
	a := strings.Repeat
	cols := "duration: 2.5 ms  statement: SELECT " + a("abcd, ", 800) + "x FROM t"
	// The template prefix "duration: $1 statement: SELECT " is 31 bytes, and
	// 993 bytes is 165 columns of 6 bytes and 3 more, so the cut falls
	// inside a column name.
	colsWant := "duration: $1 statement: SELECT " + a("abcd, ", 165) + "abc…"
	// Eleven numbers become "$1 $2 ... $11 ", 35 bytes. With 986 filler bytes
	// and a space, "$12" takes bytes 1,022 to 1,024 and the cut would leave
	// "$1"; the whole placeholder goes instead, with the space before it. A
	// placeholder that ends at the limit goes too: "…" right after it would
	// join a key=value value on a second pass.
	elevens := a("1 ", 11)
	elevensWant := "$1 $2 $3 $4 $5 $6 $7 $8 $9 $10 $11 "
	return []templateCase{
		{"5,000-byte statement", cols, colsWant},
		{"multibyte character across the limit", a("a", 1023) + "€€ tail", a("a", 1023) + "…"},
		{"multibyte character ending at the limit", a("a", 1021) + "€€ tail", a("a", 1021) + "€…"},
		{"placeholder across the limit", elevens + a("a", 986) + " 1 tail", elevensWant + a("a", 986) + "…"},
		{"placeholder ending at the limit", elevens + a("a", 985) + " 1 tail", elevensWant + a("a", 985) + "…"},
		{"placeholder before the limit", elevens + a("a", 984) + " 1 x tail", elevensWant + a("a", 984) + " $12 …"},
		{"key=value before the limit", a("a", 1015) + " user=bob, x", a("a", 1015) + " user=$1 …"},
		{"key before a masked value", a("a", 1018) + " user=bob tail", a("a", 1018) + " user…"},
		{"slash that is not a path", a("a", 1021) + " / b", a("a", 1021) + "…"},
		{"cut template is a fixed point", colsWant, colsWant},
		{"template of exactly the limit stays", a("a", 1024), a("a", 1024)},
		{"ellipsis after the limit is not counted", a("a", 1024) + "…", a("a", 1024) + "…"},
		{"no word before the limit", a("(", 2000), "…"},
	}
}()

// TestAppendTemplateCut checks REQ-011: a template over 1,024 bytes keeps at
// most 1,024 bytes, cut on a UTF-8 boundary and never inside a placeholder,
// and ends with "…".
func TestAppendTemplateCut(t *testing.T) {
	for _, c := range templateCutCases {
		t.Run(c.name, func(t *testing.T) {
			got := AppendTemplate([]byte("keep:"), &Record{Message: []byte(c.in)})
			assert.Equal(t, "keep:"+c.want, string(got))
			assert.LessOrEqual(t, len(got)-len("keep:"), templateLimit+len(ellipsis))
		})
	}
}

// TestAppendTemplateCutSharesPrefix checks that long statements sharing
// their first kilobyte share a template, whatever comes after it (REQ-011).
func TestAppendTemplateCutSharesPrefix(t *testing.T) {
	head := "duration: 7 ms  statement: SELECT " + strings.Repeat("col_name, ", 200) + "x FROM t "
	one := AppendTemplate(nil, &Record{Message: []byte(head + "WHERE id = 1")})
	two := AppendTemplate(nil, &Record{Message: []byte(head + "ORDER BY 2 LIMIT 'secret'")})
	assert.Equal(t, string(one), string(two))
	assert.True(t, strings.HasSuffix(string(one), "…"))
}

// TestAppendTemplateCutList checks that a VALUES list of only literals that
// starts within the limit is read to its end, so a bulk insert collapses
// like a short one, and that a list which fails past the limit is cut.
func TestAppendTemplateCutList(t *testing.T) {
	rows := strings.Repeat("(1, 'a'), ", 5000)
	msg := "statement: INSERT INTO t VALUES " + rows + "(2, 'b') RETURNING id"
	got := AppendTemplate(nil, &Record{Message: []byte(msg)})
	assert.Equal(t, "statement: INSERT INTO t VALUES (...) RETURNING id", string(got))

	msg = "statement: INSERT INTO t VALUES " + rows + "(now(), 'b')"
	got = AppendTemplate(nil, &Record{Message: []byte(msg)})
	assert.True(t, strings.HasPrefix(string(got), "statement: INSERT INTO t VALUES ($1, $2), ($3, $4), "))
	assert.True(t, strings.HasSuffix(string(got), "…"))
	assert.LessOrEqual(t, len(got), templateLimit+len(ellipsis))
	again := AppendTemplate(nil, &Record{Message: got})
	assert.Equal(t, string(got), string(again))
}

// TestAppendTemplateCutCapacity checks that a huge message never makes dst
// grow past the 1,100 bytes of spare capacity the doc comment promises, and
// that it costs no allocation.
func TestAppendTemplateCutCapacity(t *testing.T) {
	const mib = 1 << 20
	msgs := map[string]string{
		"word":         "statement: SELECT " + strings.Repeat("x", mib),
		"literals":     strings.Repeat("1 ", mib/2),
		"values list":  "statement: INSERT INTO t VALUES " + strings.Repeat("(1), ", mib/5) + "(now())",
		"in list":      "statement: SELECT 1 WHERE a IN (" + strings.Repeat("123, ", mib/5) + "1)",
		"key=value":    strings.Repeat("k=v ", mib/4),
		"comment":      "statement: SELECT /*" + strings.Repeat("x", mib) + "*/ 1",
		"long literal": "statement: SELECT '" + strings.Repeat("x", mib) + "'",
	}
	for name, m := range msgs {
		t.Run(name, func(t *testing.T) {
			r := &Record{Message: []byte(m)}
			dst := make([]byte, 0, 1100)
			allocs.Zero(t, 5, func() { dst = AppendTemplate(dst[:0], r) })
			assert.Equal(t, 1100, cap(dst))
			assert.LessOrEqual(t, len(dst), templateLimit+len(ellipsis))
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
		{"digit separators and radix prefixes", "1_000 0o17 0b101 1__0 0b2 1_", false,
			[]tok{{tokNumber, "1_000"}, {tokHex, "0o17"}, {tokHex, "0b101"}}},
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
		{"guillemets", "»a b« « c » »d", false,
			[]tok{{tokIdent, "»a b«"}, {tokIdent, "« c »"}, {tokIdent, "»d"}}},
		{"quote after a word starts a literal in a statement", "date'2026-01-01'", true,
			[]tok{{tokString, "'2026-01-01'"}}},
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
		{"plan", templatePlanCases},
		{"cut", templateCutCases},
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

// TestPatternID pins the id of two records to hex values computed outside
// Go (printf ... | sha256sum) and checks the hash input layout of REQ-010
// against sha256.Sum256 over a byte string built by hand.
func TestPatternID(t *testing.T) {
	cases := []struct {
		name     string
		severity Severity
		state    string
		message  string
		input    string
		want     string
	}{
		{
			name:     "error with a SQLSTATE",
			severity: SeverityError,
			state:    "23505",
			message:  `duplicate key value violates unique constraint "users_email_key"`,
			input:    "ERROR\x0023505\x001\x00duplicate key value violates unique constraint $1",
			want:     "ed9b5ca2afdcced5",
		},
		{
			name:     "zero SQLSTATE hashes as no bytes",
			severity: SeverityLog,
			message:  "checkpoint starting: time",
			input:    "LOG\x00\x001\x00checkpoint starting: time",
			want:     "1e82b007451cb7f3",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Record{Severity: c.severity, SQLState: sqlState(t, c.state), Message: []byte(c.message)}
			tmpl := AppendTemplate(nil, r)
			id := PatternID(r, tmpl)
			assert.Equal(t, c.want, hex.EncodeToString(id[:]))
			sum := sha256.Sum256([]byte(c.input))
			assert.Equal(t, sum[:8], id[:])
		})
	}
}

// TestPatternIDLongTemplate checks that a template longer than any
// AppendTemplate makes, which takes the streaming path, hashes the same
// input as the short path would.
func TestPatternIDLongTemplate(t *testing.T) {
	r := &Record{Severity: SeverityWarning, SQLState: sqlState(t, "01000")}
	tmpl := []byte(strings.Repeat("x", 5000))
	sum := sha256.Sum256(append([]byte("WARNING\x0001000\x001\x00"), tmpl...))
	id := PatternID(r, tmpl)
	assert.Equal(t, sum[:8], id[:])
}

// TestPatternIDAllocs checks that PatternID of a template AppendTemplate
// made performs no allocation (CON-001).
func TestPatternIDAllocs(t *testing.T) {
	r := &Record{Severity: SeverityError, SQLState: sqlState(t, "42P01"), Message: []byte(`relation "t" does not exist`)}
	tmpl := AppendTemplate(nil, r)
	var id [8]byte
	allocs.Zero(t, 100, func() { id = PatternID(r, tmpl) })
	assert.NotEqual(t, [8]byte{}, id)
}

// TestSameShapeSameID checks AC-003: records that differ only in values
// give the same template and the same pattern id, while a different
// severity, SQLSTATE or template gives a different id.
func TestSameShapeSameID(t *testing.T) {
	same := []struct {
		name, state string
		a, b        string
	}{
		{"different users", "",
			"connection authorized: user=alice database=shop application_name=psql",
			"connection authorized: user=bob database=crm application_name=app"},
		{"different numbers", "",
			"checkpoint complete: wrote 12 buffers (0.1%); 0 WAL file(s) added",
			"checkpoint complete: wrote 4096 buffers (12.5%); 3 WAL file(s) added"},
		{"different literals", "",
			"statement: SELECT * FROM users WHERE name = 'alice' AND id = 1",
			"statement: SELECT * FROM users WHERE name = 'bob' AND id = 99"},
		{"different IN list lengths", "",
			"statement: SELECT * FROM t WHERE id IN (1, 2, 3)",
			"statement: SELECT * FROM t WHERE id IN (7)"},
		{"different durations", "",
			"duration: 12.345 ms  statement: SELECT 1",
			"duration: 0.5 ms  statement: SELECT 42"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			ra := &Record{Severity: SeverityLog, SQLState: sqlState(t, c.state), Message: []byte(c.a)}
			rb := &Record{Severity: SeverityLog, SQLState: sqlState(t, c.state), Message: []byte(c.b)}
			ta, tb := AppendTemplate(nil, ra), AppendTemplate(nil, rb)
			assert.Equal(t, string(ta), string(tb))
			assert.Equal(t, PatternID(ra, ta), PatternID(rb, tb))
		})
	}

	base := &Record{Severity: SeverityError, SQLState: sqlState(t, "42P01"), Message: []byte(`relation "t" does not exist`)}
	tmpl := AppendTemplate(nil, base)
	id := PatternID(base, tmpl)
	t.Run("different severity", func(t *testing.T) {
		r := *base
		r.Severity = SeverityFatal
		assert.NotEqual(t, id, PatternID(&r, tmpl))
	})
	t.Run("different SQLSTATE", func(t *testing.T) {
		r := *base
		r.SQLState = sqlState(t, "42P02")
		assert.NotEqual(t, id, PatternID(&r, tmpl))
	})
	t.Run("zero SQLSTATE", func(t *testing.T) {
		r := *base
		r.SQLState = [5]byte{}
		assert.NotEqual(t, id, PatternID(&r, tmpl))
	})
	t.Run("different template", func(t *testing.T) {
		assert.NotEqual(t, id, PatternID(base, []byte(`relation $1 does not exist`)))
	})
}

// templateCorpusPath is the privacy corpus of AC-002. Its format is described
// in testdata/template/README.md.
const templateCorpusPath = "testdata/template/privacy.txt"

// corpusRecord is one record of the privacy corpus. line is its line number
// in the file, for failure messages.
type corpusRecord struct {
	line     int
	severity Severity
	state    string
	message  string
}

// loadTemplateCorpus reads the privacy corpus and fails the test on a line
// that does not follow the format.
func loadTemplateCorpus(t testing.TB) []corpusRecord {
	t.Helper()
	data, err := os.ReadFile(templateCorpusPath)
	if err != nil {
		t.Fatal(err)
	}
	severities := make(map[string]Severity)
	for s := SeverityDebug5; s <= SeverityPanic; s++ {
		severities[s.String()] = s
	}
	var out []corpusRecord
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 {
			t.Fatalf("%s:%d: want SEVERITY SQLSTATE message", templateCorpusPath, n)
		}
		sev, ok := severities[fields[0]]
		if !ok {
			t.Fatalf("%s:%d: unknown severity %q", templateCorpusPath, n, fields[0])
		}
		state := fields[1]
		if state == "-" {
			state = ""
		} else if len(state) != 5 {
			t.Fatalf("%s:%d: SQLSTATE %q is not five characters", templateCorpusPath, n, state)
		}
		msg, err := unescapeCorpus(fields[2])
		if err != nil {
			t.Fatalf("%s:%d: %v", templateCorpusPath, n, err)
		}
		msg = strings.ReplaceAll(msg, "{email}", templateCorpusEmail)
		out = append(out, corpusRecord{line: n, severity: sev, state: state, message: msg})
	}
	return out
}

// unescapeCorpus turns the \n, \t and \\ escapes of a corpus message into the
// bytes they stand for.
func unescapeCorpus(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i == len(s) {
			return "", errors.New("backslash at the end of the line")
		}
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		default:
			return "", fmt.Errorf("unknown escape \\%c", s[i])
		}
	}
	return b.String(), nil
}

// templateCorpusEmail is the e-mail marker that {email} in the corpus stands
// for. It lives here because TestFixturesCarryNoRealIdentifiers rejects an
// e-mail address written into a fixture.
const templateCorpusEmail = "secret4@example.com"

// templateMarkers are the values seeded into the privacy corpus. None of them
// may appear in a template. "SECRET" also catches a value masked only in
// part, such as "SECRET-1" written back as "SECRET-$1", and "secret4" a
// local part left behind by a masked domain.
var templateMarkers = []string{"SECRET", "SECRET-1", "424242", "SECRET-2", "SECRET-3", templateCorpusEmail, "secret4"}

// TestTemplatePrivacy checks AC-002: no marker seeded into the privacy corpus
// survives in its template. It also checks CON-002 over the corpus: masking
// a template again changes nothing.
func TestTemplatePrivacy(t *testing.T) {
	corpus := loadTemplateCorpus(t)
	if len(corpus) < 200 {
		t.Fatalf("the corpus has %d records, AC-002 asks for at least 200", len(corpus))
	}
	for _, c := range corpus {
		r := &Record{Severity: c.severity, SQLState: sqlState(t, c.state), Message: []byte(c.message)}
		tmpl := AppendTemplate(nil, r)
		seeded := false
		for _, m := range templateMarkers {
			if strings.Contains(c.message, m) {
				seeded = true
			}
			if strings.Contains(string(tmpl), m) {
				t.Errorf("%s:%d: %q survives in %q", templateCorpusPath, c.line, m, tmpl)
			}
		}
		if !seeded {
			t.Errorf("%s:%d: the message carries no marker", templateCorpusPath, c.line)
		}
		again := AppendTemplate(nil, &Record{Severity: c.severity, SQLState: r.SQLState, Message: tmpl})
		if string(again) != string(tmpl) {
			t.Errorf("%s:%d: template %q masks again to %q", templateCorpusPath, c.line, tmpl, again)
		}
	}
}
