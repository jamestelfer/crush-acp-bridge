package bridge_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/require"
)

// received returns the session updates the editor saw, as snapshot JSON.
func received(t *testing.T, ed *editor) []byte {
	t.Helper()
	out, err := json.Marshal(ed.snapshot())
	require.NoError(t, err)
	return out
}

func TestEditorViewOfToolAndPermissionTurn(t *testing.T) {
	tr := loadTrace(t, "bash-permission.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	ed := &editor{onPermisson: allow}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	prompt(t, conn, sid, "Run the shell command 'echo hello > out.txt' and then say done.")

	snaps.MatchJSON(t, received(t, ed))
}

func TestEditorViewOfStreamedTurn(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	backend := newFakeCrush(tr.sessionID(t), tr.firstTurn(t, "end_turn"))
	ed := &editor{}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	resp := prompt(t, conn, sid, tcpPrompt)

	require.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	snaps.MatchJSON(t, received(t, ed))
}
