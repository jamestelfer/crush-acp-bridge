//go:build windows

package main

import (
	"crypto/rand"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
)

// sharedEnv points the shared server log at a fresh directory and makes a
// started crush serve as the fake.
func sharedEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LocalAppData", t.TempDir())
	t.Setenv(fakecrush.EnvMode, "serve")
}

// defaultAddress returns Crush's default pipe for the current user. Unlike
// the Unix socket, the pipe name cannot be redirected per test, so the test
// uses the real one. A fake left there by an earlier, interrupted run is
// stopped; a real Crush server makes the test skip rather than stop it.
func defaultAddress(t *testing.T) string {
	t.Helper()
	addr := crushserver.DefaultSocket()
	require.NotEmpty(t, addr, "the current user's SID is readable")

	if _, err := fakecrush.Call(t.Context(), addr, http.MethodGet, "/_fake/pid"); err == nil {
		require.NoError(t, fakecrush.Stop(addr), "stop the fake left on %s by an earlier run", addr)
	}
	_, err := fakecrush.Call(t.Context(), addr, http.MethodGet, "/v1/health")
	if err == nil {
		t.Skipf("a Crush server already answers on %s", addr)
	}
	require.ErrorIs(t, err, windows.ERROR_FILE_NOT_FOUND, "probe %s", addr)
	return addr
}

// explicitAddress returns a fresh named pipe other than Crush's default.
func explicitAddress(t *testing.T) string {
	t.Helper()
	return `\\.\pipe\crush-acp-test-` + rand.Text()
}
