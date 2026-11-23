package bridge_test

import (
	"context"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

const bashPrompt = "Run the shell command 'echo hello > out.txt' and then say done."

// initialize opens the connection without creating a session.
func initialize(t *testing.T, conn *acp.ClientSideConnection) acp.InitializeResponse {
	t.Helper()
	resp, err := conn.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	require.NoError(t, err)
	return resp
}

func load(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId) (acp.LoadSessionResponse, error) {
	t.Helper()
	return conn.LoadSession(t.Context(), acp.LoadSessionRequest{SessionId: sid, Cwd: "/work/proj", McpServers: []acp.McpServer{}})
}

// conversation merges consecutive text chunks of one kind, so a streamed turn
// and its replay compare equal when they say the same thing.
func conversation(updates []acp.SessionUpdate) []acp.SessionUpdate {
	out := make([]acp.SessionUpdate, 0, len(updates))
	for _, u := range updates {
		if n := len(out); n > 0 {
			prev := &out[n-1]
			switch {
			case u.UserMessageChunk != nil && prev.UserMessageChunk != nil:
				*prev = acp.UpdateUserMessageText(prev.UserMessageChunk.Content.Text.Text + u.UserMessageChunk.Content.Text.Text)
				continue
			case u.AgentMessageChunk != nil && prev.AgentMessageChunk != nil:
				*prev = acp.UpdateAgentMessageText(prev.AgentMessageChunk.Content.Text.Text + u.AgentMessageChunk.Content.Text.Text)
				continue
			case u.AgentThoughtChunk != nil && prev.AgentThoughtChunk != nil:
				*prev = acp.UpdateAgentThoughtText(prev.AgentThoughtChunk.Content.Text.Text + u.AgentThoughtChunk.Content.Text.Text)
				continue
			}
		}
		out = append(out, u)
	}
	return out
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return string(out)
}

func TestLoadReplaysWhatTheLiveRunShowed(t *testing.T) {
	tr := loadTrace(t, "bash-permission.sse")
	sid := tr.sessionID(t)
	backend := newFakeCrush(sid, tr)
	backend.stored = []crush.Session{{ID: sid, Title: "trace", MessageCount: 4}}

	live := &editor{onPermisson: allow}
	liveConn := connect(t, backend, live)
	require.Equal(t, acp.SessionId(sid), start(t, liveConn))
	prompt(t, liveConn, acp.SessionId(sid), bashPrompt)

	later := &editor{}
	conn := connect(t, backend, later)
	initialize(t, conn)
	_, err := load(t, conn, acp.SessionId(sid))
	require.NoError(t, err)

	// The live editor showed its own prompt; the replay has to send it.
	want := append([]acp.SessionUpdate{acp.UpdateUserMessageText(bashPrompt)}, conversation(live.snapshot())...)
	assert.JSONEq(t, asJSON(t, want), asJSON(t, conversation(later.snapshot())))
	snaps.MatchJSON(t, received(t, later))
}

func TestLoadedSessionTakesPrompts(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	sid := tr.sessionID(t)
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(sid, turn)
	// Crush gives a new turn new message IDs, but the fake's prompt replays the
	// script's, so the stored history holds no messages: only the session event.
	backend.history = turn[:1]
	backend.stored = []crush.Session{{ID: sid, MessageCount: 2}}
	conn := connect(t, backend, &editor{})
	initialize(t, conn)

	resp, err := load(t, conn, acp.SessionId(sid))
	require.NoError(t, err)

	assert.NotNil(t, resp.Modes)
	assert.Contains(t, backend.currentSessions(), sid, "the bridge reports the session it views")
	assert.Equal(t, acp.StopReasonEndTurn, prompt(t, conn, acp.SessionId(sid), tcpPrompt).StopReason)
}

func TestLoadOfUnknownSessionFails(t *testing.T) {
	backend := newFakeCrush("s1", nil)
	conn := connect(t, backend, &editor{})
	initialize(t, conn)

	_, err := load(t, conn, "missing")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestResumeRegistersTheSessionWithoutReplay(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	sid := tr.sessionID(t)
	backend := newFakeCrush(sid, tr.firstTurn(t, "end_turn"))
	backend.stored = []crush.Session{{ID: sid, MessageCount: 2}}
	ed := &editor{}
	conn := connect(t, backend, ed)
	initialize(t, conn)

	resp, err := conn.ResumeSession(t.Context(), acp.ResumeSessionRequest{SessionId: acp.SessionId(sid), Cwd: "/work/proj"})
	require.NoError(t, err)

	assert.NotNil(t, resp.Modes)
	assert.Empty(t, ed.snapshot(), "resume sends no history")
	assert.Equal(t, acp.StopReasonEndTurn, prompt(t, conn, acp.SessionId(sid), tcpPrompt).StopReason)
}

func TestResumeOfUnknownSessionFails(t *testing.T) {
	conn := connect(t, newFakeCrush("s1", nil), &editor{})
	initialize(t, conn)

	_, err := conn.ResumeSession(t.Context(), acp.ResumeSessionRequest{SessionId: "missing", Cwd: "/work/proj"})

	require.Error(t, err)
}

func TestListShowsConversationsNewestFirst(t *testing.T) {
	backend := newFakeCrush("s1", nil)
	backend.stored = []crush.Session{
		{ID: "old", Title: "Old work", MessageCount: 3, UpdatedAt: time.Unix(1795400000, 0)},
		{ID: "sub", ParentID: "new", Title: "sub-agent", MessageCount: 2, UpdatedAt: time.Unix(1795500000, 0)},
		{ID: "empty", Title: "ACP", UpdatedAt: time.Unix(1795600000, 0)},
		{ID: "new", Title: "New work", MessageCount: 6, UpdatedAt: time.Unix(1795436863, 0)},
	}
	conn := connect(t, backend, &editor{})
	initialize(t, conn)
	cwd := "/work/proj"

	resp, err := conn.ListSessions(t.Context(), acp.ListSessionsRequest{Cwd: &cwd})

	require.NoError(t, err)
	assert.Nil(t, resp.NextCursor)
	snaps.MatchJSON(t, resp.Sessions)
}

func TestListWithoutCwdCoversOpenDirectories(t *testing.T) {
	opened := []string{}
	open := func(_ context.Context, cwd string) (bridge.Backend, error) {
		opened = append(opened, cwd)
		b := newFakeCrush("s-"+cwd, nil)
		b.stored = []crush.Session{{ID: "stored-in-" + cwd, Title: "t", MessageCount: 1, UpdatedAt: time.Unix(1795436863, 0)}}
		return b, nil
	}
	conn := connectWith(t, open, &editor{})
	initialize(t, conn)

	resp, err := conn.ListSessions(t.Context(), acp.ListSessionsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Sessions, "no directory is open yet")
	assert.Empty(t, opened, "listing without a directory opens none")

	newSession(t, conn, "/work/a")
	resp, err = conn.ListSessions(t.Context(), acp.ListSessionsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Sessions, 1)
	assert.Equal(t, acp.SessionId("stored-in-/work/a"), resp.Sessions[0].SessionId)
	assert.Equal(t, "/work/a", resp.Sessions[0].Cwd)
}

func TestInitializeAdvertisesSessionLoadListAndResume(t *testing.T) {
	conn := connect(t, newFakeCrush("s1", nil), &editor{})

	caps := initialize(t, conn).AgentCapabilities

	assert.True(t, caps.LoadSession)
	assert.NotNil(t, caps.SessionCapabilities.List)
	assert.NotNil(t, caps.SessionCapabilities.Resume)
	assert.Nil(t, caps.SessionCapabilities.Close, "session/close is not implemented")
}

// replyText is the assistant text a fresh translator finds in events.
func replyText(t *testing.T, events trace) string {
	t.Helper()
	tr := translate.New()
	var b strings.Builder
	for _, data := range events {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.MessageChunk {
				b.WriteString(u.Text)
			}
		}
	}
	return b.String()
}

func TestLoadDuringATurnShowsItsTextOnce(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	sid := tr.sessionID(t)
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(sid, turn[:midText]) // stored so far: a partial reply
	backend.stored = []crush.Session{{ID: sid, MessageCount: 2}}
	ed := &editor{}
	conn := connect(t, backend, ed)
	initialize(t, conn)

	_, err := load(t, conn, acp.SessionId(sid))
	require.NoError(t, err)
	require.NotEmpty(t, agentText(ed), "the replay showed the partial reply")
	for _, data := range turn[midText:] { // the turn goes on in another client
		backend.current() <- data
	}

	want := replyText(t, turn)
	require.Eventually(t, func() bool { return len(agentText(ed)) >= len(want) }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // any duplicate would follow promptly
	assert.Equal(t, want, agentText(ed))
}
