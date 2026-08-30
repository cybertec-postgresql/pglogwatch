package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Input selection (IFC-010).
//
// With no path arguments the CLI reads standard input. Without that it
// cannot appear in a pipeline, and the comparative harness in §6.4 pipes
// into it.

func TestReadsStandardInputWithNoPaths(t *testing.T) {
	// IFC-010. Without this the CLI cannot appear in a pipeline, and the
	// benchmark harness in §6.4 pipes into it.
	code, out, errOut := cli(t, "stats")
	assert.Equal(t, exitOK, code)
	assert.NotEmpty(t, out, "reading standard input must produce a report")
	assert.Empty(t, errOut)
}
func TestReadsNamedPaths(t *testing.T) {
	path := writeSample(t)
	code, out, _ := cliWith(t, "", "stats", path)
	assert.Equal(t, exitOK, code)
	assert.Contains(t, out, "ERROR")
}
