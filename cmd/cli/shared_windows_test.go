//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"net"
	"net/http"
	"testing"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/require"

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
// uses the real one, and skips when a server already answers there rather
// than stopping it.
func defaultAddress(t *testing.T) string {
	t.Helper()
	addr := crushserver.DefaultSocket()
	require.NotEmpty(t, addr, "the current user's SID is readable")
	if _, err := socketGet(addr, http.MethodGet, "/v1/health"); err == nil {
		t.Skipf("a Crush server already answers on %s", addr)
	}
	return addr
}

// explicitAddress returns a fresh named pipe other than Crush's default.
func explicitAddress(t *testing.T) string {
	t.Helper()
	return `\\.\pipe\crush-acp-test-` + rand.Text()
}

func dialAddress(ctx context.Context, addr string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, addr)
}
