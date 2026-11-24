// Package bridge exposes a Crush server session as an ACP agent.
package bridge

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
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
	DeleteSession(ctx context.Context, sessionID string) error
	SetCurrentSession(ctx context.Context, sessionID string) error
	Prompt(ctx context.Context, sessionID string, p crush.Prompt) error
	Cancel(ctx context.Context, sessionID string) error
	Grant(ctx context.Context, permission jsontext.Value, action crush.Action) error
	AnswerQuestions(ctx context.Context, batchID string, answers []crush.QuestionAnswer) (bool, error)
	CancelQuestions(ctx context.Context, batchID string) (bool, error)
	Events(ctx context.Context) (<-chan []byte, error)
	Ping(ctx context.Context) error
	Models(ctx context.Context) (crush.Models, error)
	SetModel(ctx context.Context, m crush.ModelRef) error
	SetMainAgent(ctx context.Context, agentID string) error
	Sessions(ctx context.Context) ([]crush.Session, error)
	Messages(ctx context.Context, sessionID string) ([]jsontext.Value, error)
}

var _ Backend = (*crush.Workspace)(nil)

// Opener returns the Backend for the project in cwd. The bridge calls it once
// per directory and reuses the result, and again to reconnect after the
// directory's event stream ends and the server no longer serves the
// workspace.
type Opener func(ctx context.Context, cwd string) (Backend, error)

// Option configures Serve.
type Option func(*Agent)

// WithLogger sets the logger for bridge activity. The default discards logs.
func WithLogger(l *slog.Logger) Option {
	return func(a *Agent) { a.log = l }
}

// Serve runs the ACP agent over in and out until the peer disconnects or ctx
// ends. Each ACP session opens Crush in the directory the editor sends. Before
// it returns, Serve deletes the sessions it created that are still empty.
func Serve(ctx context.Context, open Opener, in io.Reader, out io.Writer, opts ...Option) error {
	a := &Agent{
		open:       open,
		workspaces: map[string]*workspace{},
		reconnects: map[string]chan struct{}{},
		modes:      map[string]acp.SessionModeId{},
		sessions:   map[string]*session{},
		asking:     map[string]bool{},
		serveCtx:   ctx,
		log:        slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(a)
	}
	// The library starts reading in, and dispatching requests to a, before
	// NewAgentSideConnection returns. The gate holds its reads until the
	// connection is wired, so no handler sees a half-built connection.
	gate := make(chan struct{})
	conn := acp.NewAgentSideConnection(a, out, gatedReader{r: in, open: gate})
	conn.SetLogger(a.log) // the SDK's own diagnostics follow --log-format
	a.conn = conn
	close(gate)

	// Library and bridge goroutines may still log after Serve returns: the
	// library logs the disconnect after Done closes, and event loops log as
	// they stop. The log destination must tolerate concurrent writes.
	select {
	case <-conn.Done():
	case <-ctx.Done():
	}
	// ctx may already be cancelled by a signal; clean-up still gets a bounded
	// chance to reach Crush.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
	defer cancel()
	a.deleteEmpty(cleanup)
	return ctx.Err()
}

// cleanupWait bounds how long Serve spends deleting empty sessions.
const cleanupWait = 5 * time.Second

// gatedReader holds every Read until open closes.
type gatedReader struct {
	r    io.Reader
	open <-chan struct{}
}

func (g gatedReader) Read(p []byte) (int, error) {
	<-g.open
	return g.r.Read(p)
}

// Agent implements acp.Agent on top of a Backend.
type Agent struct {
	unsupported

	open     Opener
	conn     *acp.AgentSideConnection
	serveCtx context.Context
	log      *slog.Logger

	mu         sync.Mutex
	workspaces map[string]*workspace        // by directory
	reconnects map[string]chan struct{}     // closed when a directory reconnects
	modes      map[string]acp.SessionModeId // Crush's active agent, by directory
	sessions   map[string]*session
	asking     map[string]bool // permission requests and question batches the editor is answering, by ID

	// formElicitation is set when the editor advertises form elicitation, so
	// a free-text question can take a typed answer.
	formElicitation atomic.Bool

	// closing is set once Serve starts deleting empty sessions. From then on
	// no prompt starts and no session is created, so none is deleted after
	// the editor used it or escapes clean-up. creating counts the NewSession
	// calls already past that check.
	closing  bool
	creating sync.WaitGroup
}

