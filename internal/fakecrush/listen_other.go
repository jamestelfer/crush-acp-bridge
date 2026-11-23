//go:build !windows

package fakecrush

import (
	"context"
	"net"
)

func listen(network, addr string) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), network, addr)
}
