package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Output streams (IFC-011).
//
// --output json emits NDJSON on stdout and every diagnostic on stderr. One
// stray line on stdout makes the output unparseable for whatever is
// consuming it, which is the entire point of separating the streams.

func TestJSONOutputGoesToStdoutOnly(t *testing.T) {
	// IFC-011: --output json emits NDJSON on stdout, and every diagnostic
	// goes to stderr. A single stray line on stdout makes the output
	// unparseable for whatever is consuming it.
	code, out, errOut := cli(t, "stats", "--output", "json")
	require.Equal(t, exitOK, code)
	assert.Empty(t, errOut, "diagnostics must not reach stdout's stream")

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		require.NotEmpty(t, line)
		assert.True(t, strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}"),
			"every line of --output json must be a JSON object, got %q", line)
	}
}
func TestJSONOutputIsOneObjectPerLine(t *testing.T) {
	code, out, _ := cli(t, "errors", "--output", "json")
	require.Equal(t, exitOK, code)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.NotEmpty(t, lines)
	for _, line := range lines {
		assert.NotContains(t, line[1:len(line)-1], "\n",
			"NDJSON must not wrap an object across lines")
	}
}
