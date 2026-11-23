package bridge_test

import (
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

func withModels(f *fakeCrush) *fakeCrush {
	f.models = crush.Models{
		Current: crush.ModelRef{Provider: "vertexai", Model: "gemini-4-hydrogen"},
		Available: []crush.Model{
			{Provider: "anthropic", ProviderName: "Anthropic", ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6"},
			{Provider: "openrouter", ProviderName: "OpenRouter", ID: "qwen/qwen3-coder", Name: ""},
			{Provider: "vertexai", ProviderName: "Google Vertex AI", ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen"},
		},
	}
	return f
}

func openSession(t *testing.T, conn *acp.ClientSideConnection) acp.NewSessionResponse {
	t.Helper()

	_, err := conn.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	require.NoError(t, err)
	resp, err := conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/proj", McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	return resp
}

func setModel(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, value string) (acp.SetSessionConfigOptionResponse, error) {
	t.Helper()
	return conn.SetSessionConfigOption(t.Context(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: sid, ConfigId: "model", Value: acp.SessionConfigValueId(value),
		},
	})
}

// modelOption returns the model selector from a list of config options.
func modelOption(t *testing.T, opts []acp.SessionConfigOption) *acp.SessionConfigOptionSelect {
	t.Helper()
	for _, o := range opts {
		if o.Select != nil && o.Select.Category != nil && *o.Select.Category == acp.SessionConfigOptionCategoryModel {
			return o.Select
		}
	}
	require.FailNow(t, "no model selector", "options: %v", opts)
	return nil
}

func TestNewSessionOffersCrushsModels(t *testing.T) {
	backend := withModels(newFakeCrush("sess-1", nil))
	conn := connect(t, backend, &editor{})

	resp := openSession(t, conn)

	sel := modelOption(t, resp.ConfigOptions)
	require.Equal(t, acp.SessionConfigValueId("vertexai/gemini-4-hydrogen"), sel.CurrentValue)
	out, err := json.Marshal(resp.ConfigOptions)
	require.NoError(t, err)
	snaps.MatchJSON(t, out)
}

func TestSelectingAModelSwitchesCrush(t *testing.T) {
	backend := withModels(newFakeCrush("sess-1", nil))
	conn := connect(t, backend, &editor{})
	sid := openSession(t, conn).SessionId

	resp, err := setModel(t, conn, sid, "openrouter/qwen/qwen3-coder")
	require.NoError(t, err)

	require.Equal(t, []crush.ModelRef{{Provider: "openrouter", Model: "qwen/qwen3-coder"}}, backend.setModels,
		"model IDs may contain slashes; provider IDs do not")
	require.Equal(t, acp.SessionConfigValueId("openrouter/qwen/qwen3-coder"),
		modelOption(t, resp.ConfigOptions).CurrentValue, "the response reflects Crush's new state")
}

func TestUnknownModelIsRejected(t *testing.T) {
	backend := withModels(newFakeCrush("sess-1", nil))
	conn := connect(t, backend, &editor{})
	sid := openSession(t, conn).SessionId

	_, err := setModel(t, conn, sid, "vertexai/not-a-model")

	var reqErr *acp.RequestError
	require.ErrorAs(t, err, &reqErr)
	require.Equal(t, acp.NewInvalidParams(nil).Code, reqErr.Code)
	require.Empty(t, backend.setModels)
}

func TestUnknownConfigOptionIsRejected(t *testing.T) {
	backend := withModels(newFakeCrush("sess-1", nil))
	conn := connect(t, backend, &editor{})
	sid := openSession(t, conn).SessionId

	_, err := conn.SetSessionConfigOption(t.Context(), acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{SessionId: sid, ConfigId: "speed", Value: "fast"},
	})

	var reqErr *acp.RequestError
	require.ErrorAs(t, err, &reqErr)
	require.Equal(t, acp.NewInvalidParams(nil).Code, reqErr.Code)
}

func TestCurrentModelMissingFromTheListIsStillOffered(t *testing.T) {
	backend := withModels(newFakeCrush("sess-1", nil))
	backend.models.Current = crush.ModelRef{Provider: "custom", Model: "local-llm"}
	conn := connect(t, backend, &editor{})

	sel := modelOption(t, openSession(t, conn).ConfigOptions)

	require.Equal(t, acp.SessionConfigValueId("custom/local-llm"), sel.CurrentValue)
	require.Contains(t, values(sel), acp.SessionConfigValueId("custom/local-llm"),
		"the current value must be one of the options")
}

func TestSessionStartsWithoutModelsWhenCrushCannotListThem(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	backend.modelsErr = errors.New("GET /config: 500 Internal Server Error")
	logs := &logCapture{}
	conn := connect(t, backend, &editor{}, bridge.WithLogger(logs.logger()))

	resp := openSession(t, conn)

	require.Equal(t, acp.SessionId("sess-1"), resp.SessionId)
	require.Empty(t, resp.ConfigOptions)
	rec := logs.await(t, "crush models not listed")
	require.Equal(t, "WARN", rec["level"])
}

func values(sel *acp.SessionConfigOptionSelect) []acp.SessionConfigValueId {
	var out []acp.SessionConfigValueId
	if sel.Options.Ungrouped != nil {
		for _, o := range *sel.Options.Ungrouped {
			out = append(out, o.Value)
		}
	}
	return out
}
