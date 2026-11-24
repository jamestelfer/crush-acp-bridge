package bridge_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/fakecrush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// midText splits the first turn of long-text-cancel.sse part way through the
// assistant's text, which starts at event 20.
const midText = 28

// promptAsync starts a prompt and returns a channel with its outcome.
func promptAsync(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, text string) <-chan promptResult {
	t.Helper()
	done := make(chan promptResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		resp, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
		done <- promptResult{resp, err}
	}()
	return done
}

type promptResult struct {
	resp acp.PromptResponse
	err  error
}

// awaitPrompt returns the outcome of a prompt started by promptAsync.
func awaitPrompt(t *testing.T, done <-chan promptResult) promptResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the turn hung after the event stream was lost")
		return promptResult{}
	}
}

func agentText(ed *editor) string {
	var b strings.Builder
	for _, u := range ed.snapshot() {
		if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
			b.WriteString(u.AgentMessageChunk.Content.Text.Text)
		}
	}
	return b.String()
}

// fullText is the assistant text an editor receives for tr when nothing is
// lost.
func fullText(t *testing.T, tr trace) string {
	t.Helper()
	var b strings.Builder
	tl := translate.New()
	for _, data := range tr {
		updates, err := tl.Feed(data)
		require.NoError(t, err)
		for _, u := range updates {
			if u.Kind == translate.MessageChunk {
				b.WriteString(u.Text)
			}
		}
	}
	return b.String()
}

// push sends events on the current stream, as the server does.
func (f *fakeCrush) push(tr trace) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, data := range tr {
		f.events <- data
	}
}

func TestDroppedEventStreamKeepsTheTurnAndResubscribes(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(tr.sessionID(t), turn[:midText])
	var opens atomic.Int32
	open := func(context.Context, string) (bridge.Backend, error) {
		opens.Add(1)
		return backend.open(), nil
	}
	ed := &editor{}
	conn := connectWith(t, open, ed)
	sid := start(t, conn)

	done := promptAsync(t, conn, sid, "explain TCP")
	require.Eventually(t, func() bool { return agentText(ed) != "" }, 2*time.Second, 10*time.Millisecond)
	backend.drop() // the server dropped a slow client and keeps running the turn

	require.Eventually(t, func() bool { return backend.subscribes.Load() == 2 }, 5*time.Second, 10*time.Millisecond,
		"the bridge subscribes again")
	require.Eventually(t, func() bool {
		cur := backend.currentSessions()
		return len(cur) == 2 && cur[1] == string(sid)
	}, 2*time.Second, 10*time.Millisecond, "the session is reported again so permission prompts still arrive")
	backend.push(turn[midText:]) // Crush resends whole-message snapshots

	r := awaitPrompt(t, done)
	require.NoError(t, r.err, "the turn survives the drop")
	assert.Equal(t, acp.StopReasonEndTurn, r.resp.StopReason, "the turn ends with Crush's stop reason")
	assert.Equal(t, fullText(t, turn), agentText(ed), "no text is lost or sent twice")
	assert.Equal(t, int32(1), opens.Load(), "the workspace is not reopened")
}

func TestTurnThatEndsWhileTheStreamIsDownStillCompletes(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(tr.sessionID(t), turn[:midText])
	backend.history = turn // the turn finished on the server during the gap
	ed := &editor{}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	done := promptAsync(t, conn, sid, "explain TCP")
	require.Eventually(t, func() bool { return agentText(ed) != "" }, 2*time.Second, 10*time.Millisecond)
	backend.drop()

	r := awaitPrompt(t, done)
	require.NoError(t, r.err)
	assert.Equal(t, acp.StopReasonEndTurn, r.resp.StopReason, "the end is read from the stored messages")
	assert.Equal(t, fullText(t, turn), agentText(ed), "the editor receives the rest of the text once")
}

