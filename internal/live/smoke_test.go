//go:build live

package live_test

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// TestSmoke runs a broad, expandable end-to-end smoke suite against the built
// crush-acp-bridge binary and a real Crush server.
//
// Run with `just live -run TestSmoke`. When CRUSH_ACP_URL is set, server-backed
// checks talk to that server; otherwise TestSmoke starts a temporary isolated
// `crush server` on a free loopback port and stops it when done.
//
// Bridge behaviour is checked once, on the first model of SMOKE_MODELS. The
// model-sensitive checks run for every model, in parallel; see smokeModels.
func TestSmoke(t *testing.T) {
	serverURL := ensureCrushServer(t)
	models := smokeModels()
	primary := models[0]

	t.Run("session_lifecycle_and_probe_cleanup", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		cwd := smokeProject(t)

		ed := newSmokeEditor(acp.ClientCapabilities{})
		b := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--crush-url", serverURL},
			editor: ed,
		})

		caps := b.init.AgentCapabilities
		assert.True(t, caps.LoadSession, "bridge should advertise session/load")
		require.NotNil(t, caps.SessionCapabilities.List)
		assert.True(t, caps.PromptCapabilities.Image, "bridge should advertise image prompts")
		assert.True(t, caps.PromptCapabilities.EmbeddedContext, "bridge should advertise embedded resources")

		// Create a probe session and leave it unprompted; it should be cleaned up on exit.
		probeSess, err := b.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)
		require.NotNil(t, probeSess.Modes)
		assert.Equal(t, acp.SessionModeId("coder"), probeSess.Modes.CurrentModeId)
		assert.NotNil(t, modelSelector(t, probeSess.ConfigOptions))

		// Create a real session and send one prompt.
		liveSess := newSmokeSession(t, ctx, b.conn, cwd, primary)
		resp, err := b.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: liveSess.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Reply with exactly this token and nothing else: SMOKE_OK")},
		})
		require.NoError(t, err)
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "SMOKE_OK")

		// Closing the bridge should remove the unprompted probe session and keep the prompted one.
		b.close(t)

		ids := serverSessionIDs(t, ctx, serverURL, cwd)
		assert.NotContains(t, ids, string(probeSess.SessionId), "unprompted probe session should be deleted on exit")
		assert.Contains(t, ids, string(liveSess.SessionId), "prompted session should remain in Crush")

		// A fresh bridge should list and load the saved session.
		replayEd := newSmokeEditor(acp.ClientCapabilities{})
		b2 := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--crush-url", serverURL},
			editor: replayEd,
		})
		defer b2.close(t)

		listResp, err := b2.conn.ListSessions(ctx, acp.ListSessionsRequest{Cwd: &cwd})
		require.NoError(t, err)
		listed := make([]acp.SessionId, 0, len(listResp.Sessions))
		for _, s := range listResp.Sessions {
			listed = append(listed, s.SessionId)
		}
		assert.Contains(t, listed, liveSess.SessionId)

		loadResp, err := b2.conn.LoadSession(ctx, acp.LoadSessionRequest{
			SessionId:  liveSess.SessionId,
			Cwd:        cwd,
			McpServers: []acp.McpServer{},
		})
		require.NoError(t, err)
		assert.NotNil(t, loadResp.Modes)
		assert.Contains(t, replayEd.text(), "SMOKE_OK", "session/load should replay prior assistant output")
	})

	t.Run("resubscribe_on_stream_drop", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		cwd := smokeProject(t)

		proxy := newDropOnceProxy(t, serverURL)
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--crush-url", proxy.server.URL},
			editor: ed,
		})
		defer b.close(t)

		sess := newSmokeSession(t, ctx, b.conn, cwd, primary)

		proxy.armDrop()
		resp, err := b.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sess.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Write three short sentences about bridges, ending with the word FINISHED.")},
		})
		require.NoError(t, err)
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "FINISHED")

		ids := proxy.clientIDs()
		require.GreaterOrEqual(t, len(ids), 2, "bridge should resubscribe after an SSE stream drop")
		assert.Equal(t, ids[0], ids[1], "resubscription should reuse the same client_id")
	})

	t.Run("managed_cache_cleanup_and_missing_credentials", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()

		home := t.TempDir()
		cfgDir := filepath.Join(home, "config")
		dataDir := filepath.Join(home, "data")
		cacheDir := filepath.Join(home, "cache")
		cwd := filepath.Join(home, "proj")
		for _, d := range []string{filepath.Join(cfgDir, "crush"), dataDir, cacheDir, cwd} {
			require.NoError(t, os.MkdirAll(d, 0o755))
		}
		// Copy the Vertex AI test config so Crush has a configured provider whose credentials are absent.
		cfgBytes, err := os.ReadFile(filepath.Join("..", "..", "testdata", "crush.json"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "crush", "crush.json"), cfgBytes, 0o644))

		ed := newSmokeEditor(acp.ClientCapabilities{})
		b := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--managed"},
			env:    isolatedNoCredsEnv(home, cfgDir, dataDir, cacheDir),
			editor: ed,
		})

		// No credentials means no model selector, so this session keeps the test config's model.
		sess, err := b.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)

		_, err = b.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sess.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Say hello")},
		})
		reqErr, ok := errors.AsType[*acp.RequestError](err)
		require.True(t, ok, "missing credentials should fail with an ACP RequestError, got %v", err)
		assert.Contains(t, reqErr.Message, "Google Vertex AI")
		assert.Contains(t, reqErr.Message, "VERTEXAI_PROJECT")
		assert.Contains(t, reqErr.Message, "VERTEXAI_LOCATION")

		var data struct {
			Provider  string   `json:"provider"`
			Variables []string `json:"variables"`
		}
		require.NoError(t, json.Unmarshal(reqErr.Data, &data))
		assert.Equal(t, "vertexai", data.Provider)
		assert.Equal(t, []string{"VERTEXAI_PROJECT", "VERTEXAI_LOCATION"}, data.Variables)

		b.close(t)

		// The --managed server's per-host cache directory should have been removed on exit.
		entries, err := os.ReadDir(cacheDir)
		require.NoError(t, err)
		for _, e := range entries {
			assert.False(t, strings.HasPrefix(e.Name(), "server-"), "managed server cache dir %q should be removed", e.Name())
		}
	})

	t.Run("shared_server_isolation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		cwd := smokeProject(t)
		sock := shortTempSocket(t)
		cacheDir := t.TempDir()

		env := append(os.Environ(),
			"CRUSH_CACHE_DIR="+cacheDir,
			"CRUSH_SERVER_IDLE_TIMEOUT=2",
		)

		ed1 := newSmokeEditor(acp.ClientCapabilities{})
		b1 := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--shared", "--crush-socket", sock},
			env:    env,
			editor: ed1,
		})
		defer b1.close(t)

		ed2 := newSmokeEditor(acp.ClientCapabilities{})
		b2 := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--shared", "--crush-socket", sock},
			env:    env,
			editor: ed2,
		})
		defer b2.close(t)

		s1 := newSmokeSession(t, ctx, b1.conn, cwd, primary)
		s2 := newSmokeSession(t, ctx, b2.conn, cwd, primary)

		r1, err := b1.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: s1.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Reply with exactly: SHARED_ONE")},
		})
		require.NoError(t, err)
		assert.Equal(t, acp.StopReasonEndTurn, r1.StopReason)
		assert.Contains(t, ed1.resetText(), "SHARED_ONE")

		r2, err := b2.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: s2.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Reply with exactly: SHARED_TWO")},
		})
		require.NoError(t, err)
		assert.Equal(t, acp.StopReasonEndTurn, r2.StopReason)
		assert.Contains(t, ed2.resetText(), "SHARED_TWO")
	})

	t.Run("models", func(t *testing.T) {
		for i, model := range models {
			partner := models[0]
			if i == 0 {
				partner = ""
				if len(models) > 1 {
					partner = models[1]
				}
			}
			t.Run(smokeModelName(model), func(t *testing.T) {
				t.Parallel()
				requireModelOffered(t, serverURL, model)
				requireModelWorks(t, serverURL, model)
				for _, c := range modelChecks {
					t.Run(c.name, func(t *testing.T) {
						ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
						defer cancel()
						r := &modelRun{serverURL: serverURL, model: model, partner: partner}
						defer r.finish(t)
						c.run(t, ctx, r)
					})
				}
			})
		}
	})
}

