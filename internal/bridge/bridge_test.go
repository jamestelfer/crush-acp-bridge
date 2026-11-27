package bridge_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// trace is a recorded Crush SSE stream.
type trace [][]byte

func loadTrace(t *testing.T, name string) trace {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	var out trace
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			out = append(out, []byte(strings.TrimSpace(data)))
		}
	}
	require.NoError(t, sc.Err())
	return out
}

// sessionID returns the Crush session ID the trace was recorded against.
func (tr trace) sessionID(t *testing.T) string {
	t.Helper()
	for _, data := range tr {
		var ev struct {
			Payload struct {
				Payload struct {
					SessionID string `json:"session_id"`
				} `json:"payload"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(data, &ev))
		if id := ev.Payload.Payload.SessionID; id != "" {
			return id
		}
	}
	require.FailNow(t, "trace has no session ID")
	return ""
}

// firstTurn returns the events up to and including the first assistant
// message that ends with the given finish reason.
func (tr trace) firstTurn(t *testing.T, reason string) trace {
	t.Helper()
	marker := `"reason":"` + reason + `"`
	for i, data := range tr {
		if strings.Contains(string(data), `"role":"assistant"`) && strings.Contains(string(data), marker) {
			return tr[:i+1]
		}
	}
	require.FailNowf(t, "missing finish", "trace has no assistant finish %q", reason)
	return nil
}

// fakeCrush is a scripted Crush backend: a prompt replays recorded events.
type fakeCrush struct {
	sessionID string
	ids       []string // handed out in order by NewSession when set
	script    trace
	onCancel  trace // replayed when the session is cancelled
	events    chan []byte

	mu          sync.Mutex
	prompts     []string
	attachments [][]crush.Attachment // each prompt's attachments, in order
	cancels     []string
	grants      []grant
	grantErr    error
	answered    []answered // question batches answered, in order
	cancelledQ  []string   // question batches cancelled, in order
	answerErr   error      // returned once by AnswerQuestions when set
	cancelErr   error      // returned by CancelQuestions when set
	onAnswer    trace      // events Crush emits once a question batch is answered
	models      crush.Models
	modelsErr   error
	setModels   []crush.ModelRef
	agents      []string
	agentErr    error
	currents    []string
	currentErr  error // returned by SetCurrentSession when set
	deleted     []string
	deleteErr   error
	stored      []crush.Session // returned by Sessions
	history     trace           // the stored messages' source when set; else script
	down        atomic.Bool
	stale       atomic.Bool  // the server restarted and no longer knows the workspace
	gone        atomic.Bool  // nothing answers: Ping fails
	subscribes  atomic.Int32 // Events calls that succeeded

	promptErr    error        // returned by Prompt when set, as Crush refusing the prompt
	messageReads atomic.Int32 // Messages calls
	onPing       func()       // called by Ping before it answers, when set
	beforePrompt func()       // called by Prompt before Crush stores the prompt, when set
}

// Ping fails once the server has gone.
func (f *fakeCrush) Ping(context.Context) error {
	if f.onPing != nil {
		f.onPing()
	}
	if f.gone.Load() {
		return errors.New("dial unix crush.sock: connect: connection refused")
	}
	return nil
}

func (f *fakeCrush) Sessions(context.Context) ([]crush.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]crush.Session(nil), f.stored...), nil
}

// Messages returns what Crush would store for the history, or else the
// script: the last snapshot of each message, in the order the messages first
// appeared.
func (f *fakeCrush) Messages(_ context.Context, sessionID string) ([]jsontext.Value, error) {
	f.messageReads.Add(1)
	var order []string
	last := map[string]jsontext.Value{}
	source := f.script
	if f.history != nil {
		source = f.history
	}
	for _, data := range source {
		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				Payload jsontext.Value `json:"payload"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &ev); err != nil || ev.Type != "message" {
			continue
		}
		var msg struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(ev.Payload.Payload, &msg); err != nil || msg.SessionID != sessionID {
			continue
		}
		if _, seen := last[msg.ID]; !seen {
			order = append(order, msg.ID)
		}
		last[msg.ID] = ev.Payload.Payload
	}
	out := make([]jsontext.Value, 0, len(order))
	for _, id := range order {
		out = append(out, last[id])
	}
	return out, nil
}

func (f *fakeCrush) SetMainAgent(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agentErr != nil {
		return f.agentErr
	}
	f.agents = append(f.agents, id)
	return nil
}

func (f *fakeCrush) Models(context.Context) (crush.Models, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.models, f.modelsErr
}

func (f *fakeCrush) SetModel(_ context.Context, m crush.ModelRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setModels = append(f.setModels, m)
	f.models.Current = m
	return nil
}

type grant struct {
	Permission jsontext.Value
	Action     crush.Action
}

func newFakeCrush(sessionID string, script trace) *fakeCrush {
	return &fakeCrush{sessionID: sessionID, script: script, events: make(chan []byte, 1024)}
}

func (f *fakeCrush) NewSession(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ids) > 0 {
		id := f.ids[0]
		f.ids = f.ids[1:]
		return id, nil
	}
	return f.sessionID, nil
}
func (f *fakeCrush) SetCurrentSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.currentErr != nil {
		return f.currentErr
	}
	f.currents = append(f.currents, id)
	return nil
}

