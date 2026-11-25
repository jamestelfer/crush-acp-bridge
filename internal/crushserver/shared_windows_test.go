//go:build windows

package crushserver_test

import (
	"crypto/rand"
	"net"
	"testing"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
)

// sharedTarget returns options for a shared server on a fresh named pipe.
// Crush's default pipe is per user, not per test, so the tests name their
// own.
func sharedTarget(t *testing.T) (crushserver.SharedOptions, string) {
	t.Helper()
	opts := sharedOptions(t)
	opts.Socket = `\\.\pipe\crush-acp-test-` + rand.Text()
	return opts, opts.Socket
}

func listenAt(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := winio.ListenPipe(addr, &winio.PipeConfig{MessageMode: true})
	require.NoError(t, err)
	return ln
}

func TestDefaultSocketIsCrushsPipeForTheCurrentUser(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)

	require.Equal(t, crushserver.DefaultPipe(user.User.Sid.String()), crushserver.DefaultSocket())
}
