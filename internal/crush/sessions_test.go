package crush_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

func TestSessionsListsTheWorkspaceSessions(t *testing.T) {
	srv := newFakeServer(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces": func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"id": "ws1"}) },
		"GET /v1/workspaces/ws1/sessions": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, []map[string]any{
				{"id": "s1", "parent_session_id": "", "title": "Fix the build", "message_count": 4, "updated_at": 1795436863, "api_key": "x"},
				{"id": "s2", "parent_session_id": "s1", "title": "sub", "message_count": 2, "updated_at": 1795436900, "is_busy": true},
			})
		},
	})
	ws, err := crush.New(srv.URL).OpenWorkspace(t.Context(), "/work/proj")
	require.NoError(t, err)

	got, err := ws.Sessions(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []crush.Session{
		{ID: "s1", Title: "Fix the build", MessageCount: 4, UpdatedAt: time.Unix(1795436863, 0)},
		{ID: "s2", ParentID: "s1", Title: "sub", MessageCount: 2, UpdatedAt: time.Unix(1795436900, 0), Busy: true},
	}, got)
}

func TestMessagesReturnsTheSessionMessagesAsSent(t *testing.T) {
	srv := newFakeServer(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces": func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"id": "ws1"}) },
		"GET /v1/workspaces/ws1/sessions/s1/messages": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"id":"m1","role":"user"},{"id":"m2","role":"assistant"}]`))
		},
	})
	ws, err := crush.New(srv.URL).OpenWorkspace(t.Context(), "/work/proj")
	require.NoError(t, err)

	got, err := ws.Messages(t.Context(), "s1")

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.JSONEq(t, `{"id":"m1","role":"user"}`, string(got[0]))
	assert.JSONEq(t, `{"id":"m2","role":"assistant"}`, string(got[1]))
}

func TestMessagesReportsAMissingSession(t *testing.T) {
	srv := newFakeServer(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces": func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"id": "ws1"}) },
		"GET /v1/workspaces/ws1/sessions/nope/messages": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	})
	ws, err := crush.New(srv.URL).OpenWorkspace(t.Context(), "/work/proj")
	require.NoError(t, err)

	_, err = ws.Messages(t.Context(), "nope")

	require.ErrorIs(t, err, crush.ErrNotFound)
}

func TestSessionWithoutAnUpdateTimeHasZeroTime(t *testing.T) {
	srv := newFakeServer(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces": func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"id": "ws1"}) },
		"GET /v1/workspaces/ws1/sessions": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, []map[string]any{{"id": "s1", "message_count": 1, "updated_at": 0}})
		},
	})
	ws, err := crush.New(srv.URL).OpenWorkspace(t.Context(), "/work/proj")
	require.NoError(t, err)

	got, err := ws.Sessions(t.Context())

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].UpdatedAt.IsZero(), "not 1970: %v", got[0].UpdatedAt)
}

func TestDeleteSessionRemovesTheSession(t *testing.T) {
	srv := newFakeServer(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces":                   func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"id": "ws1"}) },
		"DELETE /v1/workspaces/ws1/sessions/s1": func(http.ResponseWriter, *http.Request) {},
	})
	ws, err := crush.New(srv.URL).OpenWorkspace(t.Context(), "/work/proj")
	require.NoError(t, err)

	err = ws.DeleteSession(t.Context(), "s1")

	require.NoError(t, err)
	assert.Equal(t, http.MethodDelete, srv.last().Method)
	assert.Equal(t, "/v1/workspaces/ws1/sessions/s1", srv.last().Path)
}

func TestDeleteSessionReportsAMissingSession(t *testing.T) {
	srv := newFakeServer(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces": func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"id": "ws1"}) },
		"DELETE /v1/workspaces/ws1/sessions/nope": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	})
	ws, err := crush.New(srv.URL).OpenWorkspace(t.Context(), "/work/proj")
	require.NoError(t, err)

	err = ws.DeleteSession(t.Context(), "nope")

	require.ErrorIs(t, err, crush.ErrNotFound)
}