// errClosing refuses work that arrives while the bridge shuts down.
var errClosing = internalError("the bridge is shutting down")

// workspace is a directory the bridge has open, or is opening.
type workspace struct {
	ready chan struct{} // closed when the first open finishes
	err   error         // why the first open failed; set before ready closes
	be    Backend       // nil until open; replaced on reconnect; guarded by Agent.mu

	// tr records what the editor has been sent for the directory's messages,
	// across reconnects and session loads. trMu is held while an update is
	// translated and forwarded, so the editor sees them in order.
	trMu sync.Mutex
	tr   *translate.Translator
}

// session tracks one in-flight turn.
type session struct {
	cwd  string
	turn chan turnEnd

	// created is set when this bridge created the session in Crush, and
	// prompted once the editor sends it a prompt. Serve deletes a session it
	// created and never prompted if Crush still holds no messages for it.
	created  bool
	prompted atomic.Bool
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

// turnEnd is how a turn finished: a stop reason from Crush, or an error when
// the bridge lost track of it.
type turnEnd struct {
	reason acp.StopReason
	err    error
}

// errStreamLost ends a turn whose event stream dropped: its end will never
// arrive.
var errStreamLost = errors.New("lost the connection to crush during the turn; the bridge is reconnecting, so send the prompt again")

// end completes the session's waiting turn. It does nothing when no prompt
// waits, or when an end is already pending.
func (s *session) end(e turnEnd) {
	if !s.active.Load() {
		return
	}
	select {
	case s.turn <- e:
	default:
	}
}

// internalError is a JSON-RPC internal error carrying msg for the editor.
func internalError(msg string) error {
	return acp.NewRequestError(-32603, msg, nil)
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

// pendingSent returns the text of the editor's prompt while Crush has not yet
// echoed it back.
func (s *session) pendingSent() string {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return s.sent
}

func (s *session) setSent(text string) {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	s.sent = text
}

var _ acp.Agent = (*Agent)(nil)

// Initialize negotiates capabilities.
func (a *Agent) Initialize(_ context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	e := req.ClientCapabilities.Elicitation
	a.formElicitation.Store(e != nil && e.Form != nil)
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:       &acp.Implementation{Name: "crush-acp-bridge", Version: "0.0.0"},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: promptCapabilities,
			SessionCapabilities: acp.SessionCapabilities{
				List:   &acp.SessionListCapabilities{},
				Resume: &acp.SessionResumeCapabilities{},
			},
		},
	}, nil
}

// NewSession creates a Crush session and starts following its events.
func (a *Agent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return acp.NewSessionResponse{}, errClosing
	}
	a.creating.Add(1)
	a.mu.Unlock()
	defer a.creating.Done()

	backend, err := a.workspace(ctx, req.Cwd)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	id, err := backend.NewSession(ctx, "ACP")
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	modes, err := a.attach(ctx, backend, id, req.Cwd)
	if err != nil {
		// The editor never learns the ID, so nothing can use the session.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
		defer cancel()
		if derr := backend.DeleteSession(dctx, id); derr != nil {
			a.log.Warn("empty session not deleted", "session_id", id, "cwd", req.Cwd, "error", derr)
		}
		return acp.NewSessionResponse{}, err
	}
	a.mu.Lock()
	a.sessions[id].created = true
	closing := a.closing
	a.mu.Unlock()
	if closing {
		// Clean-up waits for this call, then deletes the session.
		return acp.NewSessionResponse{}, errClosing
	}
	a.log.Info("session created", "session_id", id, "cwd", req.Cwd)
	return acp.NewSessionResponse{
		SessionId:     acp.SessionId(id),
		ConfigOptions: a.sessionConfig(ctx, backend, id),
		Modes:         modes,
	}, nil
}

// attach reports the session to Crush as the one this client views, and
// starts tracking it. It returns the directory's mode state.
func (a *Agent) attach(ctx context.Context, backend Backend, id, cwd string) (*acp.SessionModeState, error) {
	if err := backend.SetCurrentSession(ctx, id); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sessions[id]; !ok {
		a.sessions[id] = &session{cwd: cwd, turn: make(chan turnEnd, 1)}
	}
	return modeState(a.modeLocked(cwd)), nil
}

