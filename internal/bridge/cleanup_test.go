package bridge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// serveOne serves the bridge with a single backend for every directory.
func serveOne(t *testing.T, backend bridge.Backend, opts ...bridge.Option) *served {
	t.Helper()
	open := func(context.Context, string) (bridge.Backend, error) { return backend, nil }
	return serveWith(t, open, &editor{}, opts...)
}

func TestUnpromptedSessionIsDeletedWhenTheEditorDisconnects(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.stored = []crush.Session{{ID: "sess-1"}}
	srv := serveOne(t, backend)
	start(t, srv.conn)

	srv.disconnect(t)

	assert.Equal(t, []string{"sess-1"}, backend.deletedSessions())
}

func TestUnpromptedSessionIsDeletedWhenTheBridgeIsStopped(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.stored = []crush.Session{{ID: "sess-1"}}
	srv := serveOne(t, backend)
	start(t, srv.conn)

	srv.stop(t)

	assert.Equal(t, []string{"sess-1"}, backend.deletedSessions())
}

func TestSessionTheEditorPromptedIsKept(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	sid := tr.sessionID(t)
	backend := newFakeCrush(sid, tr.firstTurn(t, "end_turn"))
	backend.stored = []crush.Session{{ID: sid}} // even when Crush has not counted the turn
	srv := serveOne(t, backend)
	prompt(t, srv.conn, start(t, srv.conn), "explain TCP")

	srv.disconnect(t)

	assert.Empty(t, backend.deletedSessions())
}

func TestSessionAnotherClientPromptedIsKept(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.stored = []crush.Session{{ID: "sess-1", MessageCount: 2}}
	srv := serveOne(t, backend)
	start(t, srv.conn)

	srv.disconnect(t)

	assert.Empty(t, backend.deletedSessions())
}

func TestBusySessionIsKept(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.stored = []crush.Session{{ID: "sess-1", Busy: true}}
	srv := serveOne(t, backend)
	start(t, srv.conn)

	srv.disconnect(t)

	assert.Empty(t, backend.deletedSessions())
}

func TestSessionMissingFromCrushIsNotDeleted(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	srv := serveOne(t, backend)
	start(t, srv.conn)

	srv.disconnect(t)

	assert.Empty(t, backend.deletedSessions())
}

func TestLoadedEmptySessionIsKept(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.stored = []crush.Session{{ID: "stored-1"}}
	srv := serveOne(t, backend)
	initialize(t, srv.conn)
	_, err := load(t, srv.conn, "stored-1")
	require.NoError(t, err)

	srv.disconnect(t)

	assert.Empty(t, backend.deletedSessions())
}

func TestOnlyTheEmptySessionsOfSeveralAreDeleted(t *testing.T) {
	backend := newFakeCrush("", nil)
	backend.ids = []string{"empty-1", "used", "empty-2"}
	backend.stored = []crush.Session{{ID: "empty-1"}, {ID: "used", MessageCount: 4}, {ID: "empty-2"}}
	srv := serveOne(t, backend)
	start(t, srv.conn)
	newSession(t, srv.conn, "/work/proj")
	newSession(t, srv.conn, "/work/other")

	srv.disconnect(t)

	assert.ElementsMatch(t, []string{"empty-1", "empty-2"}, backend.deletedSessions())
}

func TestDeletedSessionIsLogged(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.stored = []crush.Session{{ID: "sess-1"}}
	logs := &logCapture{}
	srv := serveOne(t, backend, bridge.WithLogger(logs.logger()))
	start(t, srv.conn)

	srv.disconnect(t)

	rec := logs.await(t, "empty session deleted")
	assert.Equal(t, "INFO", rec["level"])
	assert.Equal(t, "sess-1", rec["session_id"])
	assert.Equal(t, "/work/proj", rec["cwd"])
}

func TestFailedDeletionIsLogged(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.stored = []crush.Session{{ID: "sess-1"}}
	backend.deleteErr = errors.New("database is locked")
	logs := &logCapture{}
	srv := serveOne(t, backend, bridge.WithLogger(logs.logger()))
	start(t, srv.conn)

	srv.disconnect(t)

	rec := logs.await(t, "empty session not deleted")
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "sess-1", rec["session_id"])
	assert.Contains(t, rec["error"], "database is locked")
}

func TestSessionCrushCreatedForAFailedNewSessionIsDeleted(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.currentErr = errors.New("connection reset")
	srv := serveOne(t, backend)
	initialize(t, srv.conn)

	_, err := srv.conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/proj", McpServers: []acp.McpServer{}})

	require.Error(t, err)
	assert.Equal(t, []string{"sess-1"}, backend.deletedSessions())
}
