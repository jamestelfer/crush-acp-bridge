package fakecrush

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// stopTimeout bounds how long Stop waits for a fake server to go away.
const stopTimeout = 5 * time.Second

// Call sends a request to the fake server at addr, a Unix socket path or a
// named pipe, and returns the response body. A status other than 200 is an
// error, so a real Crush server fails a call to a /_fake endpoint.
func Call(ctx context.Context, addr, method, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://crush"+path, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, addr) },
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	return string(body), nil
}

// Stop asks the fake server at addr to exit and waits until it stops
// answering. It returns nil when no fake answers at addr, and an error when
// the server is still answering after a few seconds.
func Stop(addr string) error {
	ctx := context.Background()
	if _, err := Call(ctx, addr, http.MethodPost, "/_fake/exit"); err != nil {
		return nil //nolint:nilerr // nothing to stop
	}
	deadline := time.Now().Add(stopTimeout)
	for time.Now().Before(deadline) {
		if _, err := Call(ctx, addr, http.MethodGet, "/v1/health"); err != nil {
			return nil //nolint:nilerr // the server has gone
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("fake crush server at " + addr + " still answers after /_fake/exit")
}
