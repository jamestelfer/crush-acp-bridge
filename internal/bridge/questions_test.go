package bridge_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

const (
	questionBatch = "ccacbfd4-687f-4a4b-a1fa-f1f46d38bac2"
	languageQ     = "655fcadf-67d9-43c3-a9aa-dfa415d0d710"
	exclaimQ      = "d075d1c2-760e-41f3-bded-5413c53b1100"
	nameQ         = "1af14935-2e23-4757-a924-026a19dbdab6"
)

// questionPrompt is the prompt question-batch.sse was recorded with.
const questionPrompt = "Use the question tool once, with a single batch of three questions: " +
	"a single_choice question asking whether a greeting should be in English or French, " +
	"a yes_no question asking whether to end it with an exclamation mark, " +
	"and a free_text question asking whose name to greet. Do not create or edit any file. " +
	"After I answer, reply with one sentence that states my three answers."

// eventType is the top-level type of a recorded event.
func eventType(t *testing.T, data []byte) string {
	t.Helper()
	var ev struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(data, &ev))
	return ev.Type
}

// splitAtBatch returns the trace up to and including its question batch
// request, and the events from the batch notification on: Crush emits those
// once the batch is answered, not before.
func splitAtBatch(t *testing.T, tr trace) (upToBatch, afterAnswer trace) {
	t.Helper()
	for i, data := range tr {
		if eventType(t, data) == "question_batch_notification" {
			return tr[:i], tr[i:]
		}
	}
	require.FailNow(t, "the trace has no question batch notification")
	return nil, nil
}

func batchEvent(t *testing.T, tr trace) []byte {
	t.Helper()
	for _, data := range tr {
		if eventType(t, data) == "question_batch_request" {
			return data
		}
	}
	require.FailNow(t, "the trace has no question batch")
	return nil
}

// questionBackend replays question-batch.sse the way Crush does: the
// notification and the rest of the turn follow the answer.
func questionBackend(t *testing.T) *fakeCrush {
	t.Helper()
	tr := loadTrace(t, "question-batch.sse")
	before, after := splitAtBatch(t, tr)
	backend := newFakeCrush(tr.sessionID(t), before)
	backend.onAnswer = after
	return backend
}

func (f *fakeCrush) answeredBatches() []answered {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]answered(nil), f.answered...)
}

func (f *fakeCrush) cancelledBatches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cancelledQ...)
}

// initWith initialises the connection with the editor's capabilities and
// creates a session.
func initWith(t *testing.T, conn *acp.ClientSideConnection, caps acp.ClientCapabilities) acp.SessionId {
	t.Helper()
	_, err := conn.Initialize(t.Context(), acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber, ClientCapabilities: caps,
	})
	require.NoError(t, err)
	return newSession(t, conn, "/work/proj")
}

func TestChatFallbackAnswersEmptyWithANoteAndEndsTheTurn(t *testing.T) {
	tests := map[string]acp.ClientCapabilities{
		"no elicitation":   {},
		"form elicitation": {Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}},
	}
	for name, caps := range tests {
		t.Run(name, func(t *testing.T) {
			backend := questionBackend(t)
			var perms int
			// An editor that approves anything would fabricate answers if the
			// bridge asked it; the chat fallback must not ask.
			ed := &editor{onPermisson: func(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
				perms++
				return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(r.Options[0].OptionId)}
			}}
			conn := connect(t, backend, ed)
			sid := initWith(t, conn, caps)

			resp := prompt(t, conn, sid, questionPrompt)

			assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
			assert.Zero(t, perms, "no permission request")
			assert.Zero(t, ed.elicited.Load(), "no elicitation request")
			assert.Empty(t, backend.cancelledBatches())
			got := backend.answeredBatches()
			require.Len(t, got, 1)
			assert.Equal(t, questionBatch, got[0].BatchID)
			require.Len(t, got[0].Answers, 3)
			for i, id := range []string{languageQ, exclaimQ, nameQ} {
				a := got[0].Answers[i]
				assert.Equal(t, id, a.QuestionID)
				assert.Empty(t, a.SelectedIDs)
				assert.Empty(t, a.FillInText)
				assert.Nil(t, a.Yes)
				assert.Contains(t, a.Notes[crush.QuestionNoteKey], "Do not call the question tool again")
			}
		})
	}
}

func TestQuestionBatchIsShownOnItsToolCall(t *testing.T) {
	backend := questionBackend(t)
	ed := &editor{}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	prompt(t, conn, sid, questionPrompt)

	var titles []string
	for _, u := range ed.snapshot() {
		if u.ToolCallUpdate != nil {
			if title, ok := u.ToolCallUpdate.Title.Value(); ok {
				titles = append(titles, title)
			}
		}
	}
	assert.Contains(t, titles, "3 questions")
}

