package crushserver_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
)

// The shared server tests run on every platform: against Crush's default
// Unix socket, or a named pipe of their own on Windows (see sharedTarget).

// fakeCall sends a request to a fake server's debug endpoint.
func fakeCall(t *testing.T, addr, method, path string) string {
	t.Helper()
	body, err := fakecrush.Call(t.Context(), addr, method, path)
	require.NoError(t, err)
	return body
}

// stopShared stops a detached fake server, which outlives the test otherwise.
func stopShared(t *testing.T, addr string) {
	t.Helper()
	t.Cleanup(func() { assert.NoError(t, fakecrush.Stop(addr)) })
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

func TestDefaultPipeFollowsCrushsRule(t *testing.T) {
	sid := "S-1-5-21-1004336348-1177238915-682003330-1001"
	assert.Equal(t, "//./pipe/crush-"+sid+".sock", crushserver.DefaultPipe(sid))
	assert.Empty(t, crushserver.DefaultPipe(""), "an unknown user gets no pipe rather than one every user shares")
}

func TestSharedURLUsesCrushsSchemeForThePlatform(t *testing.T) {
	if runtime.GOOS == "windows" {
		assert.Equal(t, "npipe:////./pipe/crush-S-1-5-21.sock", crushserver.SharedURL("//./pipe/crush-S-1-5-21.sock"))
		return
	}
	assert.Equal(t, "unix:///run/user/1000/crush-1000.sock", crushserver.SharedURL("/run/user/1000/crush-1000.sock"))
}

func TestEnsureSharedStartsADetachedServerThatOutlivesTheBridge(t *testing.T) {
	opts, addr := sharedTarget(t)
	stopShared(t, addr)

	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, crushserver.EnsureShared(ctx, opts))
	cancel()

	time.Sleep(200 * time.Millisecond)
	require.NotEmpty(t, fakeCall(t, addr, http.MethodGet, "/_fake/pid"), "the server keeps running after the bridge lets go")
	require.Empty(t, fakeCall(t, addr, http.MethodGet, "/_fake/env?name=CRUSH_SERVER_IDLE_TIMEOUT"),
		"a shared server keeps Crush's idle timeout, so it exits once every client leaves")
}

func TestEnsureSharedReusesARunningServer(t *testing.T) {
	opts, addr := sharedTarget(t)
	ln := listenAt(t, addr)
	srv := &http.Server{Handler: fakecrush.Handler(fakecrush.Hooks{}), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	opts.Binary = filepath.Join(t.TempDir(), "no-such-crush")
	require.NoError(t, crushserver.EnsureShared(t.Context(), opts), "nothing is started when a server answers")
}

func TestBridgesStartingTogetherShareOneServer(t *testing.T) {
	opts, addr := sharedTarget(t)
	stopShared(t, addr)

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() { errs[i] = crushserver.EnsureShared(t.Context(), opts) })
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err, "a server that loses the race for the address exits; the bridges use the winner")
	}
	require.NotEmpty(t, fakeCall(t, addr, http.MethodGet, "/_fake/pid"))
}

func TestEnsureSharedFailsWhenNoServerComesUp(t *testing.T) {
	opts, _ := sharedTarget(t)
	t.Setenv(fakecrush.EnvMode, "exit")
	opts.StartTimeout = time.Second

	err := crushserver.EnsureShared(t.Context(), opts)

	require.ErrorContains(t, err, "not ready")
	require.ErrorContains(t, err, opts.LogFile, "the error points at the server's log")
	logged, readErr := os.ReadFile(opts.LogFile)
	require.NoError(t, readErr)
	require.Contains(t, string(logged), "refusing to start")
}