// modelRun is what a model check needs to start its own bridges and sessions.
// partner is another model to switch to mid-conversation, or empty.
type modelRun struct {
	serverURL string
	model     acp.SessionConfigValueId
	partner   acp.SessionConfigValueId
	bridges   []*smokeBridge
	sessions  map[acp.SessionId]string
}

// session opens a session on the check's model in cwd and remembers it, so
// finish can compare what the editor was shown with what Crush stored.
func (r *modelRun) session(t *testing.T, ctx context.Context, b *smokeBridge, cwd string) acp.NewSessionResponse {
	t.Helper()
	sess := newSmokeSession(t, ctx, b.conn, cwd, r.model)
	if r.sessions == nil {
		r.sessions = map[acp.SessionId]string{}
	}
	r.sessions[sess.SessionId] = cwd
	return sess
}

// launch starts a bridge for editor ed; finish closes it and checks what it sent.
func (r *modelRun) launch(t *testing.T, ctx context.Context, ed *smokeEditor) *smokeBridge {
	t.Helper()
	b := startSmokeBridge(t, ctx, smokeConfig{args: []string{"--crush-url", r.serverURL}, editor: ed})
	r.bridges = append(r.bridges, b)
	return b
}

// start launches a bridge for editor ed and opens a session on the check's
// model in a fresh project.
func (r *modelRun) start(t *testing.T, ctx context.Context, ed *smokeEditor) (*smokeBridge, acp.SessionId, string) {
	t.Helper()
	cwd := smokeProject(t)
	b := r.launch(t, ctx, ed)
	sess := r.session(t, ctx, b, cwd)
	return b, sess.SessionId, cwd
}

// finish logs how the model went about the check, fails it on any ACP rule
// the bridge broke, and closes its bridges. It runs before the test returns:
// a bridge process dies with t.Context(), which ends before t.Cleanup runs.
func (r *modelRun) finish(t *testing.T) {
	t.Helper()
	for _, b := range r.bridges {
		b.close(t)
		t.Logf("%s: %s", r.model, b.ed.stats())
		assert.Empty(t, b.ed.protocolViolations(), "the bridge broke ACP's rules")
	}
	r.checkStoredText(t)
}

// checkStoredText fails the check when the text an editor was streamed for a
// session differs from the assistant text Crush stored for it: a dropped,
// repeated or reordered chunk. Whitespace is collapsed, and a session with a
// cancelled or failed turn is skipped. The last bridge to show a session is
// compared, since a loaded session replays everything before it.
func (r *modelRun) checkStoredText(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for sid, cwd := range r.sessions {
		var got string
		var complete bool
		for _, b := range r.bridges {
			if text, ok := b.ed.streamedText(sid); text != "" {
				got, complete = text, ok
			}
		}
		if got == "" || !complete {
			continue
		}
		want := storedAssistantText(t, ctx, r.serverURL, cwd, sid)
		assert.Equal(t, strings.Join(strings.Fields(want), " "), strings.Join(strings.Fields(got), " "),
			"the editor must be shown exactly the text Crush stored for session %s", sid)
	}
}

// storedAssistantText is the text of every assistant message Crush stored for sid, in order.
func storedAssistantText(t *testing.T, ctx context.Context, serverURL, cwd string, sid acp.SessionId) string {
	t.Helper()
	ws, err := crush.New(serverURL).OpenWorkspace(ctx, cwd)
	require.NoError(t, err)
	msgs, err := ws.Messages(ctx, string(sid))
	require.NoError(t, err)
	var out strings.Builder
	for _, raw := range msgs {
		var m struct {
			Role  string `json:"role"`
			Parts []struct {
				Type string `json:"type"`
				Data struct {
					Text string `json:"text"`
				} `json:"data"`
			} `json:"parts"`
		}
		require.NoError(t, json.Unmarshal(raw, &m))
		if m.Role != "assistant" {
			continue
		}
		for _, p := range m.Parts {
			if p.Type == "text" {
				out.WriteString(p.Data.Text)
			}
		}
	}
	return out.String()
}

func prompt(t *testing.T, ctx context.Context, b *smokeBridge, sid acp.SessionId, blocks ...acp.ContentBlock) acp.PromptResponse {
	t.Helper()
	resp, err := b.turn(ctx, sid, blocks...)
	require.NoError(t, err)
	return resp
}

