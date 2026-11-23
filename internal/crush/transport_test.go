package crush_test

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// shortTempDir returns a directory whose paths fit in a Unix socket address.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cab")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestClientTalksToAServerOnAUnixSocket(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "crush.sock")
	ln, err := net.Listen("unix", sock) //nolint:noctx // test listener
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(http.ResponseWriter, *http.Request) {})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	require.NoError(t, crush.New("unix://"+sock).Ping(t.Context()))
}

func TestOpenWorkspaceSendsTheBridgesEnvironment(t *testing.T) {
	t.Setenv("VERTEXAI_PROJECT", "proj-from-bridge")
	_, srv := openFake(t, map[string]http.HandlerFunc{})

	opened := srv.all()[0]
	env, ok := opened.Body["env"].([]any)
	require.True(t, ok, "env is a list of KEY=value strings, as Crush clients send")
	require.Contains(t, env, "VERTEXAI_PROJECT=proj-from-bridge",
		"provider credentials must reach a server another client started")
}

func TestEnvironmentGoesOnlyToALocalServer(t *testing.T) {
	for url, local := range map[string]bool{
		"unix:///run/user/1000/crush-1000.sock": true,
		"http://127.0.0.1:8484":                 true,
		"http://127.1.2.3:8484":                 true,
		"http://localhost:8484":                 true,
		"http://LocalHost:8484/":                true,
		"http://[::1]:8484":                     true,
		"http://192.168.1.20:8484":              false,
		"http://build-box:8484":                 false,
		"https://crush.example.com":             false,
		"http://[2001:db8::1]:8484":             false,
		"not a url\x7f":                         false,
	} {
		assert.Equal(t, local, crush.IsLocal(url), url)
	}
}