func TestQuestionBatchRedeliveredWhileHandledIsHandledOnce(t *testing.T) {
	tr := loadTrace(t, "question-batch.sse")
	before, after := splitAtBatch(t, tr)
	batch := batchEvent(t, tr)
	backend := newFakeCrush(tr.sessionID(t), append(append(trace{}, before...), batch))
	backend.onAnswer = after
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	prompt(t, conn, sid, questionPrompt)

	assert.Len(t, backend.answeredBatches(), 1)
}

func TestResolvedBatchIsForgottenSoItsIDCanBeAskedAgain(t *testing.T) {
	tr := loadTrace(t, "question-batch.sse")
	backend := questionBackend(t)
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	prompt(t, conn, sid, questionPrompt)
	require.Len(t, backend.answeredBatches(), 1)

	// The turn's events include the batch notification, so the batch is
	// resolved; the same ID is a new batch if Crush ever sends it again.
	batch := batchEvent(t, tr)
	require.Eventually(t, func() bool {
		backend.push(trace{batch})
		return len(backend.answeredBatches()) == 2
	}, 2*time.Second, 10*time.Millisecond)
}

func TestQuestionBatchIsHandledAgainWhenTheAnswerDoesNotReachCrush(t *testing.T) {
	tr := loadTrace(t, "question-batch.sse")
	before, after := splitAtBatch(t, tr)
	backend := newFakeCrush(tr.sessionID(t), before)
	backend.answerErr = errors.New("answer questions: connection refused")
	backend.onAnswer = nil
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	done := promptAsync(t, conn, sid, questionPrompt)
	require.Eventually(t, func() bool { return len(backend.answeredBatches()) == 1 }, 2*time.Second, 10*time.Millisecond)

	// Crush re-delivers the batch it still holds as pending. Until the bridge
	// has recorded the failure, the re-delivery is still seen as in flight.
	batch := batchEvent(t, tr)
	require.Eventually(t, func() bool {
		backend.push(trace{batch})
		return len(backend.answeredBatches()) == 2
	}, 2*time.Second, 10*time.Millisecond, "the re-delivered batch was not answered")

	backend.push(after)
	res := <-done
	require.NoError(t, res.err)
}

// blockingHandler waits for its batch to be cancelled, then hands over to the
// chat fallback as a slow strategy that finishes after the batch is gone.
type blockingHandler struct {
	started chan struct{}
	ended   chan error
}

func (h blockingHandler) Handle(ctx context.Context, backend bridge.Backend, u translate.Update) error {
	close(h.started)
	<-ctx.Done()
	err := bridge.ChatFallbackQuestionHandler{}.Handle(ctx, backend, u)
	h.ended <- err
	return err
}

func TestCancelledBatchIsNeverAnsweredLate(t *testing.T) {
	type cancelFunc = func(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, backend *fakeCrush, tr trace)
	tests := map[string]cancelFunc{
		"session cancel": func(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, _ *fakeCrush, _ trace) {
			require.NoError(t, conn.Cancel(t.Context(), acp.CancelNotification{SessionId: sid}))
		},
		"resolved elsewhere": func(t *testing.T, _ *acp.ClientSideConnection, _ acp.SessionId, backend *fakeCrush, tr trace) {
			_, after := splitAtBatch(t, tr)
			backend.push(after[:1])
		},
		"question tool call finished": func(t *testing.T, _ *acp.ClientSideConnection, _ acp.SessionId, backend *fakeCrush, tr trace) {
			var finished []byte
			for _, data := range tr {
				if eventType(t, data) == "message" && bytes.Contains(data, []byte(`"tool_result"`)) {
					finished = data
					break
				}
			}
			require.NotNil(t, finished)
			backend.push(trace{finished})
		},
	}
	for name, cancel := range tests {
		t.Run(name, func(t *testing.T) {
			tr := loadTrace(t, "question-batch.sse")
			before, _ := splitAtBatch(t, tr)
			backend := newFakeCrush(tr.sessionID(t), before)
			h := blockingHandler{started: make(chan struct{}), ended: make(chan error, 1)}
			conn := connect(t, backend, &editor{}, bridge.WithQuestionHandler(h))
			sid := start(t, conn)

			_ = promptAsync(t, conn, sid, questionPrompt)
			select {
			case <-h.started:
			case <-time.After(2 * time.Second):
				require.FailNow(t, "the question handler never started")
			}

			cancel(t, conn, sid, backend, tr)

			select {
			case err := <-h.ended:
				require.NoError(t, err)
			case <-time.After(2 * time.Second):
				require.FailNow(t, "the batch context was not cancelled")
			}
			assert.Empty(t, backend.answeredBatches(), "no late answer reaches Crush")
			assert.Empty(t, backend.cancelledBatches())
		})
	}
}