// modelChecks are the checks every model in SMOKE_MODELS runs. Each targets
// behaviour that differs between models: following exact instructions, tool
// call shapes, multi-step tool use, vision, and how a turn ends.
var modelChecks = []struct {
	name string
	run  func(t *testing.T, ctx context.Context, r *modelRun)
}{
	{"exact_reply_unicode", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, _ := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock("Reply with exactly this text and nothing else: Grüße — 日本 ✓ SMOKE_UNICODE"))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "Grüße — 日本 ✓ SMOKE_UNICODE", "multi-byte text must survive streaming intact")
	}},
	{"multi_turn_memory", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, _ := r.start(t, ctx, ed)
		prompt(t, ctx, b, sid, acp.TextBlock("Remember the code word ORCHID-58. Reply with only OK."))
		ed.resetText()
		resp := prompt(t, ctx, b, sid, acp.TextBlock("What code word did I give you? Reply with the code word only."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "ORCHID-58")
	}},
	{"edit_with_diff", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		target := filepath.Join(cwd, "greeting.txt")
		require.NoError(t, os.WriteFile(target, []byte("hello world\n"), 0o644))

		resp := prompt(t, ctx, b, sid, acp.TextBlock("Edit greeting.txt to replace 'hello' with 'goodbye'. Then reply 'done'."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		needCalls(t, ed, acp.ToolKindEdit, 1)
		assert.NotEmpty(t, ed.permissions(), "editing a file should ask for permission")
		assert.NotEmpty(t, ed.diffs(), "edit updates should include ACP diff content")
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "goodbye world\n", string(got))
	}},
	{"several_files_in_one_turn", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Create three files: a.txt containing exactly ALPHA, b.txt containing exactly BRAVO, "+
				"and c.txt containing exactly CHARLIE. Then reply 'done'."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		needCalls(t, ed, acp.ToolKindEdit, 3)
		for name, want := range map[string]string{"a.txt": "ALPHA", "b.txt": "BRAVO", "c.txt": "CHARLIE"} {
			got, err := os.ReadFile(filepath.Join(cwd, name))
			if assert.NoError(t, err, "%s should exist", name) {
				assert.Equal(t, want, strings.TrimSpace(string(got)), name)
			}
		}
	}},
	{"shell_tool_with_permission", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, _ := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Run this shell command with your bash tool: echo SHELL_$((6*7))_$((100+23)) "+
				"Then reply with the command's output only."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		needCalls(t, ed, acp.ToolKindExecute, 1)
		assert.NotEmpty(t, ed.permissions(), "running a shell command should ask for permission")
		assert.Contains(t, ed.resetText(), "SHELL_42_123")
	}},
	{"tool_error_recovery", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, _ := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Use your view tool to read the file missing-file.txt in the current directory. "+
				"It may not exist; if reading it fails, reply with exactly NO_SUCH_FILE and nothing else."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason, "a failed tool call must not end the turn early")
		needCalls(t, ed, "", 1)
		assert.Positive(t, ed.failedToolCalls(), "the failed read should be reported as a failed tool call")
		assert.Contains(t, ed.resetText(), "NO_SUCH_FILE")
	}},
	{"plan_mode_does_not_edit", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		target := filepath.Join(cwd, "greeting.txt")
		require.NoError(t, os.WriteFile(target, []byte("hello world\n"), 0o644))

		_, err := b.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: "plan"})
		require.NoError(t, err)
		resp := prompt(t, ctx, b, sid, acp.TextBlock("Plan how you would change greeting.txt so it says 'goodbye world'. Describe the plan in two sentences."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.NotEmpty(t, strings.TrimSpace(ed.resetText()), "plan mode should still answer")
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "hello world\n", string(got), "plan mode must not edit files")

		_, err = b.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: "coder"})
		require.NoError(t, err)
	}},
	{"attachments_and_vision", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)

		// Unsupported audio blocks should be refused cleanly without breaking the session.
		_, err := b.turn(ctx, sid, acp.TextBlock("listen"), acp.AudioBlock(base64.StdEncoding.EncodeToString([]byte("ogg")), "audio/ogg"))
		reqErr, ok := errors.AsType[*acp.RequestError](err)
		require.True(t, ok, "audio block should return an ACP RequestError, got %v", err)
		assert.Equal(t, acp.NewInvalidParams(nil).Code, reqErr.Code)

		img := acp.ImageBlock(tinyRedPNG(t), "image/png")
		imgURI := "file://" + filepath.ToSlash(filepath.Join(cwd, "dot.png"))
		img.Image.Uri = &imgURI

		noteMime := "text/plain"
		note := acp.ContentBlock{
			Resource: &acp.ContentBlockResource{
				Resource: acp.EmbeddedResourceResource{
					TextResourceContents: &acp.TextResourceContents{
						Uri:      "file://" + filepath.ToSlash(filepath.Join(cwd, "note.txt")),
						MimeType: &noteMime,
						Text:     "secret_token=SMOKE_ATTACH_7391\n",
					},
				},
			},
		}

		ask := acp.TextBlock("Read the attached note and repeat the exact secret_token value from it. " +
			"Then name the single colour that fills the attached image, in one word.")
		resp, err := b.turn(ctx, sid, ask, note, img)
		if reqErr, ok := errors.AsType[*acp.RequestError](err); ok && strings.Contains(string(reqErr.Data), "does not accept images") {
			assert.Equal(t, acp.NewInvalidParams(nil).Code, reqErr.Code, "an image for a model without vision is refused as invalid params")
			resp = prompt(t, ctx, b, sid, acp.TextBlock("Read the attached note and repeat the exact secret_token value from it."), note)
			assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
			assert.Contains(t, ed.resetText(), "SMOKE_ATTACH_7391")
			return
		}
		require.NoError(t, err)
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		reply := ed.resetText()
		assert.Contains(t, reply, "SMOKE_ATTACH_7391")
		assert.Contains(t, strings.ToLower(reply), "red", "the model should see the image")
	}},
	{"questions_chat_fallback", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, _ := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Use the question tool once with a single batch of two questions: "+
				"a single_choice question offering Alpha or Beta, "+
				"and a free_text question asking for my favourite fruit. "+
				"Do not create or edit any file."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		needCalls(t, ed, "", 1)
		assert.Empty(t, ed.permissions(), "the chat fallback must not ask for permission")
		assert.Zero(t, ed.elicitationCount(), "the chat fallback must not elicit")
		assert.Contains(t, ed.toolCallTitles(), "2 questions", "the question tool must have run and been shown")
		assert.NotEmpty(t, strings.TrimSpace(ed.resetText()), "the model should ask in chat")
	}},
	{"questions_form_elicitation", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
		})
		ed.onElicit = func(req acp.CreateElicitationRequest) acp.CreateElicitationResponse {
			content := map[string]acp.ElicitationContentValue{}
			for key, f := range req.Form.RequestedSchema.Properties {
				switch {
				case f.Array != nil:
					ids := make([]string, 0, len(f.Array.Items.Titled.AnyOf))
					for _, o := range f.Array.Items.Titled.AnyOf {
						ids = append(ids, o.Const)
					}
					content[key] = acp.NewElicitationContentValueStringArray(ids...)
				case f.String != nil && len(f.String.OneOf) > 0 && !strings.HasSuffix(key, "_custom"):
					content[key] = acp.NewElicitationContentValueString(f.String.OneOf[0].Const)
				case f.String != nil && strings.HasSuffix(key, "_custom"):
					content[key] = acp.NewElicitationContentValueString("CUSTOM77")
				case f.String != nil:
					content[key] = acp.NewElicitationContentValueString("MANGO42")
				}
			}
			return acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{Action: "accept", Content: content}}
		}
		b, sid, _ := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Use the question tool once with a single batch of four questions: "+
				"a single_choice question offering Alpha or Beta, a yes_no question about whether to continue, "+
				"a multi_choice question offering Red, Green or Blue, and a free_text question asking for my favourite fruit. "+
				"Do not create or edit any file. After I answer, reply with all my answers, "+
				"including word for word any extra text I typed alongside a choice."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Empty(t, ed.permissions(), "questions must not arrive as permission requests")
		reply := ed.resetText()
		needCalls(t, ed, "", 1)
		reqs := ed.elicitations()
		require.Len(t, reqs, 1, "the whole batch is one elicitation")
		assert.Len(t, reqs[0].Form.RequestedSchema.Properties, 6, "four questions plus two custom fields")
		assert.Contains(t, reply, "MANGO42")
		assert.Contains(t, reply, "CUSTOM77", "the custom answer must reach the model")
	}},
	{"questions_form_decline", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
		})
		ed.onElicit = func(acp.CreateElicitationRequest) acp.CreateElicitationResponse {
			return acp.CreateElicitationResponse{Decline: &acp.CreateElicitationDecline{Action: "decline"}}
		}
		b, sid, _ := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Use the question tool once with a single free_text question asking for my favourite fruit. "+
				"Do not create or edit any file. If I skip it, say so in one sentence."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		needCalls(t, ed, "", 1)
		assert.Len(t, ed.elicitations(), 1)
		assert.NotEmpty(t, strings.TrimSpace(ed.resetText()), "the turn continues after a decline")
	}},
	{"cancel_mid_turn_then_continue", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, _ := r.start(t, ctx, ed)

		done := make(chan acp.PromptResponse, 1)
		failed := make(chan error, 1)
		go func() {
			resp, err := b.turn(ctx, sid,
				acp.TextBlock("Write a detailed 2000-word essay on the history of suspension bridges. Do not use any tools."))
			if err != nil {
				failed <- err
				return
			}
			done <- resp
		}()

		select {
		case <-ed.outputStarted():
		case err := <-failed:
			require.NoError(t, err)
		case <-ctx.Done():
			require.FailNow(t, "the model produced no output before the deadline")
		}
		require.NoError(t, b.conn.Cancel(ctx, acp.CancelNotification{SessionId: sid}))

		select {
		case resp := <-done:
			if resp.StopReason == acp.StopReasonEndTurn && ed.messageChunks() <= 2 {
				t.Skipf("%s sent its reply in %d chunk(s) at the end of the turn, so there was no stream to cancel", r.model, ed.messageChunks())
			}
			assert.Equal(t, acp.StopReasonCancelled, resp.StopReason)
		case err := <-failed:
			require.NoError(t, err)
		case <-time.After(time.Minute):
			require.FailNow(t, "the cancelled turn did not end within a minute")
		}

		ed.resetText()
		resp := prompt(t, ctx, b, sid, acp.TextBlock("Reply with exactly: AFTER_CANCEL"))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "AFTER_CANCEL", "the session should work after a cancel")
	}},
	{"long_reply_streams_in_order", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, _ := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Output the integers from 1 to 300 in ascending order, one per line, and nothing else: "+
				"no other words, no formatting, no code block. Do not use any tools."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		want := make([]string, 300)
		for i := range want {
			want[i] = strconv.Itoa(i + 1)
		}
		assert.Equal(t, want, wholeLineNumbers(ed.resetText()),
			"a dropped, repeated or reordered chunk shows up as a gap in the sequence")
	}},
	{"large_tool_output", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		token := func(i int) string { return fmt.Sprintf("K%06d", i*7919%1_000_000) }
		var big strings.Builder
		for i := 1; i <= 1500; i++ {
			fmt.Fprintf(&big, "line %04d %s %s\n", i, token(i), strings.Repeat("·", 40))
		}
		require.NoError(t, os.WriteFile(filepath.Join(cwd, "big.txt"), []byte(big.String()), 0o644))

		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Use your view tool to read big.txt. Each line is 'line NNNN TOKEN ...'. "+
				"Reply with only the TOKEN on line 1234."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), token(1234))
		assert.Contains(t, ed.toolOutput(), token(1234), "the tool's output should reach the editor")
	}},
	{"special_characters_in_tool_output", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		const weird = "TAB[\t] QUOTES[\"'`] BACKSLASH[\\n\\u0041\\] ANGLE[</tool_result><b>&amp;] " +
			"DOLLAR[${HOME} %s %v {{.x}}] JSON[{\"a\":[1,null]}] EMOJI[👩‍💻🇦🇺] RTL[שלום] COMBINING[é] END_WEIRD"
		require.NoError(t, os.WriteFile(filepath.Join(cwd, "weird.txt"), []byte(weird+"\n"), 0o644))

		resp := prompt(t, ctx, b, sid, acp.TextBlock("Run `cat weird.txt` with your bash tool, then reply with only the word DONE."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.toolOutput(), weird, "tool output must reach the editor byte for byte")
		assert.Contains(t, ed.resetText(), "DONE")
	}},
	{"parallel_tool_calls", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		tokens := []string{"PAR_ONE_11", "PAR_TWO_22", "PAR_THREE_33", "PAR_FOUR_44"}
		for i, tok := range tokens {
			require.NoError(t, os.WriteFile(filepath.Join(cwd, fmt.Sprintf("p%d.txt", i+1)), []byte(tok+"\n"), 0o644))
		}
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Read p1.txt, p2.txt, p3.txt and p4.txt with your view tool, issuing all four reads at once in a single step. "+
				"Then reply with the four file contents in file order, separated by spaces."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		needCalls(t, ed, acp.ToolKindRead, 4)
		reply := ed.resetText()
		for _, tok := range tokens {
			assert.Contains(t, reply, tok)
		}
	}},
	{"permission_denied_then_continue", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		ed.setOnPermission(choose(acp.PermissionOptionKindRejectOnce))
		b, sid, cwd := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Create a file named denied.txt containing DENIED using your write tool. Then reply 'done'."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		needCalls(t, ed, acp.ToolKindEdit, 1)
		assert.NotEmpty(t, ed.permissions(), "writing a file should ask for permission")
		assert.NoFileExists(t, filepath.Join(cwd, "denied.txt"), "a denied write must not happen")
		assert.Positive(t, ed.failedToolCalls(), "the denied call should be reported as failed")

		ed.setOnPermission(nil)
		ed.resetText()
		resp = prompt(t, ctx, b, sid, acp.TextBlock("Reply with exactly: AFTER_DENY"))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "AFTER_DENY", "the session should work after a denial")
	}},
	{"permission_allow_for_session", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		ed.setOnPermission(choose(acp.PermissionOptionKindAllowAlways))
		b, sid, cwd := r.start(t, ctx, ed)
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Using your bash tool, run `touch one.txt`, and then, as a second separate bash call, run `touch two.txt`. "+
				"Do not combine them into one command. Then reply 'done'."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		needCalls(t, ed, acp.ToolKindExecute, 2)
		assert.FileExists(t, filepath.Join(cwd, "one.txt"))
		assert.FileExists(t, filepath.Join(cwd, "two.txt"))
		assert.Len(t, ed.permissions(), 1, "allow for this session should cover the second touch")
	}},
	{"cancel_while_permission_pending", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		ed.setOnPermission(func(acp.RequestPermissionRequest) acp.RequestPermissionResponse {
			_ = b.conn.Cancel(ctx, acp.CancelNotification{SessionId: sid})
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
		})
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Create a file named cancelled.txt containing CANCELLED using your write tool. Then reply 'done'."))
		if len(ed.permissions()) == 0 {
			t.Skipf("%s made no tool call that needs permission, so there was nothing to cancel", r.model)
		}
		assert.Equal(t, acp.StopReasonCancelled, resp.StopReason)
		assert.NotEmpty(t, ed.permissions())
		assert.NoFileExists(t, filepath.Join(cwd, "cancelled.txt"), "a cancelled permission must not run the tool")

		ed.setOnPermission(nil)
		ed.resetText()
		resp = prompt(t, ctx, b, sid, acp.TextBlock("Reply with exactly: AFTER_CANCEL"))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "AFTER_CANCEL")
	}},
	{"cancel_while_question_pending", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
		})
		b, sid, _ := r.start(t, ctx, ed)
		ed.setOnElicit(func(acp.CreateElicitationRequest) acp.CreateElicitationResponse {
			_ = b.conn.Cancel(ctx, acp.CancelNotification{SessionId: sid})
			return acp.CreateElicitationResponse{Cancel: &acp.CreateElicitationCancel{Action: "cancel"}}
		})
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Use the question tool once with a single free_text question asking for my favourite fruit. "+
				"Do not create or edit any file."))
		if ed.elicitationCount() == 0 {
			t.Skipf("%s did not call the question tool, so there was nothing to cancel", r.model)
		}
		assert.Equal(t, acp.StopReasonCancelled, resp.StopReason)
		assert.Len(t, ed.elicitations(), 1)

		ed.resetText()
		resp = prompt(t, ctx, b, sid, acp.TextBlock("Reply with exactly: AFTER_QUESTION_CANCEL"))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "AFTER_QUESTION_CANCEL")
	}},
	{"concurrent_sessions_one_bridge", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b := r.launch(t, ctx, ed)
		sids := []acp.SessionId{
			r.session(t, ctx, b, smokeProject(t)).SessionId,
			r.session(t, ctx, b, smokeProject(t)).SessionId,
		}
		tokens := []string{"SESSION_AAA", "SESSION_BBB"}
		errs := make([]error, len(sids))
		stops := make([]acp.StopReason, len(sids))
		var wg sync.WaitGroup
		for i, sid := range sids {
			wg.Go(func() {
				resp, err := b.turn(ctx, sid, acp.TextBlock(
					"Write four lines about rivers, then end with a line saying exactly "+tokens[i]+". Do not use any tools."))
				errs[i], stops[i] = err, resp.StopReason
			})
		}
		wg.Wait()
		for i, sid := range sids {
			require.NoError(t, errs[i])
			assert.Equal(t, acp.StopReasonEndTurn, stops[i])
			text := ed.sessionText(sid)
			assert.Contains(t, text, tokens[i])
			assert.NotContains(t, text, tokens[1-i], "one session's output leaked into the other")
		}
	}},
	{"load_replays_tool_history", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		prompt(t, ctx, b, sid, acp.TextBlock(
			"Create notes.txt containing LOADED_77 using your write tool, then reply with exactly: WROTE_IT"))
		needCalls(t, ed, acp.ToolKindEdit, 1)
		needFile(t, ed, filepath.Join(cwd, "notes.txt"))
		b.close(t)

		replay := newSmokeEditor(acp.ClientCapabilities{})
		b2 := r.launch(t, ctx, replay)
		_, err := b2.load(ctx, acp.LoadSessionRequest{SessionId: sid, Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)
		assert.Contains(t, replay.text(), "WROTE_IT", "the replay should include the assistant's reply")
		assert.NotEmpty(t, replay.toolKinds(), "the replay should include the write tool call")

		replay.resetText()
		resp := prompt(t, ctx, b2, sid, acp.TextBlock("What token did you write into notes.txt? Reply with the token only, without using tools."))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, replay.resetText(), "LOADED_77", "a loaded session keeps its history")
	}},
	{"switch_model_mid_conversation", func(t *testing.T, ctx context.Context, r *modelRun) {
		if r.partner == "" {
			t.Skip("SMOKE_MODELS names one model; nothing to switch to")
		}
		ed := newSmokeEditor(acp.ClientCapabilities{})
		cwd := smokeProject(t)
		b := r.launch(t, ctx, ed)
		sess := r.session(t, ctx, b, cwd)
		sid := sess.SessionId
		prompt(t, ctx, b, sid, acp.TextBlock(
			"Remember the code word TULIP-93. Also create memo.txt containing exactly MEMO-31 with your write tool. Then reply 'ok'."))
		needCalls(t, ed, acp.ToolKindEdit, 1)
		needFile(t, ed, filepath.Join(cwd, "memo.txt"))

		sel := modelSelector(t, sess.ConfigOptions)
		set, err := b.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{SessionId: sid, ConfigId: sel.Id, Value: r.partner},
		})
		require.NoError(t, err, "switch to %s", r.partner)
		assert.Equal(t, r.partner, modelSelector(t, set.ConfigOptions).CurrentValue)

		ed.resetText()
		resp := prompt(t, ctx, b, sid, acp.TextBlock(
			"Without using any tools: what code word did I give you, and what did you write into memo.txt?"))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		reply := ed.resetText()
		assert.Contains(t, reply, "TULIP-93", "history should survive a switch to %s", r.partner)
		assert.Contains(t, reply, "MEMO-31", "tool history should survive a switch to %s", r.partner)
	}},
	{"resource_link_prompt", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, cwd := r.start(t, ctx, ed)
		linked := filepath.Join(cwd, "linked.txt")
		require.NoError(t, os.WriteFile(linked, []byte("LINK_TOKEN_4410\n"), 0o644))
		resp := prompt(t, ctx, b, sid,
			acp.TextBlock("Read the file I linked and reply with the token it contains, nothing else: "),
			acp.ResourceLinkBlock("linked.txt", "file://"+filepath.ToSlash(linked)))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "LINK_TOKEN_4410")
	}},
	{"long_prompt_needle", func(t *testing.T, ctx context.Context, r *modelRun) {
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b, sid, _ := r.start(t, ctx, ed)
		var long strings.Builder
		for i := range 400 {
			if i == 217 {
				long.WriteString("Note: the vault code is NEEDLE-6142.\n")
			}
			fmt.Fprintf(&long, "Paragraph %d: the river runs past the old mill, and the miller counts sacks of grain by lamplight.\n", i)
		}
		long.WriteString("\nWhat is the vault code mentioned above? Reply with the code only. Do not use any tools.")
		resp := prompt(t, ctx, b, sid, acp.TextBlock(long.String()))
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "NEEDLE-6142")
	}},
}

