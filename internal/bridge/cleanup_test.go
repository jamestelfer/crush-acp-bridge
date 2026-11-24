package bridge_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// gatedCrush holds Sessions or SetCurrentSession until the test releases
// them, and reports when a call arrives.
type gatedCrush struct {
	*fakeCrush
	listing  chan struct{} // receives when Sessions is called
	listed   chan struct{} // Sessions returns once closed
	setting  chan struct{} // receives when SetCurrentSession is called
	settable chan struct{} // SetCurrentSession returns once closed
}

func newGatedCrush(f *fakeCrush) *gatedCrush {
	return &gatedCrush{
		fakeCrush: f,
		listing:   make(chan struct{}, 1), listed: make(chan struct{}),
		setting: make(chan struct{}, 1), settable: make(chan struct{}),
	}
}

func (g *gatedCrush) Sessions(ctx context.Context) ([]crush.Session, error) {
	g.listing <- struct{}{}
	<-g.listed
	return g.fakeCrush.Sessions(ctx)
}

func (g *gatedCrush) SetCurrentSession(ctx context.Context, id string) error {
	g.setting <- struct{}{}
	<-g.settable
	return g.fakeCrush.SetCurrentSession(ctx, id)
}

func receive(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		require.FailNow(t, what)
	}
}

// After a signal the editor is still connected while clean-up asks Crush
// which sessions are empty. A prompt then must not reach a session that
// clean-up is about to delete.
func TestPromptDuringCleanUpIsRefused(t *testing.T) {
	backend := newGatedCrush(newFakeCrush("sess-1", nil))
	backend.stored = []crush.Session{{ID: "sess-1"}}
	close(backend.settable)
	srv := serveOne(t, backend)
	sid := start(t, srv.conn)

	srv.cancel()
	receive(t, backend.listing, "clean-up never listed the sessions")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second) // a prompt that got through never ends
	defer cancel()
	_, err := srv.conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock("hi")}})
	close(backend.listed)
	srv.wait(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutting down")
	assert.Empty(t, backend.prompts, "Crush never received the prompt")
	assert.Equal(t, []string{"sess-1"}, backend.deletedSessions())
}

// A probe that disconnects while its session/new is still in flight leaves
// a session the editor never learns of.
func TestSessionCreatedDuringCleanUpIsDeleted(t *testing.T) {
	backend := newGatedCrush(newFakeCrush("sess-1", nil))
	backend.stored = []crush.Session{{ID: "sess-1"}}
	close(backend.listed)
	srv := serveOne(t, backend)
	initialize(t, srv.conn)
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		// Whether it succeeds depends on when the signal lands.
		_, _ = srv.conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/proj", McpServers: []acp.McpServer{}})
	}()
	receive(t, backend.setting, "session/new never reached Crush")

	srv.cancel()
	// Give clean-up time to start before session/new completes.
	time.Sleep(100 * time.Millisecond)
	close(backend.settable)
	srv.wait(t)

	assert.Equal(t, []string{"sess-1"}, backend.deletedSessions())
	receive(t, answered, "session/new never answered")
}