func (f *fakeCrush) DeleteSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeCrush) deletedSessions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func (f *fakeCrush) Events(context.Context) (<-chan []byte, error) {
	if f.gone.Load() {
		return nil, errors.New("events: dial unix crush.sock: connect: connection refused")
	}
	if f.stale.Load() {
		return nil, fmt.Errorf("events: GET /workspaces/ws-1/events: %w", crush.ErrNotFound)
	}
	f.down.Store(false)
	f.subscribes.Add(1)
	return f.current(), nil
}

// open is what a fresh workspace open returns: the backend, known to the
// server again.
func (f *fakeCrush) open() *fakeCrush {
	f.stale.Store(false)
	f.gone.Store(false)
	return f
}

func (f *fakeCrush) current() chan []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events
}

// restart ends the event stream as a server restart does: the server answers
// but no longer knows the workspace until it is opened again.
func (f *fakeCrush) restart() {
	f.stale.Store(true)
	f.drop()
}

// kill ends the event stream and leaves nothing answering until the workspace
// is opened again.
func (f *fakeCrush) kill() {
	f.gone.Store(true)
	f.drop()
}

// drop ends the current event stream, as a server that drops a slow client
// does. The server keeps running; the next Events call returns a fresh
// stream.
func (f *fakeCrush) drop() {
	f.down.Store(true) // like a dead server, refuse work until resubscribed
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.events)
	f.events = make(chan []byte, 1024)
}

func (f *fakeCrush) currentSessions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.currents...)
}

func (f *fakeCrush) Prompt(_ context.Context, _ string, p crush.Prompt) error {
	if f.down.Load() {
		return errors.New("dial unix crush.sock: connect: connection refused")
	}
	if f.promptErr != nil {
		return f.promptErr
	}
	if f.beforePrompt != nil {
		f.beforePrompt()
	}
	// Hold mu while sending so drop cannot close the stream mid-turn.
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, p.Text)
	f.attachments = append(f.attachments, p.Attachments)
	for _, data := range f.script {
		f.events <- data
	}
	return nil
}

func (f *fakeCrush) Cancel(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, sessionID)
	for _, data := range f.onCancel {
		f.events <- data
	}
	return nil
}

func (f *fakeCrush) Grant(_ context.Context, p jsontext.Value, a crush.Action) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants = append(f.grants, grant{p, a})
	return f.grantErr
}

// answered is one question batch the bridge answered.
type answered struct {
	BatchID string                 `json:"batch_id"`
	Answers []crush.QuestionAnswer `json:"answers"`
}

func (f *fakeCrush) AnswerQuestions(_ context.Context, batchID string, answers []crush.QuestionAnswer) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, answered{batchID, answers})
	for _, data := range f.onAnswer {
		f.events <- data
	}
	err := f.answerErr
	f.answerErr = nil
	return err == nil, err
}

func (f *fakeCrush) CancelQuestions(_ context.Context, batchID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelledQ = append(f.cancelledQ, batchID)
	return f.cancelErr == nil, f.cancelErr
}

// editor is an in-memory ACP client that records what the agent sends.
type editor struct {
	mu          sync.Mutex
	updates     []acp.SessionUpdate
	sessions    []acp.SessionId // the session of each entry in updates
	onPermisson func(acp.RequestPermissionRequest) acp.RequestPermissionResponse
}

func (e *editor) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.updates = append(e.updates, n.Update)
	e.sessions = append(e.sessions, n.SessionId)
	return nil
}

