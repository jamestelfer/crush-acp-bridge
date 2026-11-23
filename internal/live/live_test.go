//go:build live

// Package live drives the built bridge binary against a real Crush server.
//
// Run with `just live`. It needs a running `crush server`, a configured
// provider, and these environment variables:
//
//	CRUSH_ACP_URL  server base URL
//	LIVE_CWD       empty project directory the agent may write to
package live_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"
)

type editor struct {
	mu       sync.Mutex
	text     strings.Builder
	toolKind []acp.ToolKind
	asked    int
}

func (e *editor) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c := n.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
		e.text.WriteString(c.Content.Text.Text)
	}
	if tc := n.Update.ToolCall; tc != nil {
		e.toolKind = append(e.toolKind, tc.Kind)
	}
	return nil
}

func (e *editor) RequestPermission(_ context.Context, r acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	e.mu.Lock()
	e.asked++
	e.mu.Unlock()
	for _, o := range r.Options {
		if o.Kind == acp.PermissionOptionKindAllowOnce {
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(o.OptionId)}, nil
		}
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
}

func (e *editor) reset() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.text.String()
	e.text.Reset()
	return s
}

var errNo = errors.New("not supported by this test editor")

func (*editor) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errNo
}
func (*editor) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errNo
}
func (*editor) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errNo
}
func (*editor) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errNo
}
func (*editor) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errNo
}
func (*editor) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errNo
}
func (*editor) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errNo
}
func (*editor) CreateElicitation(context.Context, acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
	return acp.CreateElicitationResponse{}, errNo
}
func (*editor) CompleteElicitation(context.Context, acp.CompleteElicitationNotification) error {
	return errNo
}

func TestLiveSession(t *testing.T) {
	url, cwd := os.Getenv("CRUSH_ACP_URL"), os.Getenv("LIVE_CWD")
	if url == "" || cwd == "" {
		t.Skip("CRUSH_ACP_URL and LIVE_CWD must be set")
	}

	bin, err := filepath.Abs(filepath.Join("..", "..", "dist", "crush-acp-bridge"))
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), bin, "--crush-url", url, "--cwd", cwd)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })

	ed := &editor{}
	conn := acp.NewClientSideConnection(ed, stdin, stdout)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()

	_, err = conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	require.NoError(t, err)
	sess, err := conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	require.NoError(t, err)

	say := func(text string) acp.PromptResponse {
		resp, err := conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(text)},
		})
		require.NoError(t, err)
		return resp
	}

	t.Run("tool use with permission", func(t *testing.T) {
		resp := say("Run the shell command 'echo live > live.txt' and then say done.")

		require.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
		require.Equal(t, 1, ed.asked, "bash needs one permission")
		require.Contains(t, ed.toolKind, acp.ToolKindExecute)
		require.NotEmpty(t, strings.TrimSpace(ed.reset()))
		got, err := os.ReadFile(filepath.Join(cwd, "live.txt"))
		require.NoError(t, err, "the approved command must have run")
		require.Equal(t, "live\n", string(got))
	})

	t.Run("ten turns without losing events", func(t *testing.T) {
		for i := 1; i <= 10; i++ {
			word := fmt.Sprintf("turn%dok", i)
			resp := say("Reply with exactly this word and nothing else: " + word)

			require.Equal(t, acp.StopReasonEndTurn, resp.StopReason, "turn %d", i)
			require.Contains(t, ed.reset(), word, "turn %d lost its text", i)
		}
	})

	t.Run("server holds the whole conversation", func(t *testing.T) {
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

		var msgs []map[string]any
		get(t, fmt.Sprintf("%s/v1/workspaces/%s/sessions/%s/messages", url, wsID, sess.SessionId), &msgs)
		require.GreaterOrEqual(t, len(msgs), 22, "11 prompts and 11 replies, plus tool messages")
	})
}

func get(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // test against a local server
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.UnmarshalRead(resp.Body, out))
}
