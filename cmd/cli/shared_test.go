//go:build !windows

package main

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
)

// sharedEnv points Crush's default socket at a fresh directory and stops any
// detached fake server the test leaves behind.
func sharedEnv(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cab")
	require.NoError(t, err)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // the shared server log
	t.Setenv(fakecrush.EnvMode, "serve")
	sock := crushserver.DefaultSocket()
	t.Cleanup(func() {
		_, _ = socketGet(sock, http.MethodPost, "/_fake/exit")
		time.Sleep(100 * time.Millisecond)
		_ = os.RemoveAll(dir)
	})
	return sock
}

func socketGet(sock, method, path string) (string, error) {
	var d net.Dialer
	client := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "unix", sock) },
	}}
	req, err := http.NewRequestWithContext(context.Background(), method, "http://crush"+path, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
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
	sock := sharedEnv(t)

	sid, code, stderr := openSession(t, "--shared", "--crush-bin", os.Args[0])
	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	require.Equal(t, "sess-1", sid)

	pid, err := socketGet(sock, http.MethodGet, "/_fake/pid")
	require.NoError(t, err, "the server outlives the bridge that started it")

	sid, code, stderr = openSession(t, "--shared", "--crush-bin", filepath.Join(t.TempDir(), "no-crush"))
	require.Equal(t, exitOK, code, "a second bridge joins without starting anything; stderr: %s", stderr)
	require.Equal(t, "sess-1", sid)
	again, err := socketGet(sock, http.MethodGet, "/_fake/pid")
	require.NoError(t, err)
	require.Equal(t, pid, again, "both bridges used one server")
}

func TestSharedModeHonoursAnExplicitSocket(t *testing.T) {
	sharedEnv(t)
	dir, err := os.MkdirTemp("", "cab")
	require.NoError(t, err)
	sock := filepath.Join(dir, "mine.sock")
	t.Cleanup(func() {
		_, _ = socketGet(sock, http.MethodPost, "/_fake/exit")
		time.Sleep(100 * time.Millisecond)
		_ = os.RemoveAll(dir)
	})

	_, code, stderr := openSession(t, "--shared", "--crush-bin", os.Args[0], "--crush-socket", sock)

	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	_, err = socketGet(sock, http.MethodGet, "/_fake/pid")
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
