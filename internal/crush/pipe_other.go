//go:build !windows

package crush

import (
	"context"
	"errors"
	"net"
)

var errNoNamedPipes = errors.New("npipe:// addresses are Windows named pipes, which this platform does not have; use unix:// or http://")

// dialPipe fails: named pipes exist only on Windows.
func dialPipe(context.Context, string) (net.Conn, error) { return nil, errNoNamedPipes }