// needCalls skips the check when the model made fewer than n tool calls of
// kind (any kind when empty) than the check needs: the model ignored the
// prompt, so the bridge path under test never ran. Other checks catch a
// bridge that loses tool calls, through the stored-text and tool-call rules.
func needCalls(t *testing.T, ed *smokeEditor, kind acp.ToolKind, n int) {
	t.Helper()
	got := 0
	for _, k := range ed.toolKinds() {
		if kind == "" || k == kind {
			got++
		}
	}
	if got < n {
		t.Skipf("the model made %d %s tool call(s) where the check needs %d; tool calls seen: %v", got, cmp.Or(string(kind), "any"), n, ed.toolKinds())
	}
}

// needFile skips the check when the model's write failed, so path was never
// made, and fails it when the write reported success but path is missing.
func needFile(t *testing.T, ed *smokeEditor, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		return
	}
	if ed.failedToolCalls() > 0 {
		t.Skipf("the model's write failed, so %s was never made; tool calls seen: %v", filepath.Base(path), ed.toolKinds())
	}
	require.FileExists(t, path)
}

// wholeLineNumbers is every line of s that is only a number, ignoring any
// prose a model writes around the list.
func wholeLineNumbers(s string) []string {
	s = regexp.MustCompile(`(?s)<(reasoning|think)>.*?</(reasoning|think)>`).ReplaceAllString(s, "\n")
	matches := regexp.MustCompile(`(?m)^\s*(\d+)\s*$`).FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// choose answers every permission request with the option of the given kind.
func choose(kind acp.PermissionOptionKind) func(acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	return func(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
		for _, o := range r.Options {
			if o.Kind == kind {
				return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(o.OptionId)}
			}
		}
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	}
}

