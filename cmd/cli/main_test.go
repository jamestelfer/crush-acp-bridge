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

	_, err := io.WriteString(stdinW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")
	require.NoError(t, err)
	_, err = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":`+strconv.Quote(editorCwd)+`,"mcpServers":[]}}`+"\n")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(srv.openedDirs()) > 0 }, 3*time.Second, 10*time.Millisecond)
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