func TestResubscribeIgnoresEarlierTurnsOfAResumedSession(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(tr.sessionID(t), turn[:midText])
	sessionID := tr.sessionID(t)
	earlier := trace{ // a finished turn from before the bridge resumed the session
		storedMessage(sessionID, "old-u", "user", `{"type":"text","data":{"text":"hello"}}`),
		storedMessage(sessionID, "old-a", "assistant",
			`{"type":"text","data":{"text":"earlier answer"}},{"type":"finish","data":{"reason":"end_turn"}}`),
	}
	backend.history = slices.Concat(earlier, turn[:midText])
	backend.stored = []crush.Session{{ID: tr.sessionID(t), MessageCount: 2}}
	ed := &editor{}
	conn := connect(t, backend, ed)
	initialize(t, conn)
	sid := acp.SessionId(tr.sessionID(t))
	_, err := conn.ResumeSession(t.Context(), acp.ResumeSessionRequest{SessionId: sid, Cwd: "/work/proj", McpServers: []acp.McpServer{}})
	require.NoError(t, err)

	done := promptAsync(t, conn, sid, "explain TCP")
	require.Eventually(t, func() bool { return agentText(ed) != "" }, 2*time.Second, 10*time.Millisecond)
	backend.drop()
	require.Eventually(t, func() bool { return backend.subscribes.Load() == 2 }, 5*time.Second, 10*time.Millisecond)

	select {
	case r := <-done:
		require.FailNow(t, "an earlier turn's finish ended the running turn", "result: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	backend.push(turn[midText:])
	r := awaitPrompt(t, done)
	require.NoError(t, r.err)
	assert.Equal(t, acp.StopReasonEndTurn, r.resp.StopReason)
	assert.NotContains(t, agentText(ed), "earlier answer", "earlier turns are not replayed")
}

// storedMessage is a message event for a session, with parts given as JSON.
func storedMessage(sessionID, id, role, parts string) []byte {
	return []byte(`{"type":"message","payload":{"type":"updated","payload":{"id":"` + id +
		`","session_id":"` + sessionID + `","role":"` + role + `","parts":[` + parts + `]}}}`)
}

func TestPendingPermissionResentAfterResubscribeIsAskedOnce(t *testing.T) {
	tr := loadTrace(t, "bash-permission.sse")
	ask := -1
	for i, data := range tr {
		if bytes.Contains(data, []byte(`"type":"permission_request"`)) {
			ask = i
			break
		}
	}
	require.GreaterOrEqual(t, ask, 0)
	backend := newFakeCrush(tr.sessionID(t), tr[:ask+1])
	var asks atomic.Int32
	release := make(chan struct{})
	ed := &editor{onPermisson: func(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
		asks.Add(1)
		<-release
		return allow(r)
	}}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	done := promptAsync(t, conn, sid, "run it")
	require.Eventually(t, func() bool { return asks.Load() == 1 }, 2*time.Second, 10*time.Millisecond)
	backend.drop()
	require.Eventually(t, func() bool { return backend.subscribes.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
	backend.push(tr[ask : ask+1]) // Crush re-delivers the pending request to a client that re-reports its session

	assert.Never(t, func() bool { return asks.Load() > 1 }, 300*time.Millisecond, 10*time.Millisecond,
		"the editor is asked only once")
	close(release)
	require.Eventually(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return len(backend.grants) == 1
	}, 2*time.Second, 10*time.Millisecond)
	backend.push(tr[ask+1:])
	r := awaitPrompt(t, done)
	require.NoError(t, r.err)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	assert.Len(t, backend.grants, 1, "the request is answered once")
}

func TestResubscribeReusesTheClientID(t *testing.T) {
	var mu sync.Mutex
	var clients []string
	drops := make(chan chan struct{}, 4)
	var opens atomic.Int32
	srv := httptest.NewServer(fakecrush.Handler(fakecrush.Hooks{
		OnOpen: func(string) { opens.Add(1) },
		OnSubscribe: func(clientID string) <-chan struct{} {
			mu.Lock()
			clients = append(clients, clientID)
			mu.Unlock()
			drop := make(chan struct{})
			drops <- drop
			return drop
		},
	}))
	t.Cleanup(srv.Close)
	client := crush.New(srv.URL)
	open := func(ctx context.Context, cwd string) (bridge.Backend, error) { return client.OpenWorkspace(ctx, cwd) }
	conn := connectWith(t, open, &editor{})
	start(t, conn)

	nextDrop := func() chan struct{} {
		select {
		case d := <-drops:
			return d
		case <-time.After(5 * time.Second):
			require.FailNow(t, "the bridge did not subscribe")
			return nil
		}
	}
	close(nextDrop()) // the server drops the stream
	nextDrop()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, clients, 2)
	assert.Equal(t, clients[0], clients[1], "the bridge subscribes again as the same client")
	assert.Equal(t, int32(1), opens.Load(), "no new client is registered")
}

func TestRestartedServerFailsTheTurnAndTheBridgeReconnects(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(tr.sessionID(t), turn[:midText]) // the turn never finishes
	var opens atomic.Int32
	open := func(context.Context, string) (bridge.Backend, error) {
		opens.Add(1)
		return backend.open(), nil
	}
	ed := &editor{}
	conn := connectWith(t, open, ed)
	sid := start(t, conn)

	done := promptAsync(t, conn, sid, "explain TCP")
	require.Eventually(t, func() bool { return agentText(ed) != "" }, 2*time.Second, 10*time.Millisecond)
	sentBefore := agentText(ed)
	backend.restart()

	require.ErrorContains(t, awaitPrompt(t, done).err, "lost", "the editor learns the turn will not finish")
	require.Eventually(t, func() bool { return opens.Load() == 2 }, 5*time.Second, 10*time.Millisecond,
		"the bridge reopens the workspace")
	require.Eventually(t, func() bool {
		cur := backend.currentSessions()
		return len(cur) == 2 && cur[1] == string(sid)
	}, 2*time.Second, 10*time.Millisecond, "the session is reported again so permission prompts still arrive")

	backend.script = turn[midText:] // the server resends the rest of the same message
	resp := prompt(t, conn, sid, "go on")
	require.Equal(t, acp.StopReasonEndTurn, resp.StopReason, "the next prompt works")
	require.True(t, strings.HasPrefix(agentText(ed), sentBefore))
	require.NotContains(t, agentText(ed)[len(sentBefore):], sentBefore[:min(40, len(sentBefore))],
		"text already delivered is not sent again")
}

func TestServerGoneForGoodEndsTheTurnAndIsLogged(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(tr.sessionID(t), turn[:midText])
	var opens atomic.Int32
	open := func(context.Context, string) (bridge.Backend, error) {
		if opens.Add(1) > 1 {
			return nil, errors.New("dial unix /run/user/1000/crush-1000.sock: connect: no such file or directory")
		}
		return backend, nil
	}
	logs := &logCapture{}
	ed := &editor{}
	conn := connectWith(t, open, ed, bridge.WithLogger(logs.logger()))
	sid := start(t, conn)

	done := promptAsync(t, conn, sid, "explain TCP")
	require.Eventually(t, func() bool { return agentText(ed) != "" }, 2*time.Second, 10*time.Millisecond)
	backend.kill()

	require.ErrorContains(t, awaitPrompt(t, done).err, "lost")
	rec := logs.await(t, "crush reconnect failed")
	require.Equal(t, "WARN", rec["level"])
	require.Equal(t, "/work/proj", rec["cwd"])
	require.Contains(t, rec["error"], "no such file")
}

func TestPromptDuringAReconnectWaitsForIt(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(tr.sessionID(t), turn[:midText])
	var opens atomic.Int32
	open := func(context.Context, string) (bridge.Backend, error) {
		if opens.Add(1) > 1 {
			time.Sleep(time.Second) // a shared server restarting
		}
		return backend.open(), nil
	}
	ed := &editor{}
	conn := connectWith(t, open, ed)
	sid := start(t, conn)

	done := promptAsync(t, conn, sid, "explain TCP")
	require.Eventually(t, func() bool { return agentText(ed) != "" }, 2*time.Second, 10*time.Millisecond)
	backend.restart()
	require.ErrorContains(t, awaitPrompt(t, done).err, "lost")

	backend.script = turn[midText:]
	resp := prompt(t, conn, sid, "go on")

	require.Equal(t, acp.StopReasonEndTurn, resp.StopReason, "the prompt waited for the reconnect instead of failing")
	require.Equal(t, int32(2), opens.Load())
}