func (e *editor) RequestPermission(_ context.Context, r acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if e.onPermisson == nil {
		return acp.RequestPermissionResponse{}, errors.New("unexpected permission request")
	}
	return e.onPermisson(r), nil
}

func (e *editor) snapshot() []acp.SessionUpdate {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]acp.SessionUpdate(nil), e.updates...)
}

var errNoFS = errors.New("editor does not offer this capability")

func (*editor) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errNoFS
}
func (*editor) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errNoFS
}
func (*editor) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errNoFS
}
func (*editor) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errNoFS
}
func (*editor) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errNoFS
}
func (*editor) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errNoFS
}
func (*editor) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errNoFS
}

func (*editor) CreateElicitation(context.Context, acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
	return acp.CreateElicitationResponse{}, errNoFS
}
func (*editor) CompleteElicitation(context.Context, acp.CompleteElicitationNotification) error {
	return errNoFS
}

// connect serves the bridge with a single backend for every directory.
func connect(t *testing.T, backend bridge.Backend, ed *editor, opts ...bridge.Option) *acp.ClientSideConnection {
	t.Helper()
	return connectWith(t, func(context.Context, string) (bridge.Backend, error) {
		if f, ok := backend.(*fakeCrush); ok {
			return f.open(), nil
		}
		return backend, nil
	}, ed, opts...)
}

// connectWith serves the bridge over in-memory pipes and returns the editor's
// view of the connection.
func connectWith(t *testing.T, open bridge.Opener, ed *editor, opts ...bridge.Option) *acp.ClientSideConnection {
	t.Helper()
	return serveWith(t, open, ed, opts...).conn
}

// served is a running bridge whose end the test controls.
type served struct {
	conn   *acp.ClientSideConnection
	cancel context.CancelFunc // ends Serve as a signal does
	input  *io.PipeWriter     // the bridge's stdin
	done   chan struct{}      // closed when Serve returns
}

// serveWith serves the bridge over in-memory pipes until the test ends.
func serveWith(t *testing.T, open bridge.Opener, ed *editor, opts ...bridge.Option) *served {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	toAgentR, toAgentW := io.Pipe()
	toEditorR, toEditorW := io.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = bridge.Serve(ctx, open, toAgentR, toEditorW, opts...)
	}()
	t.Cleanup(func() {
		cancel()
		_ = toAgentW.Close()
		_ = toEditorW.Close()
		<-done
	})

	conn := acp.NewClientSideConnection(ed, toAgentW, toEditorR)
	conn.SetLogger(slog.New(slog.DiscardHandler))
	return &served{conn: conn, cancel: cancel, input: toAgentW, done: done}
}

// disconnect closes the bridge's stdin, as an editor that exits does, and
// waits for Serve to return.
func (s *served) disconnect(t *testing.T) {
	t.Helper()
	require.NoError(t, s.input.Close())
	s.wait(t)
}

// stop cancels Serve's context, as SIGTERM does, and waits for it to return.
func (s *served) stop(t *testing.T) {
	t.Helper()
	s.cancel()
	s.wait(t)
}

func (s *served) wait(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Serve did not return")
	}
}

// start initialises the connection and creates a session.
func start(t *testing.T, conn *acp.ClientSideConnection) acp.SessionId {
	t.Helper()

	_, err := conn.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	require.NoError(t, err)
	return newSession(t, conn, "/work/proj")
}

func newSession(t *testing.T, conn *acp.ClientSideConnection, cwd string) acp.SessionId {
	t.Helper()

	sess, err := conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}})
	require.NoError(t, err)
	return sess.SessionId
}

func prompt(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, text string) acp.PromptResponse {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
	require.NoError(t, err)
	return resp
}

func TestPromptStreamsAssistantTextAndEndsTurn(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	backend := newFakeCrush(tr.sessionID(t), tr.firstTurn(t, "end_turn"))
	ed := &editor{}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	resp := prompt(t, conn, sid, "explain TCP")

	require.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	require.Equal(t, []string{"explain TCP"}, backend.prompts)

	var text strings.Builder
	for _, u := range ed.snapshot() {
		if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
			text.WriteString(u.AgentMessageChunk.Content.Text.Text)
		}
	}
	require.Greater(t, text.Len(), 1000, "the recorded reply is over 2000 characters")
}