// Prompt runs one turn and returns when Crush reports it finished.
func (a *Agent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	p, err := crushPrompt(req.Prompt)
	if err != nil {
		return acp.PromptResponse{}, a.refuseContent(req.SessionId, err)
	}
	// Nothing reaches Crush until startPrompt, so a prompt that fails before
	// it leaves an empty session eligible for clean-up.
	s, err := a.lookup(req.SessionId)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.awaitConnected(ctx, s.cwd); err != nil {
		return acp.PromptResponse{}, err
	}
	if err := a.checkVision(ctx, a.backend(s.cwd), p); err != nil {
		return acp.PromptResponse{}, a.refuseContent(req.SessionId, err)
	}
	if s, err = a.startPrompt(req.SessionId); err != nil {
		return acp.PromptResponse{}, err
	}
	// sent is set before active, so a catch-up that sees the turn active
	// also sees which prompt it waits for. Crush stores the prompt text
	// without its attachments.
	s.setSent(p.Text)
	defer s.setSent("")
	s.active.Store(true)
	defer s.active.Store(false)
	select { // discard a signal left over from a turn that ended after its prompt gave up
	case <-s.turn:
	default:
	}
	began := time.Now()
	a.log.Info("prompt started", "session_id", string(req.SessionId))
	if err := a.backend(s.cwd).Prompt(ctx, string(req.SessionId), p); err != nil {
		a.log.Error("prompt rejected by crush", "session_id", string(req.SessionId), "error", err)
		return acp.PromptResponse{}, err
	}

	select {
	case end := <-s.turn:
		if end.err != nil {
			a.log.Error("prompt failed", "session_id", string(req.SessionId), "error", end.err, "duration", time.Since(began))
			return acp.PromptResponse{}, internalError(end.err.Error())
		}
		a.log.Info("prompt finished",
			"session_id", string(req.SessionId), "stop_reason", string(end.reason), "duration", time.Since(began))
		return acp.PromptResponse{StopReason: end.reason}, nil
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
	s, err := a.lookup(n.SessionId)
	if err != nil {
		return err
	}
	a.log.Info("cancel requested", "session_id", string(n.SessionId))
	return a.backend(s.cwd).Cancel(ctx, string(n.SessionId))
}

func (a *Agent) session(id string) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// lookup returns the session the editor names, or an error when the bridge
// does not track it.
// startPrompt marks the session prompted, unless clean-up has begun. Both
// happen under mu, so clean-up never deletes a session a prompt reached.
func (a *Agent) startPrompt(id acp.SessionId) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return nil, errClosing
	}
	s := a.sessions[string(id)]
	if s == nil {
		return nil, fmt.Errorf("unknown session %q", id)
	}
	s.prompted.Store(true) // before anything can fail: Crush may have the prompt
	return s, nil
}

func (a *Agent) lookup(id acp.SessionId) (*session, error) {
	if s := a.session(string(id)); s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("unknown session %q", id)
}

// backend returns the current backend for cwd, or nil while it is not open.
// Reconnecting replaces it.
func (a *Agent) backend(cwd string) Backend {
	a.mu.Lock()
	defer a.mu.Unlock()
	if w, ok := a.workspaces[cwd]; ok {
		return w.be
	}
	return nil
}

// workspace returns the backend for cwd, opening it and starting its event
// loop on first use. Opening may wait for a server to start, so it runs
// without a.mu; concurrent callers for one directory share a single open, and
// a failed open is retried by the next caller.
func (a *Agent) workspace(ctx context.Context, cwd string) (Backend, error) {
	a.mu.Lock()
	w, ok := a.workspaces[cwd]
	if !ok {
		w = &workspace{ready: make(chan struct{}), tr: translate.New()}
		a.workspaces[cwd] = w
	}
	a.mu.Unlock()

	if !ok {
		a.openWorkspace(ctx, cwd, w)
	}
	select {
	case <-w.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if w.err != nil {
		return nil, w.err
	}
	return a.backend(cwd), nil
}

// openWorkspace connects w, the pending entry for cwd, and releases its
// waiters. On failure it removes the entry so a later call opens it afresh.
func (a *Agent) openWorkspace(ctx context.Context, cwd string, w *workspace) {
	defer close(w.ready)
	b, events, err := a.connect(ctx, cwd)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		w.err = err
		delete(a.workspaces, cwd)
		return
	}
	w.be = b
	a.log.Info("workspace opened", "cwd", cwd)
	go a.follow(events, cwd, w)
}

