package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
)

// fakeCrush answers health checks and records the directories opened and
// the sessions deleted. Its one session, sess-1, never holds messages.
type fakeCrush struct {
	*httptest.Server

	mu      sync.Mutex
	opened  []string
	deleted []string
}

func newFakeCrush(t *testing.T) *fakeCrush {
	t.Helper()
	f := &fakeCrush{}
	f.Server = httptest.NewServer(fakecrush.Handler(fakecrush.Hooks{
		OnOpen: func(path string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.opened = append(f.opened, path)
		},
		OnDelete: func(sid string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.deleted = append(f.deleted, sid)
		},
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCrush) openedDirs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

func (f *fakeCrush) deletedSessions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// runEditor runs the bridge, sends an initialize and a session/new for
// editorCwd, waits for Crush to be asked to open a directory, then
// disconnects the editor.
func runEditor(t *testing.T, srv *fakeCrush, editorCwd string, extraArgs ...string) (code int, stderr string) {
	t.Helper()

	stdinR, stdinW := io.Pipe()
	// The bridge's goroutines may still log after run returns.
	var stdout bytes.Buffer
	var errBuf syncBuffer
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

// An editor probe opens a session and disconnects without prompting it, as
// `paseo provider diagnostic` does.
func TestEditorProbeLeavesNoEmptySessionInCrush(t *testing.T) {
	srv := newFakeCrush(t)

	sid, code, stderr := openSession(t, "--crush-url", srv.URL)

	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	assert.Equal(t, []string{sid}, srv.deletedSessions())
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

// TestMain lets the test binary stand in for the crush executable in managed mode.
func TestMain(m *testing.M) {
	fakecrush.MaybeRunAsServer()
	os.Exit(m.Run())
}

func healthyAt(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestManagedModeRunsAChildServerThatLivesAndDiesWithTheBridge(t *testing.T) {
	t.Setenv(fakecrush.EnvMode, "serve") // inherited by the child
	cache := t.TempDir()
	t.Setenv("CRUSH_CACHE_DIR", cache) // where the child keeps its log
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{
			"crush-acp-bridge", "--managed", "--crush-bin", os.Args[0], "--log-format", "json", "--log-level", "debug",
		}, stdinR, stdoutW, &stderr)
		_ = stdoutW.Close()
	}()
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdoutR)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	go func() {
		_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")
		_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/editor/project","mcpServers":[]}}`+"\n")
	}()

	// Both responses arrive on stdout, and nothing else does.
	var sessionID string
	for range 2 {
		select {
		case line := <-lines:
			var msg struct {
				JSONRPC string `json:"jsonrpc"`
				ID      int    `json:"id"`
				Result  struct {
					SessionID string `json:"sessionId"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal([]byte(line), &msg), "stdout must carry only JSON-RPC: %q", line)
			require.Equal(t, "2.0", msg.JSONRPC)
			if msg.ID == 2 {
				sessionID = msg.Result.SessionID
			}
		case <-time.After(10 * time.Second):
			require.FailNow(t, "no ACP response", "stderr: %s", stderr.String())
		}
	}
	assert.Equal(t, "sess-1", sessionID, "the session came from the child server")

	serverURL := loggedServerURL(t, &stderr)
	require.True(t, healthyAt(serverURL), "the child server is up while the bridge runs")
	running, err := os.ReadDir(cache)
	require.NoError(t, err)
	require.Len(t, running, 1, "the child server keeps a cache directory while it runs")

	require.NoError(t, stdinW.Close())
	select {
	case code := <-done:
		require.Equal(t, exitOK, code, "stderr: %s", stderr.String())
	case <-time.After(10 * time.Second):
		require.FailNow(t, "run did not return after the editor disconnected")
	}
	assert.Eventually(t, func() bool { return !healthyAt(serverURL) }, 5*time.Second, 50*time.Millisecond,
		"the child server must stop with the bridge")
	left, err := os.ReadDir(cache)
	require.NoError(t, err)
	assert.Empty(t, left, "the child server's cache directory goes with it")
}

// loggedServerURL returns the url attribute of the "crush server started" record.
func loggedServerURL(t *testing.T, stderr *syncBuffer) string {
	t.Helper()
	for line := range strings.Lines(stderr.String()) {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "crush server started" {
			url, ok := rec["url"].(string)
			require.True(t, ok)
			return url
		}
	}
	require.FailNow(t, "no \"crush server started\" record", "stderr: %s", stderr.String())
	return ""
}

func TestManagedModeRejectsAnExplicitCrushURL(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--managed", "--crush-url", "http://127.0.0.1:1"},
		strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, exitUsage, code)
	assert.Contains(t, stderr.String(), "--managed")
}

func TestManagedModeReportsAMissingCrushBinary(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"crush-acp-bridge", "--managed", "--crush-bin", "/no/such/crush"},
		strings.NewReader(""), &stdout, &stderr)

	assert.Equal(t, exitFailure, code)
	assert.Contains(t, stderr.String(), "/no/such/crush")
}

// syncBuffer is a goroutine-safe sink for stderr.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestCrushSocketWithoutSharedIsAUsageError(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		code := run([]string{"crush-acp-bridge", "--crush-socket", "/tmp/crush.sock"}, strings.NewReader(""), &stdout, &stderr)

		assert.Equal(t, exitUsage, code)
		assert.Contains(t, stderr.String(), "--shared")
	})
	t.Run("environment", func(t *testing.T) {
		t.Setenv("CRUSH_ACP_CRUSH_SOCKET", "/tmp/crush.sock")
		var stdout, stderr bytes.Buffer

		code := run([]string{"crush-acp-bridge", "--managed", "--crush-bin", "/no/such/crush"}, strings.NewReader(""), &stdout, &stderr)

		assert.Equal(t, exitUsage, code)
		assert.Contains(t, stderr.String(), "CRUSH_ACP_CRUSH_SOCKET")
	})
}

// A signal ends serve's context while the managed child must stay up long
// enough for the bridge to delete the editor's empty sessions through it.
func TestManagedServerOutlivesASignalUntilEmptySessionsAreDeleted(t *testing.T) {
	t.Setenv(fakecrush.EnvMode, "serve") // inherited by the child
	t.Setenv("CRUSH_CACHE_DIR", t.TempDir())
	var stderr syncBuffer
	log := slog.New(slog.NewTextHandler(&stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, signal := context.WithCancel(t.Context())
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() }) // the editor stays connected until the test ends
	stdoutR, stdoutW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, log, serveOptions{managed: true, crushBin: os.Args[0]}, stdinR, stdoutW)
		_ = stdoutW.Close()
	}()
	created := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdoutR)
		for sc.Scan() {
			if strings.Contains(sc.Text(), `"sessionId"`) {
				close(created)
			}
		}
	}()
	go func() {
		_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")
		_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/editor/project","mcpServers":[]}}`+"\n")
	}()
	select {
	case <-created:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no session created", "stderr: %s", stderr.String())
	}

	signal()
	select {
	case err := <-done:
		require.NoError(t, err, "a signal is a normal exit")
	case <-time.After(10 * time.Second):
		require.FailNow(t, "serve did not return after the signal")
	}

	assert.Contains(t, stderr.String(), "empty session deleted")
	// The child's output reaches the log through a pipe, so it may lag.
	assert.Eventually(t, func() bool {
		return strings.Contains(stderr.String(), "fake crush: deleted session sess-1")
	}, 5*time.Second, 10*time.Millisecond, "stderr: %s", stderr.String())
}
