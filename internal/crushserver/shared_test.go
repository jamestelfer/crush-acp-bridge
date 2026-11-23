//go:build !windows

package crushserver_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
)

// runtimeDir points Crush's default socket at a fresh, short directory.
func runtimeDir(t *testing.T) string {
	t.Helper()
	dir := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return dir
}

func unixClient(sock string) *http.Client {
	var d net.Dialer
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "unix", sock) },
	}}
}

// fakeCall sends a request to a fake server's debug endpoint.
func fakeCall(t *testing.T, sock, method, path string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, "http://crush"+path, nil)
	require.NoError(t, err)
	resp, err := unixClient(sock).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// stopShared stops a detached fake server, which outlives the test otherwise.
func stopShared(t *testing.T, sock string) {
	t.Helper()
	t.Cleanup(func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://crush/_fake/exit", nil)
		if resp, err := unixClient(sock).Do(req); err == nil {
			_ = resp.Body.Close()
		}
	})
}

func sharedOptions(t *testing.T) crushserver.SharedOptions {
	t.Helper()
	t.Setenv(fakecrush.EnvMode, "serve")
	return crushserver.SharedOptions{
		Binary:       os.Args[0],
		LogFile:      filepath.Join(t.TempDir(), "server.log"),
		StartTimeout: 10 * time.Second,
	}
}

func TestDefaultSocketFollowsCrushsRule(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())

	t.Setenv("XDG_RUNTIME_DIR", "/run/user/test")
	require.Equal(t, "/run/user/test/crush-"+uid+".sock", crushserver.DefaultSocket())

	t.Setenv("XDG_RUNTIME_DIR", "")
	require.Equal(t, filepath.Join(os.TempDir(), "crush-"+uid+".sock"), crushserver.DefaultSocket())

	long := "/" + strings.Repeat("d", 100)
	t.Setenv("XDG_RUNTIME_DIR", long)
	require.Equal(t, "/tmp/crush-"+uid+".sock", crushserver.DefaultSocket(),
		"past 104 bytes the socket moves to /tmp so it stays bindable")
}

func TestEnsureSharedStartsADetachedServerThatOutlivesTheBridge(t *testing.T) {
	runtimeDir(t)
	sock := crushserver.DefaultSocket()
	stopShared(t, sock)

	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, crushserver.EnsureShared(ctx, sharedOptions(t)))
	cancel()

	time.Sleep(200 * time.Millisecond)
	require.NotEmpty(t, fakeCall(t, sock, http.MethodGet, "/_fake/pid"), "the server keeps running after the bridge lets go")
	require.Empty(t, fakeCall(t, sock, http.MethodGet, "/_fake/env?name=CRUSH_SERVER_IDLE_TIMEOUT"),
		"a shared server keeps Crush's idle timeout, so it exits once every client leaves")
}

func TestEnsureSharedReusesARunningServer(t *testing.T) {
	runtimeDir(t)
	sock := crushserver.DefaultSocket()
	ln, err := net.Listen("unix", sock) //nolint:noctx // test listener
	require.NoError(t, err)
	srv := &http.Server{Handler: fakecrush.Handler(nil), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	opts := sharedOptions(t)
	opts.Binary = filepath.Join(t.TempDir(), "no-such-crush")
	require.NoError(t, crushserver.EnsureShared(t.Context(), opts), "nothing is started when a server answers")
}

func TestBridgesStartingTogetherShareOneServer(t *testing.T) {
	runtimeDir(t)
	sock := crushserver.DefaultSocket()
	stopShared(t, sock)
	opts := sharedOptions(t)

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() { errs[i] = crushserver.EnsureShared(t.Context(), opts) })
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err, "a server that loses the race for the socket exits; the bridges use the winner")
	}
	require.NotEmpty(t, fakeCall(t, sock, http.MethodGet, "/_fake/pid"))
}

func TestEnsureSharedHonoursAnExplicitSocket(t *testing.T) {
	runtimeDir(t)
	sock := filepath.Join(shortDir(t), "other.sock")
	stopShared(t, sock)
	opts := sharedOptions(t)
	opts.Socket = sock

	require.NoError(t, crushserver.EnsureShared(t.Context(), opts))

	require.NotEmpty(t, fakeCall(t, sock, http.MethodGet, "/_fake/pid"))
	_, err := os.Stat(crushserver.DefaultSocket())
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEnsureSharedFailsWhenNoServerComesUp(t *testing.T) {
	runtimeDir(t)
	opts := sharedOptions(t)
	t.Setenv(fakecrush.EnvMode, "exit")
	opts.StartTimeout = time.Second

	err := crushserver.EnsureShared(t.Context(), opts)

	require.ErrorContains(t, err, "not ready")
	require.ErrorContains(t, err, opts.LogFile, "the error points at the server's log")
	logged, readErr := os.ReadFile(opts.LogFile)
	require.NoError(t, readErr)
	require.Contains(t, string(logged), "refusing to start")
}

// shortDir returns a fresh directory whose paths fit in a Unix socket address.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cab")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
