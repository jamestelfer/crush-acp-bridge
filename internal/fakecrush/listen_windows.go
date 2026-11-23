//go:build windows

package fakecrush

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// listen binds a named pipe as Crush does on Windows: go-winio creates the
// first instance only if the name is free, so a second fake loses the race.
func listen(network, addr string) (net.Listener, error) {
	if network == "npipe" {
		return winio.ListenPipe(addr, &winio.PipeConfig{MessageMode: true})
	}
	return (&net.ListenConfig{}).Listen(context.Background(), network, addr)
}
