package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunPrintsGreetingAndSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge"}, &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.Equal(t, "hello from crush-acp-bridge\n", stdout.String())
}

func TestRunVersionFlagPrintsVersionAndSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--version"}, &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.NotEmpty(t, strings.TrimSpace(stdout.String()))
	assert.Contains(t, stdout.String(), buildVersion())
}

func TestRunHelpFlagPrintsUsageAndSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--help"}, &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.Contains(t, stdout.String(), "crush-acp-bridge")
}

func TestRunRejectsPositionalArgumentsAsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "extra"}, &stdout, &stderr)

	assert.Equal(t, exitUsage, code)
	assert.Contains(t, stderr.String(), "takes no arguments")
}

func TestRunRejectsUnknownFlagAsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--no-such-flag"}, &stdout, &stderr)

	assert.Equal(t, exitUsage, code)
}
