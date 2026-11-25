package main

import (
	"bufio"
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
)

// The --shared tests run on every platform: against Crush's default Unix
// socket in a fresh directory, or Crush's default named pipe on Windows. The
// helpers sharedEnv, defaultAddress and explicitAddress are per platform.

// stopShared stops a detached fake server at addr when the test ends, since
// it outlives the test otherwise.
func stopShared(t *testing.T, addr string) {
	t.Helper()
	t.Cleanup(func() { assert.NoError(t, fakecrush.Stop(addr)) })
}

// openSession runs the bridge with args, creates one session, disconnects,
// and returns the session ID and exit code.
func openSession(t *testing.T, args ...string) (string, int, string) {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(append([]string{"crush-acp-bridge"}, args...), stdinR, stdoutW, &stderr)
		_ = stdoutW.Close()
	}()
	go func() {
		_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`+"\n")
		_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/editor/project","mcpServers":[]}}`+"\n")
	}()

	ids := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdoutR)
		for sc.Scan() {
			var msg struct {
				ID     int `json:"id"`
				Result struct {
					SessionID string `json:"sessionId"`
				} `json:"result"`
			}
			if json.Unmarshal(sc.Bytes(), &msg) == nil && msg.ID == 2 {
				ids <- msg.Result.SessionID
			}
		}
	}()

	var sid string
	select {
	case sid = <-ids:
	case code := <-done:
		return "", code, stderr.String()
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no session created", "stderr: %s", stderr.String())
	}
	require.NoError(t, stdinW.Close())
	select {
	case code := <-done:
		return sid, code, stderr.String()
	case <-time.After(10 * time.Second):
		require.FailNow(t, "run did not return after the editor disconnected")
	}
	return "", 0, ""
}

func TestSharedModeStartsCrushsServerAndLeavesItForOtherClients(t *testing.T) {
	sharedEnv(t)
	addr := defaultAddress(t)
	stopShared(t, addr)

	sid, code, stderr := openSession(t, "--shared", "--crush-bin", os.Args[0])
	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	require.Equal(t, "sess-1", sid)

	pid, err := fakecrush.Call(t.Context(), addr, http.MethodGet, "/_fake/pid")
	require.NoError(t, err, "the server outlives the bridge that started it")

	sid, code, stderr = openSession(t, "--shared", "--crush-bin", filepath.Join(t.TempDir(), "no-crush"))
	require.Equal(t, exitOK, code, "a second bridge joins without starting anything; stderr: %s", stderr)
	require.Equal(t, "sess-1", sid)
	again, err := fakecrush.Call(t.Context(), addr, http.MethodGet, "/_fake/pid")
	require.NoError(t, err)
	require.Equal(t, pid, again, "both bridges used one server")
}

func TestSharedModeHonoursAnExplicitSocket(t *testing.T) {
	sharedEnv(t)
	addr := explicitAddress(t)
	stopShared(t, addr)

	_, code, stderr := openSession(t, "--shared", "--crush-bin", os.Args[0], "--crush-socket", addr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	_, err := fakecrush.Call(t.Context(), addr, http.MethodGet, "/_fake/pid")
	require.NoError(t, err)
}

func TestSharedModeConflictsWithOtherServerChoices(t *testing.T) {
	sharedEnv(t)
	for _, args := range [][]string{
		{"--shared", "--managed"},
		{"--shared", "--crush-url", "http://127.0.0.1:1"},
	} {
		_, code, stderr := openSession(t, args...)
		require.Equal(t, exitUsage, code, "%v: stderr: %s", args, stderr)
	}
}