func TestToolCallIsReportedWithKindAndCompletion(t *testing.T) {
	tr := loadTrace(t, "bash-permission.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	ed := &editor{onPermisson: allow}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	prompt(t, conn, sid, "run it")

	var started *acp.SessionUpdateToolCall
	var finished *acp.SessionToolCallUpdate
	for _, u := range ed.snapshot() {
		if u.ToolCall != nil {
			started = u.ToolCall
		}
		if u.ToolCallUpdate != nil {
			if status, ok := u.ToolCallUpdate.Status.Value(); ok && status == acp.ToolCallStatusCompleted {
				finished = u.ToolCallUpdate
			}
		}
	}
	require.NotNil(t, started, "no tool call reported")
	require.Equal(t, acp.ToolKindExecute, started.Kind)
	require.NotNil(t, finished, "no completed update reported")
	require.Equal(t, started.ToolCallId, finished.ToolCallId)
}

// allow answers every permission request by picking the allow-once option.
func allow(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	for _, o := range r.Options {
		if o.Kind == acp.PermissionOptionKindAllowOnce {
			return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(o.OptionId)}
		}
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
}

// choose answers a permission request by picking the option of the given kind.
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

func TestPermissionDecisionIsRelayedToCrush(t *testing.T) {
	cancelled := func(acp.RequestPermissionRequest) acp.RequestPermissionResponse {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	}
	tests := []struct {
		name   string
		answer func(acp.RequestPermissionRequest) acp.RequestPermissionResponse
		want   crush.Action
	}{
		{"allow once", choose(acp.PermissionOptionKindAllowOnce), crush.Allow},
		{"allow always", choose(acp.PermissionOptionKindAllowAlways), crush.AllowForSession},
		{"reject", choose(acp.PermissionOptionKindRejectOnce), crush.Deny},
		{"dismissed prompt", cancelled, crush.Deny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadTrace(t, "bash-permission.sse")
			backend := newFakeCrush(tr.sessionID(t), tr)
			var asked acp.RequestPermissionRequest
			ed := &editor{onPermisson: func(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
				asked = r
				return tc.answer(r)
			}}
			conn := connect(t, backend, ed)
			sid := start(t, conn)

			prompt(t, conn, sid, "run it")

			require.Eventually(t, func() bool {
				backend.mu.Lock()
				defer backend.mu.Unlock()
				return len(backend.grants) == 1
			}, 2*time.Second, 10*time.Millisecond)

			backend.mu.Lock()
			g := backend.grants[0]
			backend.mu.Unlock()
			require.Equal(t, tc.want, g.Action)

			var raw struct {
				ID         string `json:"id"`
				ToolCallID string `json:"tool_call_id"`
			}
			require.NoError(t, json.Unmarshal(g.Permission, &raw))
			require.NotEmpty(t, raw.ID, "the original permission request must be echoed back")
			require.Equal(t, raw.ToolCallID, string(asked.ToolCall.ToolCallId))
			require.Equal(t, sid, asked.SessionId)
		})
	}
}

// cancelledTurn splits the trace's cancelled turn into the events before the
// cancel and the final message update that reports it.
func (tr trace) cancelledTurn(t *testing.T) (before, atCancel trace) {
	t.Helper()
	start := len(tr.firstTurn(t, "end_turn"))
	for i := start; i < len(tr); i++ {
		d := string(tr[i])
		if strings.Contains(d, `"role":"assistant"`) && strings.Contains(d, `"reason":"canceled"`) {
			return tr[start:i], tr[i : i+1]
		}
	}
	require.FailNow(t, "trace has no cancelled turn")
	return nil, nil
}

func TestCancelStopsTurnAndReportsCancelled(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	before, atCancel := tr.cancelledTurn(t)
	backend := newFakeCrush(tr.sessionID(t), before)
	backend.onCancel = atCancel
	ed := &editor{}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	type outcome struct {
		resp acp.PromptResponse
		err  error
	}
	result := make(chan outcome, 1)
	go func() {
		resp, err := conn.Prompt(t.Context(), acp.PromptRequest{
			SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock("write an essay")},
		})
		result <- outcome{resp, err}
	}()
	require.Eventually(t, func() bool { return len(ed.snapshot()) > 0 }, 2*time.Second, 10*time.Millisecond,
		"the turn must be streaming before it is cancelled")
	select {
	case <-result:
		require.FailNow(t, "prompt returned before cancel")
	default:
	}

	require.NoError(t, conn.Cancel(t.Context(), acp.CancelNotification{SessionId: sid}))

	select {
	case got := <-result:
		require.NoError(t, got.err)
		require.Equal(t, acp.StopReasonCancelled, got.resp.StopReason)
	case <-time.After(3 * time.Second):
		require.FailNow(t, "prompt did not return after cancel")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	require.Equal(t, []string{string(sid)}, backend.cancels)
}