// connect opens cwd, restores the mode the bridge set there, and subscribes
// to its events, which last until the bridge stops. A server that restarted
// is back on coder, so the editor's mode would otherwise be wrong.
func (a *Agent) connect(ctx context.Context, cwd string) (Backend, <-chan []byte, error) {
	b, err := a.open(ctx, cwd)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", cwd, err)
	}
	a.mu.Lock()
	mode, ok := a.modes[cwd]
	a.mu.Unlock()
	if ok {
		if err := b.SetMainAgent(ctx, string(mode)); err != nil {
			return nil, nil, fmt.Errorf("restore mode %s in %s: %w", mode, cwd, err)
		}
	}
	events, err := b.Events(a.serveCtx)
	if err != nil {
		return nil, nil, err
	}
	return b, events, nil
}

// follow translates Crush events and forwards them in order to the editor.
// When the stream ends it subscribes again as the same client while the
// server still serves the workspace, and the directory's turns carry on.
// Otherwise it fails the directory's turns in flight and reconnects. The
// directory's translator spans both, so text the editor already has is not
// sent again when Crush resends a message.
func (a *Agent) follow(events <-chan []byte, cwd string, w *workspace) {
	delay := reconnectMin
	for {
		received := false
		for data := range events {
			received = true
			w.trMu.Lock()
			updates, err := w.tr.Feed(data)
			if err != nil {
				a.log.Warn("crush event not translated", "error", err)
			}
			for i := range updates {
				a.forward(&updates[i])
			}
			w.trMu.Unlock()
		}
		if a.serveCtx.Err() != nil {
			a.log.Debug("crush event stream ended at shutdown", "cwd", cwd)
			return
		}
		a.log.Warn("crush event stream closed", "cwd", cwd)

		// Prompts sent from here on wait until the stream is back, whether by
		// resubscribing or by reconnecting, rather than reach a server that
		// may be gone.
		a.mu.Lock()
		if _, ok := a.reconnects[cwd]; !ok {
			a.reconnects[cwd] = make(chan struct{})
		}
		a.mu.Unlock()

		// A stream that ends again before it delivers anything backs off, so
		// a server that accepts and drops at once is not hammered.
		if received {
			delay = reconnectMin
		} else {
			delay = min(2*delay, reconnectMax)
		}
		select {
		case <-a.serveCtx.Done():
			return
		case <-time.After(delay):
		}
		if next := a.resubscribe(cwd, w); next != nil {
			a.mu.Lock()
			if done, ok := a.reconnects[cwd]; ok {
				close(done)
				delete(a.reconnects, cwd)
			}
			a.mu.Unlock()
			events = next
			continue
		}

		a.failTurns(cwd)
		if events = a.reconnect(cwd); events == nil {
			return
		}
	}
}

// resubscribeWait bounds the health check and catch-up of a resubscribe.
const resubscribeWait = 5 * time.Second

// resubscribe subscribes to cwd's events again with the current backend, as
// the same client, when the server still answers and still serves the
// workspace. It reports the directory's sessions again and catches up on the
// turns in flight. It returns nil when the server is gone or has forgotten
// the workspace, such as after a restart.
func (a *Agent) resubscribe(cwd string, w *workspace) <-chan []byte {
	b := a.backend(cwd)
	ctx, cancel := context.WithTimeout(a.serveCtx, resubscribeWait)
	defer cancel()
	if err := b.Ping(ctx); err != nil {
		a.log.Warn("crush unreachable after the event stream closed", "cwd", cwd, "error", err)
		return nil
	}
	events, err := b.Events(a.serveCtx)
	if err != nil {
		a.log.Warn("crush event stream not resumed", "cwd", cwd, "error", err)
		return nil
	}
	a.reportSessions(ctx, b, cwd)
	a.catchUp(ctx, b, cwd, w)
	a.log.Info("crush event stream resumed", "cwd", cwd)
	return events
}

