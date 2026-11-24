//go:build windows

package crush_test

import (
	"crypto/rand"
	"net/http"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

func TestClientTalksToAServerOnANamedPipe(t *testing.T) {
	pipe := `\\.\pipe\crush-acp-test-` + rand.Text()
	ln, err := winio.ListenPipe(pipe, &winio.PipeConfig{MessageMode: true})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(http.ResponseWriter, *http.Request) {})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	require.NoError(t, crush.New("npipe://"+pipe).Ping(t.Context()))
}

func TestClientDialsCrushsForwardSlashPipeForm(t *testing.T) {
	name := "crush-acp-test-" + rand.Text()
	ln, err := winio.ListenPipe(`\\.\pipe\`+name, &winio.PipeConfig{MessageMode: true})
	require.NoError(t, err)
	srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	err = crush.New("npipe:////./pipe/" + name).Ping(t.Context())

	require.ErrorIs(t, err, crush.ErrNotFound, "Crush's default host names the pipe with forward slashes")
}
