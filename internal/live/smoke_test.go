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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
func TestSmoke(t *testing.T) {
	serverURL := ensureCrushServer(t)

	t.Run("session_lifecycle_and_probe_cleanup", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cwd := t.TempDir()

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
		liveSess, err := b.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)
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

	t.Run("modes_and_edit_diff", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cwd := t.TempDir()
		target := filepath.Join(cwd, "greeting.txt")
		require.NoError(t, os.WriteFile(target, []byte("hello world\n"), 0o644))

		ed := newSmokeEditor(acp.ClientCapabilities{})
		b := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--crush-url", serverURL},
			editor: ed,
		})
		defer b.close(t)

		sess, err := b.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)

		// Round-trip mode switching (coder -> plan -> coder).
		_, err = b.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sess.SessionId, ModeId: "plan"})
		require.NoError(t, err)
		_, err = b.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sess.SessionId, ModeId: "coder"})
		require.NoError(t, err)

		resp, err := b.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sess.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("Edit greeting.txt to replace 'hello' with 'goodbye'. Then reply 'done'.")},
		})
		require.NoError(t, err)
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.NotEmpty(t, ed.permissions(), "editing a file should ask for permission")
		assert.NotEmpty(t, ed.diffs(), "edit updates should include ACP diff content")

		got, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "goodbye world\n", string(got))
	})

	t.Run("attachments_and_validation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cwd := t.TempDir()

		ed := newSmokeEditor(acp.ClientCapabilities{})
		b := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--crush-url", serverURL},
			editor: ed,
		})
		defer b.close(t)

		sess, err := b.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)

		// Unsupported audio blocks should be refused cleanly without breaking the session.
		_, err = b.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sess.SessionId,
			Prompt:    []acp.ContentBlock{acp.TextBlock("listen"), acp.AudioBlock(base64.StdEncoding.EncodeToString([]byte("ogg")), "audio/ogg")},
		})
		reqErr, ok := errors.AsType[*acp.RequestError](err)
		require.True(t, ok, "audio block should return an ACP RequestError, got %v", err)
		assert.Equal(t, acp.NewInvalidParams(nil).Code, reqErr.Code)

		// Send a valid 1x1 PNG plus an embedded text resource.
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

		resp, err := b.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sess.SessionId,
			Prompt: []acp.ContentBlock{
				acp.TextBlock("Read the attached note and repeat the exact secret_token value from it, along with the colour of the attached image."),
				note,
				img,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.Contains(t, ed.resetText(), "SMOKE_ATTACH_7391")
	})

	t.Run("questions_permission_and_elicitation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cwd := t.TempDir()

		caps := acp.ClientCapabilities{
			Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
		}
		ed := newSmokeEditor(caps)
		ed.onPermission = func(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
			for _, o := range r.Options {
				if strings.Contains(strings.ToLower(o.Name), "alpha") {
					return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(o.OptionId)}
				}
			}
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(r.Options[0].OptionId)}
		}
		ed.onElicit = func(r acp.CreateElicitationRequest) acp.CreateElicitationResponse {
			if f := r.Form.RequestedSchema.Properties["answer"]; f.Array != nil && f.Array.Items.Titled != nil {
				ids := make([]string, 0, len(f.Array.Items.Titled.AnyOf))
				for _, o := range f.Array.Items.Titled.AnyOf {
					ids = append(ids, o.Const)
				}
				return acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{
					Action:  "accept",
					Content: map[string]acp.ElicitationContentValue{"answer": acp.NewElicitationContentValueStringArray(ids...)},
				}}
			}
			return acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{
				Action:  "accept",
				Content: map[string]acp.ElicitationContentValue{"answer": acp.NewElicitationContentValueString("MANGO42")},
			}}
		}

		b := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--crush-url", serverURL},
			editor: ed,
		})
		defer b.close(t)

		sess, err := b.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)

		resp, err := b.conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sess.SessionId,
			Prompt: []acp.ContentBlock{acp.TextBlock(
				"Use the question tool once with a single batch of two questions: " +
					"a single_choice question offering Alpha or Beta, " +
					"and a free_text question asking for a code word. " +
					"Do not create or edit any file. After I answer, reply with both answers.",
			)},
		})
		require.NoError(t, err)
		assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		assert.NotEmpty(t, ed.permissions(), "single_choice question should arrive as a permission request")
		assert.NotEmpty(t, ed.elicitations(), "free_text question should arrive as a form elicitation")

		reply := ed.resetText()
		assert.Contains(t, reply, "MANGO42")
		assert.Contains(t, strings.ToLower(reply), "alpha")
	})

	t.Run("resubscribe_on_stream_drop", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cwd := t.TempDir()

		proxy := newDropOnceProxy(t, serverURL)
		ed := newSmokeEditor(acp.ClientCapabilities{})
		b := startSmokeBridge(t, ctx, smokeConfig{
			args:   []string{"--crush-url", proxy.server.URL},
			editor: ed,
		})
		defer b.close(t)

		sess, err := b.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)

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
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cwd := t.TempDir()
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

		s1, err := b1.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)
		s2, err := b2.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
		require.NoError(t, err)

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
	init   acp.InitializeResponse
	closed bool
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
	onElicit     func(acp.CreateElicitationRequest) acp.CreateElicitationResponse

	mu           sync.Mutex
	buf          strings.Builder
	permReqs     []acp.RequestPermissionRequest
	elicitReqs   []acp.CreateElicitationRequest
	diffContents []acp.ToolCallContentDiff
}

func newSmokeEditor(caps acp.ClientCapabilities) *smokeEditor {
	return &smokeEditor{caps: caps}
}

func (e *smokeEditor) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c := n.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
		e.buf.WriteString(c.Content.Text.Text)
	}
	if u := n.Update.ToolCallUpdate; u != nil {
		if contents, ok := u.Content.Value(); ok {
			for _, c := range contents {
				if c.Diff != nil {
					e.diffContents = append(e.diffContents, *c.Diff)
				}
			}
		}
	}
	return nil
}

func (e *smokeEditor) RequestPermission(_ context.Context, r acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	e.mu.Lock()
	e.permReqs = append(e.permReqs, r)
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
	e.elicitReqs = append(e.elicitReqs, r)
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

func (e *smokeEditor) permissions() []acp.RequestPermissionRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]acp.RequestPermissionRequest(nil), e.permReqs...)
}

func (e *smokeEditor) elicitations() []acp.CreateElicitationRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]acp.CreateElicitationRequest(nil), e.elicitReqs...)
}

func (e *smokeEditor) diffs() []acp.ToolCallContentDiff {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]acp.ToolCallContentDiff(nil), e.diffContents...)
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
	cmd := exec.CommandContext(t.Context(), "crush", "server", "--host", baseURL)
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
	})

	client := crush.New(baseURL)
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		return client.Ping(ctx) == nil
	}, 15*time.Second, 100*time.Millisecond, "temporary crush server did not become healthy:\n%s", out.String())
	return baseURL
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
