//go:build live

package live_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"
)

func modelSelector(t *testing.T, opts []acp.SessionConfigOption) *acp.SessionConfigOptionSelect {
	t.Helper()
	for _, o := range opts {
		if o.Select != nil && o.Select.Category != nil && *o.Select.Category == acp.SessionConfigOptionCategoryModel {
			return o.Select
		}
	}
	require.FailNow(t, "no model selector offered")
	return nil
}

// TestLiveModelSwitch changes Crush's model through the bridge, checks the
// next reply used it, and restores the original model.
func TestLiveModelSwitch(t *testing.T) {
	ed := &editor{}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	conn, _, url, cwd := dial(t, ctx, ed)

	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	sel := modelSelector(t, sess.ConfigOptions)
	original := sel.CurrentValue
	t.Logf("current model %s, %d offered", original, len(*sel.Options.Ungrouped))

	provider, _, _ := strings.Cut(string(original), "/")
	var target acp.SessionConfigValueId
	for _, o := range *sel.Options.Ungrouped {
		if o.Value != original && strings.HasPrefix(string(o.Value), provider+"/") {
			target = o.Value
			break
		}
	}
	require.NotEmpty(t, target, "needs a second model from the current provider")

	set := func(ctx context.Context, v acp.SessionConfigValueId) acp.SetSessionConfigOptionResponse {
		resp, err := conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{SessionId: sess.SessionId, ConfigId: sel.Id, Value: v},
		})
		require.NoError(t, err)
		return resp
	}
	// A deferred restore runs while the bridge is alive; t.Cleanup runs after
	// t.Context() has stopped it.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		set(ctx, original)
	}()

	resp := set(ctx, target)
	require.Equal(t, target, modelSelector(t, resp.ConfigOptions).CurrentValue)

	reply, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("Reply with exactly: switched")},
	})
	require.NoError(t, err)
	require.Equal(t, acp.StopReasonEndTurn, reply.StopReason)

	var workspaces []struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	get(t, url+"/v1/workspaces", &workspaces)
	var wsID string
	for _, w := range workspaces {
		if w.Path == cwd {
			wsID = w.ID
		}
	}
	require.NotEmpty(t, wsID)

	var msgs []struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	get(t, fmt.Sprintf("%s/v1/workspaces/%s/sessions/%s/messages", url, wsID, sess.SessionId), &msgs)
	var used string
	for _, m := range msgs {
		if m.Role == "assistant" {
			used = m.Provider + "/" + m.Model
		}
	}
	require.Equal(t, string(target), used, "Crush answered with the selected model")
}
