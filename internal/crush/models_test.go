package crush_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// workspaceConfig is the shape of GET /config trimmed to what matters here.
var workspaceConfig = map[string]any{
	"models": map[string]any{
		"large": map[string]any{"provider": "vertexai", "model": "gemini-4-hydrogen", "max_tokens": 64000},
		"small": map[string]any{"provider": "vertexai", "model": "gemini-3-flash-preview"},
	},
	"providers": map[string]any{
		"vertexai": map[string]any{
			"id": "vertexai", "name": "Google Vertex AI", "api_key": "secret",
			"models": []any{
				map[string]any{"id": "gemini-4-hydrogen", "name": "Gemini 4 Hydrogen"},
				map[string]any{"id": "gemini-3-flash-preview", "name": "Gemini 3 Flash"},
			},
		},
		"anthropic": map[string]any{
			"id": "anthropic", "name": "Anthropic",
			"models": []any{map[string]any{"id": "claude-sonnet-4-6", "name": "Claude Sonnet 4.6"}},
		},
		"openai": map[string]any{
			"id": "openai", "name": "OpenAI", "disable": true,
			"models": []any{map[string]any{"id": "gpt-5", "name": "GPT-5"}},
		},
	},
}

func (f *fakeServer) all() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

// calls lists requests as "METHOD path".
func calls(reqs []request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func TestModelsListsConfiguredProvidersAndTheCurrentModel(t *testing.T) {
	ws, _ := openFake(t, map[string]http.HandlerFunc{
		"GET /v1/workspaces/ws-1/config": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, workspaceConfig)
		},
	})

	models, err := ws.Models(t.Context())
	require.NoError(t, err)

	require.Equal(t, crush.ModelRef{Provider: "vertexai", Model: "gemini-4-hydrogen"}, models.Current)
	require.Equal(t, []crush.Model{
		{Provider: "anthropic", ProviderName: "Anthropic", ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6"},
		{Provider: "vertexai", ProviderName: "Google Vertex AI", ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen"},
		{Provider: "vertexai", ProviderName: "Google Vertex AI", ID: "gemini-3-flash-preview", Name: "Gemini 3 Flash"},
	}, models.Available, "providers sorted by ID, disabled ones left out, model order kept")
}

func TestSetModelWithinTheProviderKeepsTheSmallModel(t *testing.T) {
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"GET /v1/workspaces/ws-1/config": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, workspaceConfig)
		},
		"POST /v1/workspaces/ws-1/config/model": func(http.ResponseWriter, *http.Request) {},
		"POST /v1/workspaces/ws-1/agent/update": func(http.ResponseWriter, *http.Request) {},
	})

	require.NoError(t, ws.SetModel(t.Context(), crush.ModelRef{Provider: "vertexai", Model: "gemini-3-flash-preview"}))

	require.Equal(t, []string{
		"GET /v1/workspaces/ws-1/config",
		"POST /v1/workspaces/ws-1/config/model",
		"POST /v1/workspaces/ws-1/agent/update",
	}, calls(srv.all()[1:]), "the first request opened the workspace")

	set := srv.all()[2].Body
	require.Equal(t, map[string]any{
		"scope":      float64(1), // the workspace's .crush/crush.json, as crush run --model does
		"model_type": "large",
		"model":      map[string]any{"provider": "vertexai", "model": "gemini-3-flash-preview"},
	}, set)
}

func TestSetModelOnAnotherProviderAlsoSwitchesTheSmallModel(t *testing.T) {
	small := map[string]any{"provider": "anthropic", "model": "claude-haiku-4-5", "max_tokens": 4096}
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"GET /v1/workspaces/ws-1/config": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, workspaceConfig)
		},
		"GET /v1/workspaces/ws-1/agent/default-small-model": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, small)
		},
		"POST /v1/workspaces/ws-1/config/model": func(http.ResponseWriter, *http.Request) {},
		"POST /v1/workspaces/ws-1/agent/update": func(http.ResponseWriter, *http.Request) {},
	})

	require.NoError(t, ws.SetModel(t.Context(), crush.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}))

	reqs := srv.all()[1:]
	require.Equal(t, []string{
		"GET /v1/workspaces/ws-1/config",
		"POST /v1/workspaces/ws-1/config/model",
		"GET /v1/workspaces/ws-1/agent/default-small-model",
		"POST /v1/workspaces/ws-1/config/model",
		"POST /v1/workspaces/ws-1/agent/update",
	}, calls(reqs), "a small model from the old provider would fail on the new one")

	require.Equal(t, "anthropic", reqs[2].Query["provider_id"])
	require.Equal(t, "small", reqs[3].Body["model_type"])
	require.Equal(t, map[string]any{"provider": "anthropic", "model": "claude-haiku-4-5", "max_tokens": float64(4096)},
		reqs[3].Body["model"], "the server's default is passed through unchanged")
}

func TestSetModelReportsAFailedUpdate(t *testing.T) {
	ws, _ := openFake(t, map[string]http.HandlerFunc{
		"GET /v1/workspaces/ws-1/config": func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, workspaceConfig)
		},
		"POST /v1/workspaces/ws-1/config/model": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"bad model"}`, http.StatusBadRequest)
		},
	})

	err := ws.SetModel(t.Context(), crush.ModelRef{Provider: "vertexai", Model: "nope"})

	require.ErrorContains(t, err, "set model")
}

func TestSetMainAgentSwitchesCrushsAgent(t *testing.T) {
	ws, srv := openFake(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces/ws-1/agent/main": func(http.ResponseWriter, *http.Request) {},
	})

	require.NoError(t, ws.SetMainAgent(t.Context(), "plan"))

	require.Equal(t, map[string]any{"agent_id": "plan"}, srv.last().Body)
}

func TestSetMainAgentWhileBusyReturnsErrBusy(t *testing.T) {
	ws, _ := openFake(t, map[string]http.HandlerFunc{
		"POST /v1/workspaces/ws-1/agent/main": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"agent is busy"}`, http.StatusConflict)
		},
	})

	require.ErrorIs(t, ws.SetMainAgent(t.Context(), "plan"), crush.ErrBusy)
}
