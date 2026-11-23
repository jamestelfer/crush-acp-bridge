// Package bridge exposes a Crush server session as an ACP agent.
package bridge

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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
	Models(ctx context.Context) (crush.Models, error)
	SetModel(ctx context.Context, m crush.ModelRef) error
}

var _ Backend = (*crush.Workspace)(nil)

// Opener returns the Backend for the project in cwd. The bridge calls it once
// per directory and reuses the result.
type Opener func(ctx context.Context, cwd string) (Backend, error)

// Option configures Serve.
type Option func(*Agent)

// WithLogger sets the logger for bridge activity. The default discards logs.
func WithLogger(l *slog.Logger) Option {
	return func(a *Agent) { a.log = l }
}

// Serve runs the ACP agent over in and out until the peer disconnects or ctx
// ends. Each ACP session opens Crush in the directory the editor sends.
func Serve(ctx context.Context, open Opener, in io.Reader, out io.Writer, opts ...Option) error {
	a := &Agent{
		open:       open,
		workspaces: map[string]Backend{},
		sessions:   map[string]*session{},
		serveCtx:   ctx,
		log:        slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(a)
	}
	conn := acp.NewAgentSideConnection(a, out, in)
	conn.SetLogger(a.log) // the SDK's own diagnostics follow --log-format
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

	open     Opener
	conn     *acp.AgentSideConnection
	serveCtx context.Context
	log      *slog.Logger

	mu         sync.Mutex
	workspaces map[string]Backend // by directory
	sessions   map[string]*session
}

// session tracks one in-flight turn.
type session struct {
	backend Backend
	turn    chan acp.StopReason
	// active is set while a Prompt call waits for its turn. Turns started by
	// other clients (the Crush TUI) also end in this session; their ends must
	// not complete a later prompt.
	active atomic.Bool

	// sent is the text of the prompt this editor just submitted. Crush echoes
	// it back as a user message; the editor already shows it, so the echo is
	// dropped. Other clients' user messages are forwarded.
	sentMu sync.Mutex
	sent   string
}

// takeSent reports whether text is the echo of the editor's own prompt, and
// consumes the record if so.
func (s *session) takeSent(text string) bool {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	if s.sent == "" || s.sent != text {
		return false
	}
	s.sent = ""
	return true
}

