package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI behaviour (§4.8, IFC-010 through IFC-012).
//
// Every case drives run() in-process rather than spawning a binary. That is
// what makes it possible to assert on stdout and stderr SEPARATELY, which
// IFC-011 requires, and to check exit codes without parsing process output.

// sampleLog is a small jsonlog corpus covering the shapes the reports need:
// a slow statement, errors, a connection, a checkpoint, a lock wait and a
// temp file.
const sampleLog = `{"timestamp":"2026-08-30 10:00:00.000 UTC","error_severity":"LOG","pid":1,"user":"app","dbname":"appdb","application_name":"psql","remote_host":"10.0.0.5","message":"connection authorized: user=app database=appdb"}
{"timestamp":"2026-08-30 10:00:01.000 UTC","error_severity":"LOG","pid":1,"user":"app","dbname":"appdb","message":"duration: 1500.000 ms  statement: SELECT * FROM orders"}
{"timestamp":"2026-08-30 10:00:02.000 UTC","error_severity":"ERROR","state_code":"42P01","pid":1,"user":"app","dbname":"appdb","message":"relation \"nope\" does not exist","statement":"SELECT * FROM nope"}
{"timestamp":"2026-08-30 10:00:03.000 UTC","error_severity":"ERROR","state_code":"42P01","pid":2,"user":"app","dbname":"appdb","message":"relation \"nope\" does not exist","statement":"SELECT * FROM nope"}
{"timestamp":"2026-08-30 10:00:04.000 UTC","error_severity":"WARNING","pid":2,"message":"there is already a transaction in progress"}
{"timestamp":"2026-08-30 10:10:00.000 UTC","error_severity":"LOG","pid":3,"backend_type":"checkpointer","message":"checkpoint starting: time"}
{"timestamp":"2026-08-30 10:10:05.000 UTC","error_severity":"LOG","pid":3,"backend_type":"checkpointer","message":"checkpoint complete: wrote 512 buffers"}
{"timestamp":"2026-08-30 10:10:06.000 UTC","error_severity":"LOG","pid":4,"dbname":"appdb","message":"process 4 still waiting for ShareLock on transaction 42 after 1000.000 ms"}
{"timestamp":"2026-08-30 10:10:07.000 UTC","error_severity":"ERROR","state_code":"40P01","pid":4,"dbname":"appdb","message":"deadlock detected"}
{"timestamp":"2026-08-30 10:10:08.000 UTC","error_severity":"LOG","pid":5,"dbname":"appdb","message":"temporary file: path \"base/pgsql_tmp/x\", size 1048576"}
{"timestamp":"2026-08-30 10:10:09.000 UTC","error_severity":"LOG","pid":6,"backend_type":"autovacuum worker","message":"automatic vacuum of table \"appdb.public.orders\": index scans: 1"}
{"timestamp":"2026-08-30 10:10:10.000 UTC","error_severity":"FATAL","pid":7,"message":"the database system is shutting down"}
`

// cli runs the CLI with the sample log on standard input.
func cli(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return cliWith(t, sampleLog, args...)
}

func cliWith(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := run(args, strings.NewReader(input), &out, &errBuf)
	return code, out.String(), errBuf.String()
}

// writeSample puts the sample log in a temporary file and returns its path.
func writeSample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "postgresql.json")
	require.NoError(t, os.WriteFile(path, []byte(sampleLog), 0o600))
	return path
}

func TestEveryCommandRuns(t *testing.T) {
	// A smoke test over the whole §4.8 table: each command must accept the
	// sample log and produce something, in both output modes.
	for _, name := range commandNames() {
		for _, mode := range []string{"text", "json"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				args := []string{name, "--output", mode}
				if name == "grep" {
					args = append(args, "relation")
				}
				code, out, errOut := cli(t, args...)
				assert.Equal(t, exitOK, code, "stderr: %s", errOut)
				assert.NotEmpty(t, strings.TrimSpace(out),
					"%s produced no output", name)
			})
		}
	}
}
func TestGlobalFlagsAreAccepted(t *testing.T) {
	// §4.8's global flag list, exercised so a rename or a typo fails here
	// rather than in someone's script.
	code, _, errOut := cli(t, "stats",
		"--format", "jsonlog",
		"--lang", "en",
		"--begin", "2026-08-30",
		"--end", "2026-08-31",
		"--jobs", "2",
		"--output", "text",
		"--no-color",
	)
	assert.Equal(t, exitOK, code, "stderr: %s", errOut)
}
func TestTimeWindowFiltersRecords(t *testing.T) {
	// --begin and --end must actually filter, not merely parse.
	_, all, _ := cli(t, "stats")
	_, windowed, _ := cli(t, "stats", "--begin", "2026-08-30 10:10:00")
	assert.NotEqual(t, all, windowed,
		"a time window that excludes records must change the report")
}
func TestFormatOverrideIsHonoured(t *testing.T) {
	// A wrong --format must produce a wrong answer rather than being
	// quietly corrected: the flag exists to override detection.
	code, out, _ := cli(t, "stats", "--format", "csvlog")
	require.Equal(t, exitOK, code)
	assert.NotContains(t, out, "ERROR       3",
		"jsonlog read as csvlog must not parse correctly")
}