// A turn started by another client (the TUI) ends while this editor has no
// prompt in flight. That end must not complete the editor's next prompt.
func TestEarlierTurnByAnotherClientDoesNotEndNextPrompt(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	before, atCancel := tr.cancelledTurn(t)
	backend := newFakeCrush(tr.sessionID(t), before)
	backend.onCancel = atCancel
	ed := &editor{}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	// Another client's turn runs to completion.
	for _, data := range tr.firstTurn(t, "end_turn") {
		backend.events <- data
	}
	require.Eventually(t, func() bool { return len(ed.snapshot()) > 0 }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // let the end-of-turn event be processed

	result := make(chan acp.PromptResponse, 1)
	go func() {
		resp, _ := conn.Prompt(t.Context(), acp.PromptRequest{
			SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock("next")},
		})
		result <- resp
	}()

	select {
	case resp := <-result:
		require.FailNowf(t, "early return", "prompt returned %q before its own turn ended", resp.StopReason)
	case <-time.After(300 * time.Millisecond):
	}
}

const tcpPrompt = "Write a 300 word explanation of how TCP congestion control works. No tools."

func userChunks(ed *editor) []string {
	var out []string
	for _, u := range ed.snapshot() {
		if u.UserMessageChunk != nil && u.UserMessageChunk.Content.Text != nil {
			out = append(out, u.UserMessageChunk.Content.Text.Text)
		}
	}
	return out
}

func TestUserMessageFromAnotherClientIsForwarded(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	backend := newFakeCrush(tr.sessionID(t), nil)
	ed := &editor{}
	conn := connect(t, backend, ed)
	start(t, conn)

	// The TUI user types a prompt; the editor sent nothing.
	for _, data := range tr.firstTurn(t, "end_turn") {
		backend.events <- data
	}

	require.Eventually(t, func() bool { return len(userChunks(ed)) > 0 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{tcpPrompt}, userChunks(ed))
}

func TestEditorsOwnPromptIsNotEchoedBack(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	backend := newFakeCrush(tr.sessionID(t), tr.firstTurn(t, "end_turn"))
	ed := &editor{}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	prompt(t, conn, sid, tcpPrompt)

	require.Empty(t, userChunks(ed), "the editor already shows what the user typed")
}

// toolDetails returns the most recent title, raw input and locations the
// editor received for a tool call.
func toolDetails(ed *editor, id acp.ToolCallId) (title string, rawInput string, locs []acp.ToolCallLocation) {
	for _, u := range ed.snapshot() {
		upd := u.ToolCallUpdate
		if upd == nil || upd.ToolCallId != id {
			continue
		}
		if v, ok := upd.Title.Value(); ok {
			title = v
		}
		if v, ok := upd.RawInput.Value(); ok {
			rawInput = string(v)
		}
		if v, ok := upd.Locations.Value(); ok {
			locs = v
		}
	}
	return title, rawInput, locs
}

func TestBashCallShowsCrushsDescriptionAndInput(t *testing.T) {
	tr := loadTrace(t, "bash-permission.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	ed := &editor{onPermisson: allow}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	prompt(t, conn, sid, "run it")

	var id acp.ToolCallId
	for _, u := range ed.snapshot() {
		if u.ToolCall != nil {
			id = u.ToolCall.ToolCallId
		}
	}
	require.NotEmpty(t, id)
	title, raw, _ := toolDetails(ed, id)
	require.Equal(t, "Write hello to out.txt", title)
	require.JSONEq(t,
		`{"command":"echo hello > out.txt","description":"Write hello to out.txt"}`, raw)
}

func TestFileToolCallPointsAtTheFile(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	ed := &editor{}
	conn := connect(t, backend, ed)
	start(t, conn)

	input, err := json.Marshal(map[string]any{"file_path": "/work/proj/main.go"})
	require.NoError(t, err)
	event, err := json.Marshal(map[string]any{
		"type": "message",
		"payload": map[string]any{"type": "updated", "payload": map[string]any{
			"id": "m1", "role": "assistant", "session_id": "sess-1",
			"parts": []any{map[string]any{"type": "tool_call", "data": map[string]any{
				"id": "c1", "name": "view", "input": string(input), "finished": true,
			}}},
		}},
	})
	require.NoError(t, err)
	backend.events <- event

	require.Eventually(t, func() bool {
		title, _, _ := toolDetails(ed, "c1")
		return title != ""
	}, 2*time.Second, 10*time.Millisecond)
	title, _, locs := toolDetails(ed, "c1")
	require.Equal(t, "View /work/proj/main.go", title)
	require.Equal(t, []acp.ToolCallLocation{{Path: "/work/proj/main.go"}}, locs)
}

func TestEachSessionOpensCrushInItsOwnDirectory(t *testing.T) {
	backends := map[string]*fakeCrush{
		"/work/a": newFakeCrush("sess-a", nil),
		"/work/b": newFakeCrush("sess-b", nil),
	}
	var mu sync.Mutex
	var opened []string
	open := func(_ context.Context, cwd string) (bridge.Backend, error) {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, cwd)
		b, ok := backends[cwd]
		if !ok {
			return nil, fmt.Errorf("unexpected directory %q", cwd)
		}
		return b, nil
	}
	conn := connectWith(t, open, &editor{})
	_, err := conn.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	require.NoError(t, err)

	sidA := newSession(t, conn, "/work/a")
	sidB := newSession(t, conn, "/work/b")
	require.Equal(t, acp.SessionId("sess-a"), sidA)
	require.Equal(t, acp.SessionId("sess-b"), sidB)

	// A prompt reaches the workspace of its own session only.
	go func() {
		_, _ = conn.Prompt(t.Context(), acp.PromptRequest{SessionId: sidB, Prompt: []acp.ContentBlock{acp.TextBlock("hi")}})
	}()
	require.Eventually(t, func() bool {
		backends["/work/b"].mu.Lock()
		defer backends["/work/b"].mu.Unlock()
		return len(backends["/work/b"].prompts) == 1
	}, 2*time.Second, 10*time.Millisecond)
	require.Empty(t, backends["/work/a"].prompts)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"/work/a", "/work/b"}, opened)
}

