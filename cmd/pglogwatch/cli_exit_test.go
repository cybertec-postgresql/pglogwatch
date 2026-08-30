package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Exit codes (IFC-012).
//
// 0 success, 1 usage or I/O error, 2 no input matched. A script wrapping the
// CLI branches on these, so each is pinned separately.

func TestExitCodes(t *testing.T) {
	// IFC-012: 0 success, 1 usage or I/O error, 2 no input matched.
	t.Run("success", func(t *testing.T) {
		code, _, _ := cli(t, "stats")
		assert.Equal(t, exitOK, code)
	})

	t.Run("no input matched", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "not-there.json")
		code, out, errOut := cliWith(t, "", "stats", missing)
		assert.Equal(t, exitNoInput, code)
		assert.Empty(t, out, "a failed run must not emit a partial report")
		assert.Contains(t, errOut, "no input")
	})

	t.Run("unknown command", func(t *testing.T) {
		code, _, errOut := cli(t, "frobnicate")
		assert.Equal(t, exitError, code)
		assert.Contains(t, errOut, "unknown command")
	})

	t.Run("unknown flag", func(t *testing.T) {
		code, _, errOut := cli(t, "stats", "--nonsense")
		assert.Equal(t, exitError, code)
		assert.NotEmpty(t, errOut)
	})

	t.Run("bad flag value", func(t *testing.T) {
		code, _, errOut := cli(t, "stats", "--format", "yaml")
		assert.Equal(t, exitError, code)
		assert.Contains(t, errOut, "--format")
	})

	t.Run("no arguments prints usage", func(t *testing.T) {
		code, _, errOut := cli(t)
		assert.Equal(t, exitError, code)
		assert.Contains(t, errOut, "Usage:")
	})

	t.Run("help succeeds", func(t *testing.T) {
		code, out, _ := cli(t, "help")
		assert.Equal(t, exitOK, code)
		assert.Contains(t, out, "Usage:")
	})
}
