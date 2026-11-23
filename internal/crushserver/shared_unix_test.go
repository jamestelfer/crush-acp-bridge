//go:build !windows

package crushserver_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
)

// sharedTarget points Crush's default socket at a fresh, short directory and
// returns options for it, so the default address, started without --host,
// is what the shared tests exercise.
func sharedTarget(t *testing.T) (crushserver.SharedOptions, string) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	return sharedOptions(t), crushserver.DefaultSocket()
}

func dialAddress(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}

func listenAt(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", addr) //nolint:noctx // test listener
	require.NoError(t, err)
	return ln
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

func TestEnsureSharedHonoursAnExplicitSocket(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))
	sock := filepath.Join(shortDir(t), "other.sock")
	stopShared(t, sock)
	opts := sharedOptions(t)
	opts.Socket = sock

	require.NoError(t, crushserver.EnsureShared(t.Context(), opts))

	require.NotEmpty(t, fakeCall(t, sock, http.MethodGet, "/_fake/pid"))
	_, err := os.Stat(crushserver.DefaultSocket())
	require.ErrorIs(t, err, os.ErrNotExist)
}

// shortDir returns a fresh directory whose paths fit in a Unix socket address.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cab")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