func TestSessionsInOneDirectoryShareAWorkspace(t *testing.T) {
	backend := newFakeCrush("", nil)
	backend.ids = []string{"sess-1", "sess-2"}
	var mu sync.Mutex
	var opened int
	open := func(context.Context, string) (bridge.Backend, error) {
		mu.Lock()
		defer mu.Unlock()
		opened++
		return backend, nil
	}
	conn := connectWith(t, open, &editor{})
	_, err := conn.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	require.NoError(t, err)

	newSession(t, conn, "/work/a")
	newSession(t, conn, "/work/a")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, opened, "one workspace and one event stream per directory")
}

// logCapture collects structured log output from the bridge.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(c, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// find returns the first record with the given message, or nil.
func (c *logCapture) find(t *testing.T, msg string) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for line := range bytes.Lines(c.buf.Bytes()) {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(line, &rec))
		if rec["msg"] == msg {
			return rec
		}
	}
	return nil
}

func (c *logCapture) await(t *testing.T, msg string) map[string]any {
	t.Helper()
	var rec map[string]any
	require.Eventually(t, func() bool {
		rec = c.find(t, msg)
		return rec != nil
	}, 2*time.Second, 10*time.Millisecond, "no log record %q", msg)
	return rec
}

func TestMalformedCrushEventIsLoggedAndSkipped(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	backend := newFakeCrush(tr.sessionID(t), nil)
	logs := &logCapture{}
	ed := &editor{}
	conn := connect(t, backend, ed, bridge.WithLogger(logs.logger()))
	start(t, conn)

	backend.events <- []byte(`{this is not json`)
	for _, data := range tr.firstTurn(t, "end_turn") {
		backend.events <- data
	}

	rec := logs.await(t, "crush event not translated")
	require.Equal(t, "WARN", rec["level"])
	require.NotEmpty(t, rec["error"])
	require.Eventually(t, func() bool { return len(ed.snapshot()) > 0 }, 2*time.Second, 10*time.Millisecond,
		"events after the bad one must still be delivered")
}

