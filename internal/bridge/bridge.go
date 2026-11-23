// Package bridge exposes a Crush server session as an ACP agent.
package bridge

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// Backend is the part of a Crush workspace the bridge uses. *crush.Workspace
// satisfies it.
type Backend interface {
	NewSession(ctx context.Context, title string) (string, error)
	SetCurrentSession(ctx context.Context, sessionID string) error
	Prompt(ctx context.Context, sessionID, text string) error
	Cancel(ctx context.Context, sessionID string) error
	Grant(ctx context.Context, permission jsontext.Value, action crush.Action) error
	Events(ctx context.Context) (<-chan []byte, error)
}

var _ Backend = (*crush.Workspace)(nil)

// Serve runs the ACP agent over in and out until the peer disconnects or ctx
// ends.
func Serve(ctx context.Context, backend Backend, in io.Reader, out io.Writer) error {
	a := &Agent{backend: backend, sessions: map[string]*session{}, serveCtx: ctx}
	conn := acp.NewAgentSideConnection(a, out, in)
	a.conn = conn

	select {
	case <-conn.Done():
	case <-ctx.Done():
	}
	return ctx.Err()
}

// Agent implements acp.Agent on top of a Backend.
type Agent struct {
	unsupported

	backend  Backend
	conn     *acp.AgentSideConnection
	serveCtx context.Context

	startEvents sync.Once
	eventsErr   error

	mu       sync.Mutex
	sessions map[string]*session
}

// session tracks one in-flight turn.
type session struct {
	turn chan acp.StopReason
}

var _ acp.Agent = (*Agent)(nil)

// Initialize negotiates capabilities.
func (a *Agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "crush-acp-bridge", Version: "0.0.0"},
	}, nil
}

// NewSession creates a Crush session and starts following its events.
func (a *Agent) NewSession(ctx context.Context, _ acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := a.ensureEvents(); err != nil {
		return acp.NewSessionResponse{}, err
	}
	id, err := a.backend.NewSession(ctx, "ACP")
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err := a.backend.SetCurrentSession(ctx, id); err != nil {
		return acp.NewSessionResponse{}, err
	}

	a.mu.Lock()
	a.sessions[id] = &session{turn: make(chan acp.StopReason, 1)}
	a.mu.Unlock()
	return acp.NewSessionResponse{SessionId: acp.SessionId(id)}, nil
}

// Prompt runs one turn and returns when Crush reports it finished.
func (a *Agent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	s := a.session(string(req.SessionId))
	if s == nil {
		return acp.PromptResponse{}, fmt.Errorf("unknown session %q", req.SessionId)
	}

	var text strings.Builder
	for _, b := range req.Prompt {
		if b.Text != nil {
			text.WriteString(b.Text.Text)
		}
	}
	if err := a.backend.Prompt(ctx, string(req.SessionId), text.String()); err != nil {
		return acp.PromptResponse{}, err
	}

	select {
	case reason := <-s.turn:
		return acp.PromptResponse{StopReason: reason}, nil
	case <-ctx.Done():
		// The SDK cancels the prompt context on session/cancel. ACP expects a
		// normal response with the cancelled stop reason, sent after the
		// updates Crush still has in flight.
		select {
		case <-s.turn:
		case <-time.After(cancelGrace):
		}
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
}

// cancelGrace bounds how long Prompt waits for Crush to report a cancelled
// turn before answering the editor anyway.
const cancelGrace = 2 * time.Second

// Cancel stops the running turn. The turn ends when Crush reports the
// cancelled finish, which makes Prompt return StopReasonCancelled.
func (a *Agent) Cancel(ctx context.Context, n acp.CancelNotification) error {
	return a.backend.Cancel(ctx, string(n.SessionId))
}

func (a *Agent) session(id string) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// ensureEvents starts the single event loop shared by all sessions.
func (a *Agent) ensureEvents() error {
	a.startEvents.Do(func() {
		events, err := a.backend.Events(a.serveCtx)
		if err != nil {
			a.eventsErr = err
			return
		}
		go a.pump(events)
	})
	return a.eventsErr
}

// pump translates Crush events and forwards them in order to the editor.
func (a *Agent) pump(events <-chan []byte) {
	tr := translate.New()
	for data := range events {
		updates, err := tr.Feed(data)
		if err != nil {
			continue
		}
		for i := range updates {
			a.forward(&updates[i])
		}
	}
}

func (a *Agent) forward(u *translate.Update) {
	s := a.session(u.SessionID)
	if s == nil {
		return
	}
	sid := acp.SessionId(u.SessionID)

	switch u.Kind {
	case translate.MessageChunk:
		_ = a.conn.SessionUpdate(a.serveCtx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateAgentMessageText(u.Text)})
	case translate.ThoughtChunk:
		_ = a.conn.SessionUpdate(a.serveCtx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateAgentThoughtText(u.Text)})
	case translate.ToolCallStarted:
		_ = a.conn.SessionUpdate(a.serveCtx, acp.SessionNotification{SessionId: sid, Update: acp.StartToolCall(
			acp.ToolCallId(u.ToolCallID), u.ToolName,
			acp.WithStartKind(toolKind(u.ToolName)),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
		)})
	case translate.ToolCallFinished:
		status := acp.ToolCallStatusCompleted
		if u.IsError {
			status = acp.ToolCallStatusFailed
		}
		_ = a.conn.SessionUpdate(a.serveCtx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateToolCall(
			acp.ToolCallId(u.ToolCallID),
			acp.WithUpdateStatus(status),
			acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(u.Output))}),
		)})
	case translate.PermissionRequested:
		// The editor may take minutes to answer; do not stall the event loop.
		go a.askPermission(*u)
	case translate.TurnEnded:
		select {
		case s.turn <- acp.StopReason(u.StopReason):
		default:
		}
	}
}

// toolKind maps a Crush tool name to the ACP tool kind editors use for icons
// and grouping.
func toolKind(name string) acp.ToolKind {
	switch name {
	case "bash":
		return acp.ToolKindExecute
	case "edit", "multiedit", "write", "lsp_replace_symbol":
		return acp.ToolKindEdit
	case "view", "ls", "glob":
		return acp.ToolKindRead
	case "grep", "sourcegraph":
		return acp.ToolKindSearch
	case "fetch", "agentic_fetch", "download":
		return acp.ToolKindFetch
	default:
		return acp.ToolKindOther
	}
}

// askPermission relays a Crush permission request to the editor and sends the
// decision back. Anything other than an explicit allow denies the request.
func (a *Agent) askPermission(u translate.Update) {
	const (
		allowOnce   acp.PermissionOptionId = "allow_once"
		allowAlways acp.PermissionOptionId = "allow_always"
		rejectOnce  acp.PermissionOptionId = "reject_once"
	)

	title := u.Description
	if title == "" {
		title = u.ToolName
	}
	resp, err := a.conn.RequestPermission(a.serveCtx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(u.SessionID),
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(u.ToolCallID),
			Title:      acp.PatchSet(title),
			Kind:       acp.PatchSet(toolKind(u.ToolName)),
		},
		Options: []acp.PermissionOption{
			{OptionId: allowOnce, Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: allowAlways, Name: "Allow for this session", Kind: acp.PermissionOptionKindAllowAlways},
			{OptionId: rejectOnce, Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
		},
	})

	action := crush.Deny
	if err == nil && resp.Outcome.Selected != nil {
		switch resp.Outcome.Selected.OptionId {
		case allowOnce:
			action = crush.Allow
		case allowAlways:
			action = crush.AllowForSession
		}
	}
	_ = a.backend.Grant(a.serveCtx, u.RawPermission, action)
}
