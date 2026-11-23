package crush_test

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// request is one recorded call to the fake Crush server.
type request struct {
	Method string
	Path   string
	Query  map[string]string
	Body   map[string]any
}

// fakeServer records requests and replies from a handler table.
type fakeServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []request
}

func newFakeServer(t *testing.T, routes map[string]http.HandlerFunc) *fakeServer {
	t.Helper()

	f := &fakeServer{}
	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			f.record(r)
			h(w, r)
		})
	}
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) record(r *http.Request) {
	rec := request{Method: r.Method, Path: r.URL.Path, Query: map[string]string{}}
	for k, v := range r.URL.Query() {
		rec.Query[k] = v[0]
	}
	if body, _ := io.ReadAll(r.Body); len(body) > 0 {
		_ = json.Unmarshal(body, &rec.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, rec)
}

func (f *fakeServer) last() request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, v)
}

func TestOpenWorkspaceRegistersProjectWithClientID(t *testing.T) {
	srv := newFakeServer(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"id": "ws-1", "path": "/work/proj"})
		},
	})

	ws, err := crush.New(srv.URL).OpenWorkspace(t.Context(), "/work/proj")
	require.NoError(t, err)

	require.Equal(t, "ws-1", ws.ID())
	req := srv.last()
	require.Equal(t, "/work/proj", req.Body["path"])
	require.Regexp(t, uuidRE, req.Body["client_id"], "Crush rejects client IDs that are not UUIDs")
}

// openFake returns a workspace opened against a fake with the given extra routes.
func openFake(t *testing.T, routes map[string]http.HandlerFunc) (*crush.Workspace, *fakeServer) {
	t.Helper()

	routes["POST /v1/workspaces"] = func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": "ws-1"})
	}
	srv := newFakeServer(t, routes)
	ws, err := crush.New(srv.URL).OpenWorkspace(t.Context(), "/work/proj")
	require.NoError(t, err)
	return ws, srv
}

func TestNewSessionReturnsServerAssignedID(t *testing.T) {
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces/ws-1/sessions": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"id": "sess-9", "title": "acp"})
		},
	})

	id, err := ws.NewSession(t.Context(), "acp")
	require.NoError(t, err)

	require.Equal(t, "sess-9", id)
	require.Equal(t, "acp", srv.last().Body["title"])
}

func TestSetCurrentSessionIdentifiesTheClient(t *testing.T) {
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces/ws-1/current-session": func(http.ResponseWriter, *http.Request) {},
	})

	require.NoError(t, ws.SetCurrentSession(t.Context(), "sess-9"))

	req := srv.last()
	require.Equal(t, "sess-9", req.Body["session_id"])
	require.Regexp(t, uuidRE, req.Query["client_id"],
		"permission prompts are only delivered to the client viewing the session")
}

func TestPromptSendsTextToSession(t *testing.T) {
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces/ws-1/agent": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		},
	})

	require.NoError(t, ws.Prompt(t.Context(), "sess-9", "hello"))

	req := srv.last()
	require.Equal(t, "sess-9", req.Body["session_id"])
	require.Equal(t, "hello", req.Body["prompt"])
}

func TestPromptWhileBusyReturnsErrBusy(t *testing.T) {
	ws, _ := openFake(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces/ws-1/agent": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"busy"}`, http.StatusConflict)
		},
	})

	err := ws.Prompt(t.Context(), "sess-9", "hello")

	require.ErrorIs(t, err, crush.ErrBusy)
}

func TestCancelTargetsTheSession(t *testing.T) {
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces/ws-1/agent/sessions/sess-9/cancel": func(http.ResponseWriter, *http.Request) {},
	})

	require.NoError(t, ws.Cancel(t.Context(), "sess-9"))

	require.Equal(t, "/v1/workspaces/ws-1/agent/sessions/sess-9/cancel", srv.last().Path)
}

func TestGrantEchoesRequestWithChosenAction(t *testing.T) {
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces/ws-1/permissions/grant": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"resolved": true})
		},
	})
	raw := []byte(`{"id":"perm-1","tool_name":"bash","params":{"command":"ls"}}`)

	require.NoError(t, ws.Grant(t.Context(), raw, crush.AllowForSession))

	req := srv.last()
	require.Equal(t, "allow_session", req.Body["action"])
	require.Equal(t, map[string]any{
		"id": "perm-1", "tool_name": "bash", "params": map[string]any{"command": "ls"},
	}, req.Body["permission"])
}

func TestEventsDeliversDataPayloadsInOrderThenCloses(t *testing.T) {
	trace, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bash-permission.sse"))
	require.NoError(t, err)
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"GET /v1/workspaces/ws-1/events": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(trace)
		},
	})

	events, err := ws.Events(t.Context())
	require.NoError(t, err)

	var got []string
	for data := range events {
		got = append(got, string(data))
	}

	var want []string
	for line := range strings.Lines(string(trace)) {
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			want = append(want, strings.TrimSpace(data))
		}
	}
	require.NotEmpty(t, want)
	require.Equal(t, want, got)
	require.Regexp(t, uuidRE, srv.last().Query["client_id"])
}

func TestEventsStopsWhenContextIsCancelled(t *testing.T) {
	ws, _ := openFake(t, map[string]http.HandlerFunc{
		"GET /v1/workspaces/ws-1/events": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done() // hold the stream open
		},
	})
	ctx, cancel := context.WithCancel(t.Context())

	events, err := ws.Events(ctx)
	require.NoError(t, err)
	cancel()

	select {
	case _, ok := <-events:
		require.False(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("event channel did not close after cancel")
	}
}