// catchUp feeds the stored messages of each turn in flight in cwd through the
// directory's translator. Events sent while the stream was down are lost, so
// this delivers what the editor missed, and a turn that ended in the gap ends.
func (a *Agent) catchUp(ctx context.Context, b Backend, cwd string, w *workspace) {
	for id, s := range a.sessionsIn(cwd) {
		if !s.active.Load() {
			continue
		}
		messages, err := b.Messages(ctx, id)
		if err != nil {
			a.log.Warn("turn not caught up after the event stream closed", "session_id", id, "error", err)
			continue
		}
		turn := currentTurn(messages)
		if pending := s.pendingSent(); pending != "" && userText(turn) != pending {
			// Crush has not stored the editor's prompt yet, so the last user
			// message starts an earlier turn. The live stream delivers this one.
			continue
		}
		w.trMu.Lock()
		for _, raw := range turn {
			updates, err := w.tr.FeedMessage(raw)
			if err != nil {
				a.log.Warn("stored message not translated", "session_id", id, "error", err)
				continue
			}
			for i := range updates {
				a.forward(&updates[i])
			}
		}
		w.trMu.Unlock()
	}
}

// currentTurn returns the messages from the last user message on: the turn in
// flight. Earlier turns may predate the bridge, so their text and finish must
// not reach the editor.
func currentTurn(messages []jsontext.Value) []jsontext.Value {
	for i, raw := range slices.Backward(messages) {
		var m struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Role == "user" {
			return messages[i:]
		}
	}
	return messages
}

// userText returns the visible text of the user message that starts turn, or
// "" when turn does not start with one.
func userText(turn []jsontext.Value) string {
	if len(turn) == 0 {
		return ""
	}
	var m struct {
		Role  string `json:"role"`
		Parts []struct {
			Type string `json:"type"`
			Data struct {
				Text   string `json:"text"`
				Hidden bool   `json:"hidden"`
			} `json:"data"`
		} `json:"parts"`
	}
	if json.Unmarshal(turn[0], &m) != nil || m.Role != "user" {
		return ""
	}
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" && !p.Data.Hidden {
			b.WriteString(p.Data.Text)
		}
	}
	return b.String()
}

// failTurns ends every turn waiting in cwd with errStreamLost.
func (a *Agent) failTurns(cwd string) {
	for _, s := range a.sessionsIn(cwd) {
		s.end(turnEnd{err: errStreamLost})
	}
}

func (a *Agent) sessionsIn(cwd string) map[string]*session {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]*session{}
	for id, s := range a.sessions {
		if s.cwd == cwd {
			out[id] = s
		}
	}
	return out
}

// reconnectWait bounds how long a prompt waits for its directory to reconnect.
const reconnectWait = 30 * time.Second

// awaitConnected waits while cwd is reconnecting, so a prompt sent just after
// a lost connection reaches the new server instead of the dead one.
func (a *Agent) awaitConnected(ctx context.Context, cwd string) error {
	a.mu.Lock()
	done, ok := a.reconnects[cwd]
	a.mu.Unlock()
	if !ok {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(reconnectWait):
		return internalError("crush is unreachable; the bridge is still reconnecting")
	}
}

// Reconnect backoff: the first retry is quick, as a dropped stream is the
// common case; later ones wait up to reconnectMax while the server restarts.
const (
	reconnectMin = 100 * time.Millisecond
	reconnectMax = 5 * time.Second
)

// reconnect reopens cwd until it succeeds or the bridge stops, then reports
// the directory's sessions to Crush again and returns the new event stream.
// It returns nil when the bridge is shutting down.
func (a *Agent) reconnect(cwd string) <-chan []byte {
	delay := reconnectMin
	for attempt := 1; ; attempt++ {
		select {
		case <-a.serveCtx.Done():
			return nil
		case <-time.After(delay):
		}
		delay = min(2*delay, reconnectMax)

		b, events, err := a.connect(a.serveCtx, cwd)
		if err != nil {
			a.log.Warn("crush reconnect failed", "cwd", cwd, "attempt", attempt, "error", err)
			continue
		}

		a.mu.Lock()
		a.workspaces[cwd].be = b
		if done, ok := a.reconnects[cwd]; ok {
			defer close(done) // after the sessions below are reported
			delete(a.reconnects, cwd)
		}
		a.mu.Unlock()
		a.reportSessions(a.serveCtx, b, cwd)
		a.log.Info("crush reconnected", "cwd", cwd, "attempt", attempt)
		return events
	}
}

