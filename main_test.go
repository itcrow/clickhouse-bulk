package main

import (
	"bytes"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCLI_Version(t *testing.T) {
	version = "1.2.3"
	commit = "abc"
	date = "2026-01-01"

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })

	assert.Equal(t, 0, runCLI([]string{"version"}))
	assert.Contains(t, buf.String(), "clickhouse-bulk v1.2.3")
	assert.Contains(t, buf.String(), "commit: abc")
}

func TestRunCLI_ConfigError(t *testing.T) {
	t.Setenv("JOURNAL_DIR", "../etc")
	t.Cleanup(func() { os.Unsetenv("JOURNAL_DIR") })

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })

	code := runCLI([]string{"-config", "non_existent_config.json"})
	require.Equal(t, 1, code)
	assert.Contains(t, buf.String(), "ERROR:")
}

func TestRunCLI_InvalidFlag(t *testing.T) {
	assert.Equal(t, 2, runCLI([]string{"-not-a-flag"}))
}
