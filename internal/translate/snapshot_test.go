package translate_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// updateView is the snapshot form of an Update: kind by name, empty fields
// omitted.
type updateView struct {
	Kind          string               `json:"kind"`
	SessionID     string               `json:"session_id,omitzero"`
	MessageID     string               `json:"message_id,omitzero"`
	Text          string               `json:"text,omitzero"`
	ToolCallID    string               `json:"tool_call_id,omitzero"`
	ToolName      string               `json:"tool_name,omitzero"`
	RawInput      jsontext.Value       `json:"raw_input,omitzero"`
	Output        string               `json:"output,omitzero"`
	IsError       bool                 `json:"is_error,omitzero"`
	PermissionID  string               `json:"permission_id,omitzero"`
	Description   string               `json:"description,omitzero"`
	RawPermission jsontext.Value       `json:"raw_permission,omitzero"`
	RawParams     jsontext.Value       `json:"raw_params,omitzero"`
	Metadata      jsontext.Value       `json:"metadata,omitzero"`
	StopReason    string               `json:"stop_reason,omitzero"`
	BatchID       string               `json:"batch_id,omitzero"`
	Questions     []translate.Question `json:"questions,omitzero"`
}

// streamOf replays a recorded trace and returns every update as snapshot JSON.
func streamOf(t *testing.T, name string) []byte {
	t.Helper()

	tr := translate.New()
	var views []updateView
	for _, data := range replay(t, name) {
		updates, err := tr.Feed(data)
		require.NoError(t, err)
		for i := range updates {
			u := &updates[i]
			views = append(views, updateView{
				Kind: u.Kind.String(), SessionID: u.SessionID, MessageID: u.MessageID, Text: u.Text,
				ToolCallID: u.ToolCallID, ToolName: u.ToolName, RawInput: u.RawInput,
				Output: u.Output, IsError: u.IsError, PermissionID: u.PermissionID,
				Description: u.Description, RawPermission: u.RawPermission, RawParams: u.RawParams, Metadata: u.Metadata,
				StopReason: u.StopReason, BatchID: u.BatchID, Questions: u.Questions,
			})
		}
	}
	out, err := json.Marshal(views)
	require.NoError(t, err)
	return out
}

func TestUpdateStreamForToolAndPermissionTurn(t *testing.T) {
	snaps.MatchJSON(t, streamOf(t, "bash-permission.sse"))
}

func TestUpdateStreamForStreamedAndCancelledTurns(t *testing.T) {
	snaps.MatchJSON(t, streamOf(t, "long-text-cancel.sse"))
}

func TestUpdateStreamForEditTurn(t *testing.T) {
	snaps.MatchJSON(t, streamOf(t, "edit-permission.sse"))
}

func TestUpdateStreamForWriteTurn(t *testing.T) {
	snaps.MatchJSON(t, streamOf(t, "write-permission.sse"))
}