func TestFailedPermissionGrantIsLogged(t *testing.T) {
	tr := loadTrace(t, "bash-permission.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	backend.grantErr = errors.New("permission already resolved")
	logs := &logCapture{}
	ed := &editor{onPermisson: allow}
	conn := connect(t, backend, ed, bridge.WithLogger(logs.logger()))
	sid := start(t, conn)

	prompt(t, conn, sid, "run it")

	rec := logs.await(t, "permission grant failed")
	require.Equal(t, "WARN", rec["level"])
	require.Equal(t, "allow", rec["action"])
	require.Equal(t, "bash", rec["tool"])
	require.Contains(t, rec["error"], "already resolved")
}

func TestSessionAndPromptLifecycleAreLogged(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	backend := newFakeCrush(tr.sessionID(t), tr.firstTurn(t, "end_turn"))
	logs := &logCapture{}
	conn := connect(t, backend, &editor{}, bridge.WithLogger(logs.logger()))
	sid := start(t, conn)

	prompt(t, conn, sid, tcpPrompt)

	created := logs.await(t, "session created")
	require.Equal(t, "INFO", created["level"])
	require.Equal(t, string(sid), created["session_id"])
	require.Equal(t, "/work/proj", created["cwd"])

	begun := logs.await(t, "prompt started")
	require.Equal(t, string(sid), begun["session_id"])

	done := logs.await(t, "prompt finished")
	require.Equal(t, "INFO", done["level"])
	require.Equal(t, string(sid), done["session_id"])
	require.Equal(t, "end_turn", done["stop_reason"])
	require.Contains(t, done, "duration")
}

func TestPermissionDecisionIsLogged(t *testing.T) {
	tr := loadTrace(t, "bash-permission.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	logs := &logCapture{}
	conn := connect(t, backend, &editor{onPermisson: choose(acp.PermissionOptionKindRejectOnce)},
		bridge.WithLogger(logs.logger()))
	sid := start(t, conn)

	prompt(t, conn, sid, "run it")

	rec := logs.await(t, "permission decided")
	require.Equal(t, "INFO", rec["level"])
	require.Equal(t, "deny", rec["action"])
	require.Equal(t, "bash", rec["tool"])
	require.Equal(t, string(sid), rec["session_id"])
}

func TestEndOfTheCrushEventStreamIsLogged(t *testing.T) {
	backend := newFakeCrush("sess-1", nil)
	logs := &logCapture{}
	conn := connect(t, backend, &editor{}, bridge.WithLogger(logs.logger()))
	start(t, conn)

	backend.drop()

	rec := logs.await(t, "crush event stream closed")
	require.Equal(t, "WARN", rec["level"])
	require.Equal(t, "/work/proj", rec["cwd"])
}

func TestPermissionRequestShowsTheCommandToBeRun(t *testing.T) {
	tr := loadTrace(t, "bash-permission.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	requests := make(chan acp.RequestPermissionRequest, 1)
	ed := &editor{onPermisson: func(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
		requests <- r
		return allow(r)
	}}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	prompt(t, conn, sid, "run it")

	var asked acp.RequestPermissionRequest
	select {
	case asked = <-requests:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "the editor was never asked for permission")
	}
	raw, ok := asked.ToolCall.RawInput.Value()
	require.True(t, ok, "the request must say what will run")
	require.JSONEq(t, `{
		"description": "Write hello to out.txt", "command": "echo hello > out.txt",
		"working_dir": "", "run_in_background": false, "auto_background_after": 0
	}`, string(raw))
	status, ok := asked.ToolCall.Status.Value()
	require.True(t, ok)
	require.Equal(t, acp.ToolCallStatusPending, status)
	content, ok := asked.ToolCall.Content.Value()
	require.True(t, ok)
	require.Len(t, content, 1)
	require.Equal(t, "echo hello > out.txt", content[0].Content.Content.Text.Text)
}

func TestACPLibraryDiagnosticsUseTheStructuredLogger(t *testing.T) {
	logs := &logCapture{}
	toAgentR, toAgentW := io.Pipe()
	toEditorR, toEditorW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = bridge.Serve(t.Context(), func(context.Context, string) (bridge.Backend, error) {
			return newFakeCrush("sess-1", nil), nil
		}, toAgentR, toEditorW, bridge.WithLogger(logs.logger()))
	}()
	conn := acp.NewClientSideConnection(&editor{}, toAgentW, toEditorR)
	conn.SetLogger(slog.New(slog.DiscardHandler))
	_, err := conn.Initialize(t.Context(), acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber})
	require.NoError(t, err)

	require.NoError(t, toAgentW.Close()) // the editor goes away

	rec := logs.await(t, "connection closed")
	require.Equal(t, "INFO", rec["level"])
	<-done
	_ = toEditorW.Close()
}