// smokeConfig configures a bridge subprocess launched by startSmokeBridge.
type smokeConfig struct {
	args   []string
	env    []string
	editor *smokeEditor
}

// smokeBridge wraps a running crush-acp-bridge process and its ACP connection.
type smokeBridge struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *syncBuffer
	conn   *acp.ClientSideConnection
	ed     *smokeEditor
	init   acp.InitializeResponse
	closed bool
}

// turn sends a prompt, telling the editor the turn is open while it runs.
func (b *smokeBridge) turn(ctx context.Context, sid acp.SessionId, blocks ...acp.ContentBlock) (acp.PromptResponse, error) {
	b.ed.beginTurn(sid)
	resp, err := b.conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: blocks})
	b.ed.endTurn(sid, resp.StopReason)
	return resp, err
}

// load loads a session, during which the bridge replays its history.
func (b *smokeBridge) load(ctx context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	b.ed.beginTurn(req.SessionId)
	resp, err := b.conn.LoadSession(ctx, req)
	b.ed.endTurn(req.SessionId, "")
	return resp, err
}

func startSmokeBridge(t *testing.T, ctx context.Context, cfg smokeConfig) *smokeBridge {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("..", "..", "dist", "crush-acp-bridge"))
	require.NoError(t, err)

	ed := cmp.Or(cfg.editor, newSmokeEditor(acp.ClientCapabilities{}))
	cmd := exec.CommandContext(t.Context(), bin, cfg.args...)
	if cfg.env != nil {
		cmd.Env = cfg.env
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	b := &smokeBridge{
		cmd:    cmd,
		stdin:  stdin,
		stderr: stderr,
		conn:   acp.NewClientSideConnection(ed, stdin, stdout),
		ed:     ed,
	}
	t.Cleanup(func() {
		if !b.closed {
			_ = b.stdin.Close()
			_ = b.cmd.Wait()
		}
		if t.Failed() && b.stderr.Len() > 0 {
			t.Logf("bridge stderr:\n%s", b.stderr.String())
		}
	})

	resp, err := b.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientCapabilities: ed.caps,
	})
	require.NoError(t, err, "bridge initialize failed; stderr:\n%s", b.stderr.String())
	b.init = resp
	return b
}

