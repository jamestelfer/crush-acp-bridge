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
)

// The --shared tests run on every platform: against Crush's default Unix
// socket in a fresh directory, or Crush's default named pipe on Windows. The
// helpers sharedEnv, defaultAddress, explicitAddress and dialAddress are per
// platform.

func addressClient(addr string) *http.Client {
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dialAddress(ctx, addr) },
	}}
}

func socketGet(addr, method, path string) (string, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, "http://crush"+path, nil)
	if err != nil {
		return "", err
	}
	resp, err := addressClient(addr).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// stopShared stops a detached fake server at addr when the test ends, since
// it outlives the test otherwise.
func stopShared(t *testing.T, addr string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = socketGet(addr, http.MethodPost, "/_fake/exit")
		time.Sleep(100 * time.Millisecond)
	})
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

	pid, err := socketGet(addr, http.MethodGet, "/_fake/pid")
	require.NoError(t, err, "the server outlives the bridge that started it")

	sid, code, stderr = openSession(t, "--shared", "--crush-bin", filepath.Join(t.TempDir(), "no-crush"))
	require.Equal(t, exitOK, code, "a second bridge joins without starting anything; stderr: %s", stderr)
	require.Equal(t, "sess-1", sid)
	again, err := socketGet(addr, http.MethodGet, "/_fake/pid")
	require.NoError(t, err)
	require.Equal(t, pid, again, "both bridges used one server")
}

func TestSharedModeHonoursAnExplicitSocket(t *testing.T) {
	sharedEnv(t)
	addr := explicitAddress(t)
	stopShared(t, addr)

	_, code, stderr := openSession(t, "--shared", "--crush-bin", os.Args[0], "--crush-socket", addr)

	require.Equal(t, exitOK, code, "stderr: %s", stderr)
	_, err := socketGet(addr, http.MethodGet, "/_fake/pid")
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
