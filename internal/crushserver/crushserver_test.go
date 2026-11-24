package crushserver_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
)

// TestMain lets the test binary stand in for the crush executable.
func TestMain(m *testing.M) {
	fakecrush.MaybeRunAsServer()
	os.Exit(m.Run())
}

// fakeOptions runs the test binary as `crush` in the given fake mode.
func fakeOptions(mode string, env ...string) crushserver.Options {
	return crushserver.Options{
		Binary:       os.Args[0],
		Env:          append([]string{fakecrush.EnvMode + "=" + mode}, env...),
		StartTimeout: 10 * time.Second,
	}
}

func healthy(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestStartReturnsAHealthyServerAndStopEndsIt(t *testing.T) {
	srv, err := crushserver.Start(t.Context(), fakeOptions("serve"))
	require.NoError(t, err)

	assert.True(t, healthy(srv.URL()), "server must be healthy when Start returns")

	require.NoError(t, srv.Stop())
	assert.Eventually(t, func() bool { return !healthy(srv.URL()) }, 3*time.Second, 20*time.Millisecond,
		"server must be gone after Stop")
}

func TestServerIsKeptAliveBeyondCrushsDefaultIdleTimeout(t *testing.T) {
	srv, err := crushserver.Start(t.Context(), fakeOptions("serve"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop() })

	got := fetchEnv(t, srv.URL(), "CRUSH_SERVER_IDLE_TIMEOUT")

	assert.Equal(t, "86400", got, "the bridge owns the server's lifetime, so idle exit is pushed out")
}

func TestExplicitIdleTimeoutIsRespected(t *testing.T) {
	srv, err := crushserver.Start(t.Context(), fakeOptions("serve", "CRUSH_SERVER_IDLE_TIMEOUT=90"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop() })

	assert.Equal(t, "90", fetchEnv(t, srv.URL(), "CRUSH_SERVER_IDLE_TIMEOUT"))
}

func fetchEnv(t *testing.T, base, name string) string {
	t.Helper()
	resp, err := http.Get(base + "/_fake/env?name=" + name) //nolint:noctx // local fake
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

func TestStartFailsWithTheServersOutputWhenItExitsEarly(t *testing.T) {
	_, err := crushserver.Start(t.Context(), fakeOptions("exit"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited before it was ready")
	assert.Contains(t, err.Error(), "refusing to start", "the child's own message explains why")
}

func TestStartFailsClearlyWhenTheBinaryIsMissing(t *testing.T) {
	opts := fakeOptions("serve")
	opts.Binary = "/no/such/crush"

	_, err := crushserver.Start(t.Context(), opts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "/no/such/crush")
}

func TestStartGivesUpWhenTheServerNeverBecomesHealthy(t *testing.T) {
	opts := fakeOptions("silent")
	opts.StartTimeout = 300 * time.Millisecond
	began := time.Now()

	_, err := crushserver.Start(t.Context(), opts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")
	assert.Less(t, time.Since(began), 5*time.Second)
}

func TestCancellingTheContextStopsTheServer(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	srv, err := crushserver.Start(ctx, fakeOptions("serve"))
	require.NoError(t, err)
	require.True(t, healthy(srv.URL()))

	cancel()

	assert.Eventually(t, func() bool { return !healthy(srv.URL()) }, 3*time.Second, 20*time.Millisecond)
}

func TestListenAddressIsHonoured(t *testing.T) {
	probe, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := probe.Addr().String()
	require.NoError(t, probe.Close())
	opts := fakeOptions("serve")
	opts.Listen = addr

	srv, err := crushserver.Start(t.Context(), opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop() })

	assert.Equal(t, "http://"+addr, srv.URL())
	assert.True(t, healthy("http://"+addr))
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestChildOutputIsLogged(t *testing.T) {
	logs := &syncBuffer{}
	opts := fakeOptions("serve")
	opts.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv, err := crushserver.Start(t.Context(), opts)
	require.NoError(t, err)
	require.NoError(t, srv.Stop())

	assert.Eventually(t, func() bool {
		out := logs.String()
		return strings.Contains(out, "crush server output") && strings.Contains(out, "fake crush: listening")
	}, 2*time.Second, 10*time.Millisecond)
}
