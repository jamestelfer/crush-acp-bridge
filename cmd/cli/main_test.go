package main

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunServesBridgeAndExitsWhenEditorDisconnects(t *testing.T) {
	var opened string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces" {
			var body struct {
				Path string `json:"path"`
			}
			_ = json.UnmarshalRead(r.Body, &body)
			opened = body.Path
			_, _ = w.Write([]byte(`{"id":"ws-1"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--crush-url", srv.URL, "--cwd", "/work/proj"},
		strings.NewReader(""), &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.Equal(t, "/work/proj", opened)
	assert.Empty(t, stdout.String(), "stdout carries ACP traffic only")
}

func TestRunReportsUnreachableCrushServer(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--crush-url", "http://127.0.0.1:1", "--cwd", "/work/proj"},
		strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, exitFailure, code)
	assert.Contains(t, stderr.String(), "open workspace")
}

func TestRunVersionFlagPrintsVersionAndSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--version"}, strings.NewReader(""), &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.NotEmpty(t, strings.TrimSpace(stdout.String()))
	assert.Contains(t, stdout.String(), buildVersion())
}

func TestRunHelpFlagPrintsUsageAndSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--help"}, strings.NewReader(""), &stdout, &stderr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	assert.Contains(t, stdout.String(), "crush-acp-bridge")
}

func TestRunRejectsPositionalArgumentsAsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "extra"}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, exitUsage, code)
	assert.Contains(t, stderr.String(), "takes no arguments")
}

func TestRunRejectsUnknownFlagAsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--no-such-flag"}, strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, exitUsage, code)
}
