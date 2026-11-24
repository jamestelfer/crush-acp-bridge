//go:build !windows

package fakecrush

import (
	"context"
	"net"
)

func dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}
