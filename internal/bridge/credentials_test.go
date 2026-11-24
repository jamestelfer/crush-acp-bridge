package bridge_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// vertexModel is a Vertex AI model as Crush's config names it.
var vertexModel = crush.ModelRef{Provider: "vertexai", Model: "gemini-4-hydrogen"}

// noProviderCrush is a Crush whose configured model's provider was dropped
// for missing credentials: it lists no models and refuses every prompt, as
// Crush 0.102.0 does.
func noProviderCrush(current crush.ModelRef) *fakeCrush {
	f := newFakeCrush("sess-1", nil)
	f.models = crush.Models{Current: current}
	f.promptErr = fmt.Errorf("prompt: POST /workspaces/ws-1/agent: agent coordinator not initialized: %w", crush.ErrNoProvider)
	return f
}

// promptError sends a prompt and returns the editor's view of its failure.
func promptError(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId) *acp.RequestError {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock("hi")}})
	var reqErr *acp.RequestError
	require.ErrorAs(t, err, &reqErr)
	return reqErr
}

// credentialData is the data of a credentials error.
type credentialData struct {
	Provider  string   `json:"provider"`
	Variables []string `json:"variables"`
}

func TestPromptRefusedForMissingCredentialsNamesTheProviderAndItsVariables(t *testing.T) {
	backend := noProviderCrush(vertexModel)
	logs := &logCapture{}
	conn := connect(t, backend, &editor{}, bridge.WithLogger(logs.logger()))
	sid := start(t, conn)

	reqErr := promptError(t, conn, sid)

	assert.Contains(t, reqErr.Message, "Google Vertex AI")
	assert.Contains(t, reqErr.Message, "VERTEXAI_PROJECT")
	assert.Contains(t, reqErr.Message, "VERTEXAI_LOCATION")
	assert.Contains(t, reqErr.Message, "Crush server")
	var data credentialData
	require.NoError(t, json.Unmarshal(reqErr.Data, &data))
	assert.Equal(t, credentialData{Provider: "vertexai", Variables: []string{"VERTEXAI_PROJECT", "VERTEXAI_LOCATION"}}, data)
	rec := logs.find(t, "prompt rejected by crush")
	require.NotNil(t, rec, "the refusal is logged")
	assert.Contains(t, rec["error"], "VERTEXAI_PROJECT")
}

func TestPromptRefusedWithNoProviderConfiguredSaysSo(t *testing.T) {
	backend := noProviderCrush(crush.ModelRef{})
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	reqErr := promptError(t, conn, sid)

	assert.Contains(t, reqErr.Message, "no provider")
	assert.Contains(t, reqErr.Message, "ANTHROPIC_API_KEY")
}

func TestPromptRefusedForAnUnknownProviderNamesIt(t *testing.T) {
	backend := noProviderCrush(crush.ModelRef{Provider: "my-proxy", Model: "m"})
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	reqErr := promptError(t, conn, sid)

	assert.Contains(t, reqErr.Message, "my-proxy")
	assert.Contains(t, reqErr.Message, "api_key")
}

// errorFinish is a Crush event ending the session's turn with an error.
func errorFinish(t *testing.T, sessionID, provider, title, details string) trace {
	t.Helper()
	event := map[string]any{"type": "message", "payload": map[string]any{"type": "updated", "payload": map[string]any{
		"id": "m-1", "role": "assistant", "session_id": sessionID, "provider": provider,
		"parts": []any{map[string]any{"type": "finish", "data": map[string]any{
			"reason": "error", "time": 1, "message": title, "details": details,
		}}},
	}}}
	data, err := json.Marshal(event)
	require.NoError(t, err)
	return trace{data}
}

func TestTurnFailingOnCredentialsNamesTheProviderAndItsVariables(t *testing.T) {
	backend := newFakeCrush("sess-1",
		errorFinish(t, "sess-1", "vertexai", "Provider Error", "credentials: could not find default credentials"))
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	reqErr := promptError(t, conn, sid)

	assert.Contains(t, reqErr.Message, "could not find default credentials", "Crush's account of the failure")
	assert.Contains(t, reqErr.Message, "Google Vertex AI")
	assert.Contains(t, reqErr.Message, "VERTEXAI_PROJECT")
	var data credentialData
	require.NoError(t, json.Unmarshal(reqErr.Data, &data))
	assert.Equal(t, "vertexai", data.Provider)
}

func TestTurnFailingForAnotherReasonCarriesCrushsError(t *testing.T) {
	backend := newFakeCrush("sess-1",
		errorFinish(t, "sess-1", "vertexai", "Too Many Requests", "quota exceeded for gemini-4-hydrogen"))
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	reqErr := promptError(t, conn, sid)

	assert.Contains(t, reqErr.Message, "Too Many Requests")
	assert.Contains(t, reqErr.Message, "quota exceeded for gemini-4-hydrogen")
	assert.NotContains(t, reqErr.Message, "VERTEXAI_PROJECT", "the failure is not about credentials")
}

func TestNewSessionWarnsWhenCrushHasNoUsableModel(t *testing.T) {
	backend := noProviderCrush(vertexModel)
	logs := &logCapture{}
	conn := connect(t, backend, &editor{}, bridge.WithLogger(logs.logger()))

	start(t, conn)

	rec := logs.find(t, "crush has no usable model")
	require.NotNil(t, rec, "the missing provider is logged at session/new")
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "vertexai", rec["provider"])
	assert.Contains(t, rec["variables"], "VERTEXAI_PROJECT")
}

func TestNewSessionDoesNotWarnWhenTheModelIsUsable(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.models = crush.Models{Current: vertexModel, Available: []crush.Model{{Provider: "vertexai", ID: "gemini-4-hydrogen"}}}
	logs := &logCapture{}
	conn := connect(t, backend, &editor{}, bridge.WithLogger(logs.logger()))

	start(t, conn)

	assert.Nil(t, logs.find(t, "crush has no usable model"))
}

func TestCredentialsErrorReadsAsSentences(t *testing.T) {
	backend := noProviderCrush(vertexModel)
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	reqErr := promptError(t, conn, sid)

	snaps.MatchSnapshot(t, reqErr.Message)
}
