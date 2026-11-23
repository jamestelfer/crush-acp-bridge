package main

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCrush answers health checks and records the directories opened.
type fakeCrush struct {
	*httptest.Server

	mu     sync.Mutex
	opened []string
}

func newFakeCrush(t *testing.T) *fakeCrush {
	t.Helper()
	f := &fakeCrush{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("POST /v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		_ = json.UnmarshalRead(r.Body, &body)
		f.mu.Lock()
		f.opened = append(f.opened, body.Path)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"ws-1"}`))
	})
	mux.HandleFunc("GET /v1/workspaces/ws-1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("POST /v1/workspaces/ws-1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"sess-1"}`))
	})
	mux.HandleFunc("POST /v1/workspaces/ws-1/current-session", func(http.ResponseWriter, *http.Request) {})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCrush) openedDirs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

// runEditor runs the bridge, sends an initialize and a session/new for
// editorCwd, waits for Crush to be asked to open a directory, then
// disconnects the editor.
func runEditor(t *testing.T, srv *fakeCrush, editorCwd string, extraArgs ...string) (code int, stderr string) {
	t.Helper()

	stdinR, stdinW := io.Pipe()
	var stdout, errBuf bytes.Buffer
	done := make(chan int, 1)
	go func() {
		args := append([]string{"crush-acp-bridge", "--crush-url", srv.URL}, extraArgs...)
		done <- run(args, stdinR, &stdout, &errBuf)
	}()

	// run may exit without reading stdin (a usage error), so write from a
	// goroutine and let a closed reader release it.
	go func() {
		_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")
		_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":`+strconv.Quote(editorCwd)+`,"mcpServers":[]}}`+"\n")
	}()

	deadline := time.After(3 * time.Second)
	for len(srv.openedDirs()) == 0 {
		select {
		case code = <-done: // exited early, for example on a usage error
			_ = stdinR.Close()
			return code, errBuf.String()
		case <-deadline:
			require.FailNow(t, "Crush was never asked to open a directory")
		case <-time.After(10 * time.Millisecond):
		}
	}
	require.NoError(t, stdinW.Close())

	select {
	case code = <-done:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "run did not return after the editor disconnected")
	}
	return code, errBuf.String()
}

func TestRunOpensCrushInTheDirectoryTheEditorSends(t *testing.T) {
	srv := newFakeCrush(t)

	code, stderr := runEditor(t, srv, "/editor/project")

	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	assert.Equal(t, []string{"/editor/project"}, srv.openedDirs())
}

func TestRunCwdFlagOverridesTheEditorsDirectory(t *testing.T) {
	srv := newFakeCrush(t)

	code, stderr := runEditor(t, srv, "/editor/project", "--cwd", "/forced/project")

	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	assert.Equal(t, []string{"/forced/project"}, srv.openedDirs())
}

func TestRunReportsUnreachableCrushServer(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--crush-url", "http://127.0.0.1:1"},
		strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, exitFailure, code)
	assert.Contains(t, stderr.String(), "is `crush server` running")
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

func TestRunLogsStructuredJSONToStderrOnly(t *testing.T) {
	srv := newFakeCrush(t)

	code, stderr := runEditor(t, srv, "/editor/project", "--log-format", "json", "--log-level", "debug")

	require.Equal(t, exitOK, code)
	var opened map[string]any
	for line := range strings.Lines(stderr) {
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), "every stderr line is a JSON record: %q", line)
		if rec["msg"] == "workspace opened" {
			opened = rec
		}
	}
	require.NotNil(t, opened, "stderr: %s", stderr)
	assert.Equal(t, "/editor/project", opened["cwd"])
	assert.Equal(t, "INFO", opened["level"])
}

func TestRunLogLevelFiltersRecords(t *testing.T) {
	srv := newFakeCrush(t)

	code, stderr := runEditor(t, srv, "/editor/project", "--log-level", "error")

	require.Equal(t, exitOK, code)
	assert.NotContains(t, stderr, "workspace opened")
}

func TestRunRejectsUnknownLogLevelAndFormat(t *testing.T) {
	for _, flag := range [][]string{{"--log-level", "loud"}, {"--log-format", "xml"}} {
		var stdout, stderr bytes.Buffer

		code := run(append([]string{"crush-acp-bridge"}, flag...), strings.NewReader(""), &stdout, &stderr)

		assert.Equal(t, exitUsage, code, "%v", flag)
		assert.Contains(t, stderr.String(), flag[0])
	}
}
