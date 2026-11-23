package bridge_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
)

// midText splits the first turn of long-text-cancel.sse part way through the
// assistant's text, which starts at event 20.
const midText = 28

// promptAsync starts a prompt and returns a channel with its outcome.
func promptAsync(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, text string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		_, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
		done <- err
	}()
	return done
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

func TestLostEventStreamFailsTheTurnAndTheBridgeReconnects(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	turn := tr.firstTurn(t, "end_turn")
	backend := newFakeCrush(tr.sessionID(t), turn[:midText]) // the turn never finishes
	var opens atomic.Int32
	open := func(context.Context, string) (bridge.Backend, error) {
		opens.Add(1)
		return backend, nil
	}
	ed := &editor{}
	conn := connectWith(t, open, ed)
	sid := start(t, conn)

	done := promptAsync(t, conn, sid, "explain TCP")
	require.Eventually(t, func() bool { return agentText(ed) != "" }, 2*time.Second, 10*time.Millisecond)
	sentBefore := agentText(ed)
	backend.drop()

	select {
	case err := <-done:
		require.ErrorContains(t, err, "lost", "the editor learns the turn will not finish")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the turn hung after the event stream was lost")
	}
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
	backend.drop()

	require.ErrorContains(t, <-done, "lost")
	rec := logs.await(t, "crush reconnect failed")
	require.Equal(t, "WARN", rec["level"])
	require.Equal(t, "/work/proj", rec["cwd"])
	require.Contains(t, rec["error"], "no such file")
}
