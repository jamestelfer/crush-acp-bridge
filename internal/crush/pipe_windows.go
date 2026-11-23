//go:build windows

package crush

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// dialPipe connects to the named pipe at address once it has confirmed that
// the process serving it runs as the current user. Pipe names share one
// machine-wide namespace, so another user can create the name first, and
// opening a workspace sends the environment. Crush's own client makes the
// same check (Crush 0.102.0, internal/client/dial_windows.go).
func dialPipe(ctx context.Context, address string) (net.Conn, error) {
	if err := verifyPipeServer(ctx, address); err != nil {
		return nil, err
	}
	return winio.DialPipeContext(ctx, address)
}

// verifyPipeServer fails unless the server of the pipe at address runs as
// the current user. It uses a short-lived connection of its own: go-winio's
// connection is bound to go-winio's completion port, which must see no other
// I/O.
func verifyPipeServer(ctx context.Context, address string) error {
	h, err := openPipe(ctx, address)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return fmt.Errorf("verify the owner of named pipe %q: %w", address, err)
	}
	ours, err := runsAsCurrentUser(pid)
	if err != nil {
		return fmt.Errorf("verify the owner of named pipe %q: %w", address, err)
	}
	if !ours {
		return fmt.Errorf("named pipe %q is owned by another user", address)
	}
	return nil
}

// openPipe opens the pipe at address, retrying while every instance is busy.
// The server may not impersonate the connection.
func openPipe(ctx context.Context, address string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return 0, err
	}
	for {
		h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING,
			windows.SECURITY_SQOS_PRESENT|windows.SECURITY_ANONYMOUS, 0)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return 0, &os.PathError{Op: "open", Path: address, Err: err}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// runsAsCurrentUser reports whether process pid runs as the current user.
func runsAsCurrentUser(pid uint32) (bool, error) {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false, err
	}
	defer func() { _ = windows.CloseHandle(p) }()

	var token windows.Token
	if err := windows.OpenProcessToken(p, windows.TOKEN_QUERY, &token); err != nil {
		return false, err
	}
	defer func() { _ = token.Close() }()

	server, err := token.GetTokenUser()
	if err != nil {
		return false, err
	}
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	return server.User.Sid.Equals(me.User.Sid), nil
}