func (b *smokeBridge) close(t *testing.T) {
	t.Helper()
	if b.closed {
		return
	}
	b.closed = true
	_ = b.stdin.Close()
	err := b.cmd.Wait()
	assert.NoError(t, err, "bridge exited with error; stderr:\n%s", b.stderr.String())
}

// smokeEditor is a configurable ACP client used by smoke tests.
type smokeEditor struct {
	caps         acp.ClientCapabilities
	onPermission func(acp.RequestPermissionRequest) acp.RequestPermissionResponse

	mu           sync.Mutex
	buf          strings.Builder
	permReqs     []acp.RequestPermissionRequest
	diffContents []acp.ToolCallContentDiff
	elicitCount  int
	onElicit     func(acp.CreateElicitationRequest) acp.CreateElicitationResponse
	elicitReqs   []acp.CreateElicitationRequest
	toolTitles   []string
	kinds        []acp.ToolKind
	toolCalls    int
	failedCalls  int
	thoughts     int
	outputOnce   sync.Once
	output       chan struct{}

	texts       map[acp.SessionId]*strings.Builder
	toolOut     strings.Builder
	active      map[acp.SessionId]bool
	calls       map[acp.ToolCallId]trackedCall
	maxOpen     int
	violations  []string
	updateCount int
	chunks      int
	interrupted map[acp.SessionId]bool
}

// trackedCall is a tool call the editor has been told about, and its latest status.
type trackedCall struct {
	session acp.SessionId
	status  acp.ToolCallStatus
}

func newSmokeEditor(caps acp.ClientCapabilities) *smokeEditor {
	return &smokeEditor{
		caps:   caps,
		output: make(chan struct{}),
		texts:  map[acp.SessionId]*strings.Builder{},
		active: map[acp.SessionId]bool{},
		calls:  map[acp.ToolCallId]trackedCall{},

		interrupted: map[acp.SessionId]bool{},
	}
}

func (e *smokeEditor) violate(format string, args ...any) {
	e.violations = append(e.violations, fmt.Sprintf(format, args...))
}

func terminal(s acp.ToolCallStatus) bool {
	return s == acp.ToolCallStatusCompleted || s == acp.ToolCallStatusFailed
}

func (e *smokeEditor) openCalls() int {
	n := 0
	for _, c := range e.calls {
		if !terminal(c.status) {
			n++
		}
	}
	return n
}

// SessionUpdate records what the bridge streams and checks it against ACP's
// rules: turn content only inside a prompt or load, and tool call updates
// only for tool calls already announced.
func (e *smokeEditor) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	u := n.Update
	turnContent := u.AgentMessageChunk != nil || u.AgentThoughtChunk != nil || u.ToolCall != nil || u.ToolCallUpdate != nil
	if turnContent {
		e.updateCount++
		if !e.active[n.SessionId] {
			e.violate("session %s: %s arrived outside a prompt or load", n.SessionId, updateKind(u))
		}
	}
	if c := u.AgentMessageChunk; c != nil && c.Content.Text != nil {
		e.buf.WriteString(c.Content.Text.Text)
		if e.texts[n.SessionId] == nil {
			e.texts[n.SessionId] = &strings.Builder{}
		}
		e.texts[n.SessionId].WriteString(c.Content.Text.Text)
		e.chunks++
		e.outputOnce.Do(func() { close(e.output) })
	}
	if u.AgentThoughtChunk != nil {
		e.thoughts++
		e.outputOnce.Do(func() { close(e.output) })
	}
	if c := u.ToolCall; c != nil {
		e.toolCalls++
		e.kinds = append(e.kinds, c.Kind)
		if strings.TrimSpace(c.Title) == "" {
			e.violate("tool call %s has no title", c.ToolCallId)
		}
		if prev, ok := e.calls[c.ToolCallId]; ok && terminal(prev.status) {
			e.violate("tool call %s announced again after it finished", c.ToolCallId)
		}
		e.calls[c.ToolCallId] = trackedCall{session: n.SessionId, status: cmp.Or(c.Status, acp.ToolCallStatusPending)}
		e.maxOpen = max(e.maxOpen, e.openCalls())
		e.recordToolContent(c.Content)
	}
	if tu := u.ToolCallUpdate; tu != nil {
		call, known := e.calls[tu.ToolCallId]
		if !known {
			e.violate("update for unannounced tool call %s", tu.ToolCallId)
			call = trackedCall{session: n.SessionId, status: acp.ToolCallStatusPending}
		}
		if title, ok := tu.Title.Value(); ok {
			e.toolTitles = append(e.toolTitles, title)
		}
		if status, ok := tu.Status.Value(); ok {
			if terminal(call.status) && status != call.status {
				e.violate("tool call %s moved from %s to %s", tu.ToolCallId, call.status, status)
			}
			call.status = status
			if status == acp.ToolCallStatusFailed {
				e.failedCalls++
			}
		}
		e.calls[tu.ToolCallId] = call
		e.maxOpen = max(e.maxOpen, e.openCalls())
		if contents, ok := tu.Content.Value(); ok {
			e.recordToolContent(contents)
		}
	}
	return nil
}

func (e *smokeEditor) recordToolContent(contents []acp.ToolCallContent) {
	for _, c := range contents {
		if c.Diff != nil {
			e.diffContents = append(e.diffContents, *c.Diff)
		}
		if c.Content != nil && c.Content.Content.Text != nil {
			e.toolOut.WriteString(c.Content.Content.Text.Text)
			e.toolOut.WriteByte('\n')
		}
	}
}

