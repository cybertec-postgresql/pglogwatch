package pglogwatch

import (
	"testing"

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
		pending:  "US1/T005",
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
		pending: "US1/T005",
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