// reportSessions tells Crush again which sessions this client views in cwd.
// Permission prompts reach only the client viewing the session, and Crush
// re-delivers those still pending.
func (a *Agent) reportSessions(ctx context.Context, b Backend, cwd string) {
	for id := range a.sessionsIn(cwd) {
		if err := b.SetCurrentSession(ctx, id); err != nil {
			a.log.Warn("session not reported after reconnect", "session_id", id, "error", err)
		}
	}
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
		opts := []acp.ToolCallUpdateOpt{
			acp.WithUpdateTitle(title),
			acp.WithUpdateLocations(locs),
			acp.WithUpdateRawInput(u.RawInput),
		}
		if d := inputDiffs(u.ToolName, s.cwd, u.RawInput); d != nil {
			opts = append(opts, acp.WithUpdateContent(d))
		}
		a.send(sid, acp.UpdateToolCall(acp.ToolCallId(u.ToolCallID), opts...))
	case translate.ToolCallFinished:
		status := acp.ToolCallStatusCompleted
		content := []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(u.Output))}
		if u.IsError {
			status = acp.ToolCallStatusFailed
		} else if d := resultDiffs(u.ToolName, s.cwd, u.RawInput, u.RawParams, u.Metadata); d != nil {
			// Paseo reads an edit's text content as a unified diff, so the
			// diff replaces Crush's confirmation message.
			content = d
		}
		a.send(sid, acp.UpdateToolCall(
			acp.ToolCallId(u.ToolCallID),
			acp.WithUpdateStatus(status),
			acp.WithUpdateContent(content),
		))
	case translate.PermissionRequested:
		a.requestPermission(s, u)
	case translate.QuestionAsked:
		a.askQuestions(s, u)
	case translate.TurnEnded:
		s.end(turnEnd{reason: acp.StopReason(u.StopReason)})
	}
}

// requestPermission starts asking the editor about a Crush permission
// request, unless it already is.
func (a *Agent) requestPermission(s *session, u *translate.Update) {
	if !a.startAsking(u.PermissionID) {
		return // re-delivered after a resubscribe while the editor answers it
	}
	diffs := permissionDiffs(u.ToolName, s.cwd, u.RawParams)
	if diffs != nil {
		// Paseo shows the tool call as it already holds it, not the copy in
		// the request, so the diff goes to the tool call first.
		a.send(acp.SessionId(u.SessionID), acp.UpdateToolCall(acp.ToolCallId(u.ToolCallID), acp.WithUpdateContent(diffs)))
	}
	// The editor may take minutes to answer; do not stall the event loop.
	go a.askPermission(a.backend(s.cwd), *u, diffs)
}

// startAsking records that the editor is being asked permission id. It
// returns false when the editor is already answering it. A request without an
// ID is always asked.
func (a *Agent) startAsking(id string) bool {
	if id == "" {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.asking[id] {
		return false
	}
	a.asking[id] = true
	return true
}

func (a *Agent) doneAsking(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.asking, id)
}

// askPermission relays a Crush permission request to the editor and sends the
// decision back. Anything other than an explicit allow denies the request.
// diffs, when set, shows the change an edit tool will make.
func (a *Agent) askPermission(backend Backend, u translate.Update, diffs []acp.ToolCallContent) {
	const (
		allowOnce   acp.PermissionOptionId = "allow_once"
		allowAlways acp.PermissionOptionId = "allow_always"
		rejectOnce  acp.PermissionOptionId = "reject_once"
	)

	title := u.Description
	if title == "" {
		title = u.ToolName
	}
	content := diffs
	if content == nil {
		content = []acp.ToolCallContent{acp.ToolContent(acp.TextBlock(permissionSummary(u.RawParams, title)))}
	}
	resp, err := a.conn.RequestPermission(a.serveCtx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(u.SessionID),
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(u.ToolCallID),
			Title:      acp.PatchSet(title),
			Kind:       acp.PatchSet(toolKind(u.ToolName)),
			Status:     acp.PatchSet(acp.ToolCallStatusPending),
			RawInput:   rawPatch(acp.ToolCallUpdate{}.RawInput, u.RawParams),
			Content:    acp.PatchSet(content),
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
	a.doneAsking(u.PermissionID)
}
