package crushserver_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
)

// cacheOptions runs the fake with Crush's cache directory at a scratch
// directory, which it returns. The fake writes its log there as Crush does.
func cacheOptions(t *testing.T) (crushserver.Options, string) {
	t.Helper()
	cache := t.TempDir()
	return fakeOptions("serve", "CRUSH_CACHE_DIR="+cache), cache
}

// entries lists the names in dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

// serverDir returns the one per-host directory the running fake made in cache.
func serverDir(t *testing.T, cache string) string {
	t.Helper()
	names := entries(t, cache)
	require.Len(t, names, 1, "the server makes one per-host cache directory")
	dir := filepath.Join(cache, names[0])
	require.FileExists(t, filepath.Join(dir, "crush.log"))
	return dir
}

func TestRepeatedRunsOnAPickedPortLeaveNoCacheDirectory(t *testing.T) {
	opts, cache := cacheOptions(t)

	for range 3 {
		srv, err := crushserver.Start(t.Context(), opts)
		require.NoError(t, err)
		dir := serverDir(t, cache)
		require.NoError(t, srv.Stop())
		assert.NoDirExists(t, dir)
	}

	assert.Empty(t, entries(t, cache))
}

func TestCancellingTheContextRemovesTheCacheDirectory(t *testing.T) {
	opts, cache := cacheOptions(t)
	ctx, cancel := context.WithCancel(t.Context())
	srv, err := crushserver.Start(ctx, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop() })
	serverDir(t, cache)

	cancel()

	assert.Eventually(t, func() bool { return len(entries(t, cache)) == 0 }, 5*time.Second, 20*time.Millisecond)
}

func TestRotatedLogsAreRemovedWithTheCacheDirectory(t *testing.T) {
	opts, cache := cacheOptions(t)
	srv, err := crushserver.Start(t.Context(), opts)
	require.NoError(t, err)
	dir := serverDir(t, cache)
	backup := filepath.Join(dir, "crush-2026-11-24T10-00-00.000.log")
	require.NoError(t, os.WriteFile(backup, []byte("{}\n"), 0o600))

	require.NoError(t, srv.Stop())

	assert.NoDirExists(t, dir)
}

func TestUnknownCacheContentsAreKept(t *testing.T) {
	opts, cache := cacheOptions(t)
	srv, err := crushserver.Start(t.Context(), opts)
	require.NoError(t, err)
	dir := serverDir(t, cache)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.db"), []byte("keep"), 0o600))

	require.NoError(t, srv.Stop())

	assert.Equal(t, []string{"state.db"}, entries(t, dir), "only Crush's logs are disposable")
}

func TestAnExplicitListenAddressKeepsItsCacheDirectory(t *testing.T) {
	opts, cache := cacheOptions(t)
	probe, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	opts.Listen = probe.Addr().String()
	require.NoError(t, probe.Close())
	srv, err := crushserver.Start(t.Context(), opts)
	require.NoError(t, err)
	dir := serverDir(t, cache)

	require.NoError(t, srv.Stop())

	assert.FileExists(t, filepath.Join(dir, "crush.log"),
		"a fixed address reuses its directory, and `crush logs --host` can still find it")
}