func updateKind(u acp.SessionUpdate) string {
	switch {
	case u.AgentMessageChunk != nil:
		return "agent_message_chunk"
	case u.AgentThoughtChunk != nil:
		return "agent_thought_chunk"
	case u.ToolCall != nil:
		return "tool_call " + string(u.ToolCall.ToolCallId)
	case u.ToolCallUpdate != nil:
		return "tool_call_update " + string(u.ToolCallUpdate.ToolCallId)
	}
	return "update"
}

// beginTurn marks sid as being inside a prompt or load, when the bridge may stream turn content.
func (e *smokeEditor) beginTurn(sid acp.SessionId) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[sid] = true
}

// endTurn closes sid's turn. A turn that ended normally must leave none of its
// tool calls pending or in progress.
func (e *smokeEditor) endTurn(sid acp.SessionId, stop acp.StopReason) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[sid] = false
	if stop != acp.StopReasonEndTurn {
		if stop != "" {
			e.interrupted[sid] = true
		}
		return
	}
	for id, c := range e.calls {
		if c.session == sid && !terminal(c.status) {
			e.violate("tool call %s still %s when the turn ended", id, c.status)
		}
	}
}

func (e *smokeEditor) protocolViolations() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.violations...)
}

func (e *smokeEditor) sessionText(sid acp.SessionId) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if b := e.texts[sid]; b != nil {
		return b.String()
	}
	return ""
}

func (e *smokeEditor) messageChunks() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.chunks
}

// streamedText is what the editor was shown for sid, and whether every turn
// it saw there ran to its end, so the text should match what Crush stored.
func (e *smokeEditor) streamedText(sid acp.SessionId) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b := e.texts[sid]
	if b == nil {
		return "", false
	}
	return b.String(), !e.interrupted[sid]
}

func (e *smokeEditor) toolOutput() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.toolOut.String()
}

func (e *smokeEditor) setOnPermission(f func(acp.RequestPermissionRequest) acp.RequestPermissionResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onPermission = f
}

func (e *smokeEditor) setOnElicit(f func(acp.CreateElicitationRequest) acp.CreateElicitationResponse) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onElicit = f
}

func (e *smokeEditor) RequestPermission(_ context.Context, r acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	e.mu.Lock()
	e.permReqs = append(e.permReqs, r)
	if !e.active[r.SessionId] {
		e.violate("session %s: permission request outside a prompt", r.SessionId)
	}
	if _, ok := e.calls[r.ToolCall.ToolCallId]; !ok {
		e.violate("permission request for unannounced tool call %s", r.ToolCall.ToolCallId)
	}
	cb := e.onPermission
	e.mu.Unlock()

	if cb != nil {
		return cb(r), nil
	}
	for _, o := range r.Options {
		if o.Kind == acp.PermissionOptionKindAllowOnce {
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(o.OptionId)}, nil
		}
	}
	if len(r.Options) > 0 {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(r.Options[0].OptionId)}, nil
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
}

func (e *smokeEditor) CreateElicitation(_ context.Context, r acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
	e.mu.Lock()
	e.elicitCount++
	e.elicitReqs = append(e.elicitReqs, r)
	if r.Form != nil && r.Form.Session != nil && !e.active[r.Form.Session.SessionId] {
		e.violate("session %s: elicitation outside a prompt", r.Form.Session.SessionId)
	}
	cb := e.onElicit
	e.mu.Unlock()
	if cb != nil {
		return cb(r), nil
	}
	return acp.CreateElicitationResponse{}, errNo
}

func (*smokeEditor) CompleteElicitation(context.Context, acp.CompleteElicitationNotification) error {
	return errNo
}
func (*smokeEditor) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errNo
}
func (*smokeEditor) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errNo
}
func (*smokeEditor) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errNo
}
func (*smokeEditor) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errNo
}
func (*smokeEditor) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errNo
}
func (*smokeEditor) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errNo
}
func (*smokeEditor) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errNo
}

func (e *smokeEditor) text() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.buf.String()
}

func (e *smokeEditor) resetText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.buf.String()
	e.buf.Reset()
	return s
}

func (e *smokeEditor) elicitationCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.elicitCount
}

func (e *smokeEditor) elicitations() []acp.CreateElicitationRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]acp.CreateElicitationRequest(nil), e.elicitReqs...)
}

func (e *smokeEditor) toolCallTitles() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.toolTitles...)
}

func (e *smokeEditor) permissions() []acp.RequestPermissionRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]acp.RequestPermissionRequest(nil), e.permReqs...)
}

func (e *smokeEditor) diffs() []acp.ToolCallContentDiff {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]acp.ToolCallContentDiff(nil), e.diffContents...)
}

func (e *smokeEditor) toolKinds() []acp.ToolKind {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]acp.ToolKind(nil), e.kinds...)
}

func (e *smokeEditor) failedToolCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.failedCalls
}

// outputStarted is closed by the first agent message or thought chunk.
func (e *smokeEditor) outputStarted() <-chan struct{} { return e.output }

// stats summarises how the model went about the turn, so runs can be compared
// across models in the test log.
func (e *smokeEditor) stats() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("updates=%d message chunks=%d thought chunks=%d tool calls=%d (failed %d, max open %d, open now %d) kinds=%v permissions=%d elicitations=%d",
		e.updateCount, e.chunks, e.thoughts, e.toolCalls, e.failedCalls, e.maxOpen, e.openCalls(), e.kinds, len(e.permReqs), e.elicitCount)
}

// defaultSmokeModel runs the suite when SMOKE_MODELS is unset.
const defaultSmokeModel acp.SessionConfigValueId = "vertexai/gemini-4-hydrogen"

// smokeModels is the comma- or space-separated provider/model list in
// SMOKE_MODELS, or defaultSmokeModel. The first entry also runs the checks of
// bridge behaviour that do not depend on the model.
func smokeModels() []acp.SessionConfigValueId {
	fields := strings.FieldsFunc(os.Getenv("SMOKE_MODELS"), func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	if len(fields) == 0 {
		return []acp.SessionConfigValueId{defaultSmokeModel}
	}
	models := make([]acp.SessionConfigValueId, len(fields))
	for i, f := range fields {
		models[i] = acp.SessionConfigValueId(f)
	}
	return models
}

// smokeModelName keeps a model's subtest name to one -run path element.
func smokeModelName(m acp.SessionConfigValueId) string {
	return strings.ReplaceAll(string(m), "/", ":")
}

// smokeProject returns a fresh project directory holding
// testdata/smoke-models.crushrc as its crushrc and testdata/smoke-models.crush.json
// as its crush.json, so every model SMOKE_MODELS may name is registered
// whatever the developer's own Crush config holds. The JSON holds the
// providers whose API key is a shell command, which a crushrc stores literally.
func smokeProject(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	for src, dst := range map[string]string{"smoke-models.crushrc": "crushrc", "smoke-models.crush.json": "crush.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", src))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(cwd, dst), data, 0o644))
	}
	return cwd
}

// requireModelOffered skips the model's checks when Crush does not offer it,
// typically because its provider has no credentials on this machine, and
// fails them once when the model cannot finish a trivial turn.
func requireModelOffered(t *testing.T, serverURL string, model acp.SessionConfigValueId) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	b := startSmokeBridge(t, ctx, smokeConfig{args: []string{"--crush-url", serverURL}})
	defer b.close(t)
	sess, err := b.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: smokeProject(t), McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	sel := modelSelector(t, sess.ConfigOptions)
	if sel.Options.Ungrouped != nil {
		for _, o := range *sel.Options.Ungrouped {
			if o.Value == model {
				return
			}
		}
	}
	t.Skipf("Crush does not offer %s; is its provider configured with credentials?", model)
}

