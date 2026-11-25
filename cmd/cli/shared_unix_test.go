//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
)

// sharedEnv points Crush's default socket and the shared server log at fresh
// directories, and makes a started crush serve as the fake.
func sharedEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv(fakecrush.EnvMode, "serve")
}

// defaultAddress returns Crush's default socket, which sharedEnv moved into
// a fresh directory. It fails rather than return the user's real socket,
// which DefaultSocket falls back to when the fresh path is too long to bind.
func defaultAddress(t *testing.T) string {
	t.Helper()
	addr := crushserver.DefaultSocket()
	require.Equal(t, os.Getenv("XDG_RUNTIME_DIR"), filepath.Dir(addr),
		"the default socket is in the test's directory; shorten TMPDIR if it is not")
	return addr
}

// explicitAddress returns a socket path other than Crush's default.
func explicitAddress(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortDir(t), "mine.sock")
}

// shortDir returns a fresh directory whose paths fit in a Unix socket
// address, which t.TempDir's often do not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cab")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