func (s *session) setSent(text string) {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	s.sent = text
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
func (a *Agent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	backend, err := a.workspace(ctx, req.Cwd)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	id, err := backend.NewSession(ctx, "ACP")
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err := backend.SetCurrentSession(ctx, id); err != nil {
		return acp.NewSessionResponse{}, err
	}

	a.mu.Lock()
	a.sessions[id] = &session{backend: backend, turn: make(chan acp.StopReason, 1)}
	a.mu.Unlock()
	a.log.Info("session created", "session_id", id, "cwd", req.Cwd)
	return acp.NewSessionResponse{SessionId: acp.SessionId(id), ConfigOptions: a.sessionConfig(ctx, backend, id)}, nil
}

// Prompt runs one turn and returns when Crush reports it finished.
func (a *Agent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	s := a.session(string(req.SessionId))
	if s == nil {
		return acp.PromptResponse{}, fmt.Errorf("unknown session %q", req.SessionId)
	}

	s.active.Store(true)
	defer s.active.Store(false)
	select { // discard a signal left over from a turn that ended after its prompt gave up
	case <-s.turn:
	default:
	}

	var text strings.Builder
	for _, b := range req.Prompt {
		if b.Text != nil {
			text.WriteString(b.Text.Text)
		}
	}
	s.setSent(text.String())
	defer s.setSent("")
	began := time.Now()
	a.log.Info("prompt started", "session_id", string(req.SessionId))
	if err := s.backend.Prompt(ctx, string(req.SessionId), text.String()); err != nil {
		a.log.Error("prompt rejected by crush", "session_id", string(req.SessionId), "error", err)
		return acp.PromptResponse{}, err
	}

	select {
	case reason := <-s.turn:
		a.log.Info("prompt finished",
			"session_id", string(req.SessionId), "stop_reason", string(reason), "duration", time.Since(began))
		return acp.PromptResponse{StopReason: reason}, nil
	case <-ctx.Done():
		// The SDK cancels the prompt context on session/cancel. ACP expects a
		// normal response with the cancelled stop reason, sent after the
		// updates Crush still has in flight.
		select {
		case <-s.turn:
		case <-time.After(cancelGrace):
			a.log.Warn("crush did not confirm the cancelled turn", "session_id", string(req.SessionId))
		}
		a.log.Info("prompt finished",
			"session_id", string(req.SessionId), "stop_reason", string(acp.StopReasonCancelled), "duration", time.Since(began))
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
}

// cancelGrace bounds how long Prompt waits for Crush to report a cancelled
// turn before answering the editor anyway.
const cancelGrace = 2 * time.Second

// Cancel stops the running turn. The turn ends when Crush reports the
// cancelled finish, which makes Prompt return StopReasonCancelled.
func (a *Agent) Cancel(ctx context.Context, n acp.CancelNotification) error {
	s := a.session(string(n.SessionId))
	if s == nil {
		return fmt.Errorf("unknown session %q", n.SessionId)
	}
	a.log.Info("cancel requested", "session_id", string(n.SessionId))
	return s.backend.Cancel(ctx, string(n.SessionId))
}

func (a *Agent) session(id string) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// workspace returns the backend for cwd, opening it and starting its event
// loop on first use.
func (a *Agent) workspace(ctx context.Context, cwd string) (Backend, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if b, ok := a.workspaces[cwd]; ok {
		return b, nil
	}
	b, err := a.open(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cwd, err)
	}
	events, err := b.Events(a.serveCtx)
	if err != nil {
		return nil, err
	}
	a.workspaces[cwd] = b
	a.log.Info("workspace opened", "cwd", cwd)
	go a.pump(events, cwd)
	return b, nil
}

// pump translates Crush events and forwards them in order to the editor.
func (a *Agent) pump(events <-chan []byte, cwd string) {
	tr := translate.New()
	for data := range events {
		updates, err := tr.Feed(data)
		if err != nil {
			a.log.Warn("crush event not translated", "error", err)
			continue
		}
		for i := range updates {
			a.forward(&updates[i])
		}
	}
	if a.serveCtx.Err() != nil {
		a.log.Debug("crush event stream ended at shutdown", "cwd", cwd)
		return
	}
	a.log.Warn("crush event stream closed", "cwd", cwd)
}

// send delivers a session update to the editor. A failure is logged and does
// not stop the event loop: later updates may still get through.
func (a *Agent) send(sid acp.SessionId, update acp.SessionUpdate) {
	if err := a.conn.SessionUpdate(a.serveCtx, acp.SessionNotification{SessionId: sid, Update: update}); err != nil {
		a.log.Warn("session update not delivered", "session_id", string(sid), "error", err)
	}
}

func (a *Agent) forward(u *translate.Update) {
	s := a.session(u.SessionID)
	if s == nil {
		return
	}
	sid := acp.SessionId(u.SessionID)

	switch u.Kind {
	case translate.UserChunk:
		if s.takeSent(u.Text) {
			return
		}
		a.send(sid, acp.UpdateUserMessageText(u.Text))
	case translate.MessageChunk:
		a.send(sid, acp.UpdateAgentMessageText(u.Text))
	case translate.ThoughtChunk:
		a.send(sid, acp.UpdateAgentThoughtText(u.Text))
	case translate.ToolCallStarted:
		a.send(sid, acp.StartToolCall(
			acp.ToolCallId(u.ToolCallID), u.ToolName,
			acp.WithStartKind(toolKind(u.ToolName)),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
		))
	case translate.ToolCallInput:
		title, locs := describeTool(u.ToolName, u.RawInput)
		a.send(sid, acp.UpdateToolCall(
			acp.ToolCallId(u.ToolCallID),
			acp.WithUpdateTitle(title),
			acp.WithUpdateLocations(locs),
			acp.WithUpdateRawInput(u.RawInput),
		))
	case translate.ToolCallFinished:
		status := acp.ToolCallStatusCompleted
		if u.IsError {
			status = acp.ToolCallStatusFailed
		}
		a.send(sid, acp.UpdateToolCall(
			acp.ToolCallId(u.ToolCallID),
			acp.WithUpdateStatus(status),
			acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(u.Output))}),
		))
	case translate.PermissionRequested:
		// The editor may take minutes to answer; do not stall the event loop.
		go a.askPermission(s.backend, *u)
	case translate.TurnEnded:
		if !s.active.Load() {
			return
		}
		select {
		case s.turn <- acp.StopReason(u.StopReason):
		default:
		}
	}
}

// askPermission relays a Crush permission request to the editor and sends the
// decision back. Anything other than an explicit allow denies the request.
func (a *Agent) askPermission(backend Backend, u translate.Update) {
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
			Status:     acp.PatchSet(acp.ToolCallStatusPending),
			RawInput:   rawPatch(acp.ToolCallUpdate{}.RawInput, u.RawParams),
			Content: acp.PatchSet([]acp.ToolCallContent{
				acp.ToolContent(acp.TextBlock(permissionSummary(u.RawParams, title))),
			}),
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
	if err != nil {
		a.log.Warn("permission request failed", "error", err, "tool", u.ToolName, "session_id", u.SessionID)
	}
	a.log.Info("permission decided",
		"action", string(action), "tool", u.ToolName, "session_id", u.SessionID)
	if err := backend.Grant(a.serveCtx, u.RawPermission, action); err != nil {
		a.log.Warn("permission grant failed",
			"error", err, "action", string(action), "tool", u.ToolName, "session_id", u.SessionID)
	}
}