// requireModelWorks fails a model once, with Crush's error, when it cannot
// finish a trivial turn, instead of failing every check the same way.
func requireModelWorks(t *testing.T, serverURL string, model acp.SessionConfigValueId) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	b := startSmokeBridge(t, ctx, smokeConfig{args: []string{"--crush-url", serverURL}})
	defer b.close(t)
	sess := newSmokeSession(t, ctx, b.conn, smokeProject(t), model)
	_, err := b.conn.Prompt(ctx, acp.PromptRequest{SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("Reply with exactly: OK")}})
	require.NoError(t, err, "%s cannot finish a one-line turn; skipping its checks", model)
}

// newSmokeSession opens a session in cwd and selects model for its workspace
// through the bridge's model selector.
func newSmokeSession(t *testing.T, ctx context.Context, conn *acp.ClientSideConnection, cwd string, model acp.SessionConfigValueId) acp.NewSessionResponse {
	t.Helper()
	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	sel := modelSelector(t, sess.ConfigOptions)
	if sel.CurrentValue != model {
		_, err = conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{SessionId: sess.SessionId, ConfigId: sel.Id, Value: model},
		})
		require.NoError(t, err, "select %s", model)
	}
	return sess
}

// ensureCrushServer returns CRUSH_ACP_URL when set, or starts a temporary
// isolated crush server on a free loopback port for the duration of the test.
func ensureCrushServer(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("CRUSH_ACP_URL"); u != "" {
		return u
	}

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	baseURL := "http://" + addr
	cacheDir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "crush", "server", "--debug", "--host", baseURL)
	cmd.Env = append(os.Environ(),
		"CRUSH_CACHE_DIR="+cacheDir,
		"CRUSH_SERVER_IDLE_TIMEOUT=86400",
	)
	out := &syncBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		if t.Failed() && out.Len() > 0 {
			t.Logf("temporary crush server output:\n%s", out.String())
		}
		if t.Failed() {
			keepServerLogs(t, cacheDir)
		}
	})

	client := crush.New(baseURL)
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		return client.Ping(ctx) == nil
	}, 15*time.Second, 100*time.Millisecond, "temporary crush server did not become healthy:\n%s", out.String())
	return baseURL
}

// keepServerLogs copies the temporary server's logs out of its cache
// directory, which the test deletes, into dist/smoke-logs.
func keepServerLogs(t *testing.T, cacheDir string) {
	logs, _ := filepath.Glob(filepath.Join(cacheDir, "server-*", "*.log"))
	dir, err := filepath.Abs(filepath.Join("..", "..", "dist", "smoke-logs", time.Now().Format("20060102-150405")))
	if err != nil || len(logs) == 0 || os.MkdirAll(dir, 0o755) != nil {
		return
	}
	for _, l := range logs {
		if data, err := os.ReadFile(l); err == nil {
			_ = os.WriteFile(filepath.Join(dir, filepath.Base(l)), data, 0o644)
		}
	}
	t.Logf("temporary crush server logs kept in %s", dir)
}

// serverSessionIDs queries the Crush server's REST API for session IDs stored in cwd.
func serverSessionIDs(t *testing.T, ctx context.Context, baseURL, cwd string) []string {
	t.Helper()
	httpClient, reqBase := serverHTTPClient(t, baseURL)

	var workspaces []struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	getJSON(t, ctx, httpClient, reqBase+"/v1/workspaces", &workspaces)
	var wsID string
	for _, w := range workspaces {
		if w.Path == cwd {
			wsID = w.ID
			break
		}
	}
	require.NotEmpty(t, wsID, "workspace %q not found on server", cwd)

	var sessions []struct {
		ID string `json:"id"`
	}
	getJSON(t, ctx, httpClient, fmt.Sprintf("%s/v1/workspaces/%s/sessions", reqBase, wsID), &sessions)
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

func serverHTTPClient(t *testing.T, baseURL string) (*http.Client, string) {
	t.Helper()
	if sock, ok := strings.CutPrefix(baseURL, "unix://"); ok {
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		}
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{Transport: tr}, "http://unix"
	}
	return http.DefaultClient, strings.TrimRight(baseURL, "/")
}

func getJSON(t *testing.T, ctx context.Context, client *http.Client, targetURL string, out any) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.UnmarshalRead(resp.Body, out))
}

// dropOnceProxy fronts a Crush server and drops the first SSE /events stream
// once armed and a turn event arrives, verifying the bridge resubscribes.
type dropOnceProxy struct {
	server *httptest.Server
	armed  atomic.Bool

	mu      sync.Mutex
	dropped bool
	clients []string
}

func newDropOnceProxy(t *testing.T, upstream string) *dropOnceProxy {
	t.Helper()
	httpClient, reqBase := serverHTTPClient(t, upstream)
	parsed, err := url.Parse(reqBase)
	require.NoError(t, err)

	rp := httputil.NewSingleHostReverseProxy(parsed)
	rp.Transport = httpClient.Transport
	rp.FlushInterval = -1

	p := &dropOnceProxy{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events") {
			p.handleEvents(w, r, httpClient, reqBase)
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		p.server.CloseClientConnections()
		p.server.Close()
	})
	return p
}

func (p *dropOnceProxy) armDrop() { p.armed.Store(true) }

func (p *dropOnceProxy) clientIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.clients...)
}

func (p *dropOnceProxy) handleEvents(w http.ResponseWriter, r *http.Request, client *http.Client, reqBase string) {
	p.mu.Lock()
	p.clients = append(p.clients, r.URL.Query().Get("client_id"))
	p.mu.Unlock()

	target := reqBase + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			if p.armed.Load() && bytes.HasPrefix(line, []byte("data:")) {
				p.mu.Lock()
				already := p.dropped
				if !already {
					p.dropped = true
				}
				p.mu.Unlock()
				if !already {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func tinyRedPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	red := color.RGBA{R: 255, G: 0, B: 0, A: 255}
	for y := range 4 {
		for x := range 4 {
			img.Set(x, y, red)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// isolatedNoCredsEnv returns a minimal environment with isolated Crush config,
// data, and cache directories and no provider credentials.
func isolatedNoCredsEnv(home, cfgDir, dataDir, cacheDir string) []string {
	keep := []string{"PATH", "USER", "LOGNAME", "TMPDIR", "TEMP", "TMP", "TERM", "LANG", "LC_ALL", "SystemRoot", "COMSPEC"}
	env := make([]string, 0, len(keep)+5)
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env,
		"HOME="+home,
		"USERPROFILE="+home,
		"XDG_CONFIG_HOME="+cfgDir,
		"XDG_DATA_HOME="+dataDir,
		"CRUSH_CACHE_DIR="+cacheDir,
	)
}

func shortTempSocket(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("cs-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if runtime.GOOS == "windows" {
		return `\\.\pipe\` + name
	}
	sock := filepath.Join("/tmp", name)
	t.Cleanup(func() {
		_ = os.Remove(sock)
		_ = os.Remove(sock + ".lock")
	})
	return sock
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}
