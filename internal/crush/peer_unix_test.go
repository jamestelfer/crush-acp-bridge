//go:build !windows

package crush_test

import (
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// serveUnix serves h on a Unix socket owned by the current user and returns
// its path.
func serveUnix(t *testing.T, h http.Handler) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "crush.sock")
	ln, err := net.Listen("unix", sock) //nolint:noctx // test listener
	require.NoError(t, err)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestClientRefusesAUnixSocketOwnedByAnotherUser(t *testing.T) {
	// Not parallel: PretendOtherUser swaps a package-level hook.
	crush.PretendOtherUser(t)
	t.Setenv("VERTEXAI_PROJECT", "secret-project")

	var requests atomic.Int32
	sock := serveUnix(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))

	_, err := crush.New("unix://"+sock).OpenWorkspace(t.Context(), "/work/proj")

	require.ErrorContains(t, err, "owned by another user")
	require.Zero(t, requests.Load(), "no request, least of all the environment, reaches the server")
}
