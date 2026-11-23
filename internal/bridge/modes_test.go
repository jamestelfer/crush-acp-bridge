package bridge_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

func modeIDs(m *acp.SessionModeState) []acp.SessionModeId {
	ids := make([]acp.SessionModeId, 0, len(m.AvailableModes))
	for _, mode := range m.AvailableModes {
		ids = append(ids, mode.Id)
	}
	return ids
}

func setMode(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, mode string) error {
	t.Helper()
	_, err := conn.SetSessionMode(t.Context(), acp.SetSessionModeRequest{SessionId: sid, ModeId: acp.SessionModeId(mode)})
	return err
}

// modeUpdates returns the current-mode updates each session received.
func (e *editor) modeUpdates() map[acp.SessionId][]acp.SessionModeId {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[acp.SessionId][]acp.SessionModeId{}
	for i, u := range e.updates {
		if u.CurrentModeUpdate != nil {
			out[e.sessions[i]] = append(out[e.sessions[i]], u.CurrentModeUpdate.CurrentModeId)
		}
	}
	return out
}

func TestNewSessionOffersCrushsCoderAndPlanAgents(t *testing.T) {
	conn := connect(t, newFakeCrush("sess-1", nil), &editor{})

	resp := openSession(t, conn)

	require.NotNil(t, resp.Modes)
	require.Equal(t, []acp.SessionModeId{"coder", "plan"}, modeIDs(resp.Modes))
	require.Equal(t, acp.SessionModeId("coder"), resp.Modes.CurrentModeId, "Crush starts every workspace on coder")
}

func TestChoosingPlanSwitchesCrushAndEverySessionInTheDirectory(t *testing.T) {
	backend := newFakeCrush("", nil)
	backend.ids = []string{"sess-1", "sess-2", "sess-3"}
	ed := &editor{}
	conn := connect(t, backend, ed)
	first := openSession(t, conn).SessionId
	second := newSession(t, conn, "/work/proj")

	require.NoError(t, setMode(t, conn, first, "plan"))

	require.Equal(t, []string{"plan"}, backend.agents)
	require.Eventually(t, func() bool {
		return fmt.Sprint(ed.modeUpdates()[second]) == "[plan]"
	}, 2*time.Second, 10*time.Millisecond, "Crush has one agent per directory, so the other session changed too")
	require.Empty(t, ed.modeUpdates()[first], "the requesting session already knows")

	third, err := conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/proj", McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	require.Equal(t, acp.SessionModeId("plan"), third.Modes.CurrentModeId)
}

func TestUnknownModeIsRejected(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	conn := connect(t, backend, &editor{})
	sid := openSession(t, conn).SessionId

	err := setMode(t, conn, sid, "task")

	var reqErr *acp.RequestError
	require.ErrorAs(t, err, &reqErr)
	require.Equal(t, acp.NewInvalidParams(nil).Code, reqErr.Code)
	require.Empty(t, backend.agents)
}

func TestModeChangeDuringATurnFailsAndKeepsTheMode(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.agentErr = fmt.Errorf("set main agent: %w", crush.ErrBusy)
	conn := connect(t, backend, &editor{})
	sid := openSession(t, conn).SessionId

	err := setMode(t, conn, sid, "plan")

	require.ErrorContains(t, err, "busy")
	next, err := conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/proj", McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	require.Equal(t, acp.SessionModeId("coder"), next.Modes.CurrentModeId)
}
