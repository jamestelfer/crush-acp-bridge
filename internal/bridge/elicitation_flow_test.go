package bridge_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

func str(s string) acp.ElicitationContentValue { return acp.NewElicitationContentValueString(s) }

func strs(s ...string) acp.ElicitationContentValue {
	return acp.NewElicitationContentValueStringArray(s...)
}

var formCaps = acp.ClientCapabilities{
	Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
}

// formEditor is an editor that answers the one elicitation it is asked with
// resp, and records the request.
type formEditor struct {
	*editor
	requests chan acp.CreateElicitationRequest
}

func newFormEditor(resp acp.CreateElicitationResponse, err error) *formEditor {
	f := &formEditor{editor: &editor{}, requests: make(chan acp.CreateElicitationRequest, 8)}
	f.onElicit = func(_ context.Context, r acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
		f.requests <- r
		return resp, err
	}
	return f
}

func accept(content map[string]acp.ElicitationContentValue) acp.CreateElicitationResponse {
	return acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{Action: "accept", Content: content}}
}

var (
	decline = acp.CreateElicitationResponse{Decline: &acp.CreateElicitationDecline{Action: "decline"}}
	cancel  = acp.CreateElicitationResponse{Cancel: &acp.CreateElicitationCancel{Action: "cancel"}}
)

// withReplacement returns the question-batch trace with old replaced by new.
func withReplacement(tr trace, old, replacement string) trace {
	out := make(trace, len(tr))
	for i, data := range tr {
		out[i] = bytes.ReplaceAll(data, []byte(old), []byte(replacement))
	}
	return out
}

func backendFor(t *testing.T, tr trace) *fakeCrush {
	t.Helper()
	before, after := splitAtBatch(t, tr)
	backend := newFakeCrush(tr.sessionID(t), before)
	backend.onAnswer = after
	backend.onCancelQ = after
	return backend
}

func TestFormEditorGetsTheWholeBatchAsOneForm(t *testing.T) {
	yes := true
	ed := newFormEditor(accept(map[string]acp.ElicitationContentValue{
		"q0": str("fr"), "q0_custom": str("with a smile"), "q1": str("yes"), "q2": str("Ada"),
	}), nil)
	var perms int
	ed.onPermisson = func(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
		perms++
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(r.Options[0].OptionId)}
	}
	backend := questionBackend(t)
	conn := connect(t, backend, ed.editor)
	sid := initWith(t, conn, formCaps)

	resp := prompt(t, conn, sid, questionPrompt)

	assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	assert.Zero(t, perms, "no permission request")
	assert.EqualValues(t, 1, ed.elicited.Load(), "one elicitation for the whole batch")
	req := <-ed.requests
	require.NotNil(t, req.Form)
	out, err := json.Marshal(req)
	require.NoError(t, err)
	snaps.MatchJSON(t, out)

	got := backend.answeredBatches()
	require.Len(t, got, 1)
	assert.Equal(t, []crush.QuestionAnswer{
		{QuestionID: languageQ, SelectedIDs: []string{"fr"}, Notes: map[string]string{crush.QuestionNoteKey: "with a smile"}},
		{QuestionID: exclaimQ, Yes: &yes},
		{QuestionID: nameQ, FillInText: "Ada"},
	}, got[0].Answers)
	assert.Empty(t, backend.cancelledBatches())
}

func TestFormForMultiChoiceAndYesNoBatch(t *testing.T) {
	tr := withReplacement(loadTrace(t, "question-batch.sse"), `"type":"single_choice"`, `"type":"multi_choice"`)
	ed := newFormEditor(accept(map[string]acp.ElicitationContentValue{"q0": strs("en", "fr")}), nil)
	backend := backendFor(t, tr)
	conn := connect(t, backend, ed.editor)
	sid := initWith(t, conn, formCaps)

	prompt(t, conn, sid, questionPrompt)

	out, err := json.Marshal(<-ed.requests)
	require.NoError(t, err)
	snaps.MatchJSON(t, out)
	got := backend.answeredBatches()
	require.Len(t, got, 1)
	assert.Equal(t, []string{"en", "fr"}, got[0].Answers[0].SelectedIDs)
}

func TestCredentialBatchGoesToChatWithoutAForm(t *testing.T) {
	tr := withReplacement(loadTrace(t, "question-batch.sse"), "Whose name should be greeted?", "Enter your password")
	ed := newFormEditor(accept(nil), nil)
	backend := backendFor(t, tr)
	conn := connect(t, backend, ed.editor)
	sid := initWith(t, conn, formCaps)

	resp := prompt(t, conn, sid, questionPrompt)

	assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	assert.Zero(t, ed.elicited.Load(), "no form is shown for a credential")
	got := backend.answeredBatches()
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Answers[0].Notes[crush.QuestionNoteKey], "Do not call the question tool again")
}

func TestElicitationErrorFallsBackToChat(t *testing.T) {
	ed := newFormEditor(acp.CreateElicitationResponse{}, errors.New("method not found"))
	backend := questionBackend(t)
	conn := connect(t, backend, ed.editor)
	sid := initWith(t, conn, formCaps)

	resp := prompt(t, conn, sid, questionPrompt)

	assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	assert.EqualValues(t, 1, ed.elicited.Load())
	got := backend.answeredBatches()
	require.Len(t, got, 1)
	for _, a := range got[0].Answers {
		assert.Contains(t, a.Notes[crush.QuestionNoteKey], "Do not call the question tool again")
	}
	assert.Empty(t, backend.cancelledBatches())
}

func TestDeclinedFormContinuesTheTurnWithSkippedAnswers(t *testing.T) {
	ed := newFormEditor(decline, nil)
	backend := questionBackend(t)
	conn := connect(t, backend, ed.editor)
	sid := initWith(t, conn, formCaps)

	resp := prompt(t, conn, sid, questionPrompt)

	assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	got := backend.answeredBatches()
	require.Len(t, got, 1)
	assert.Equal(t, []crush.QuestionAnswer{{QuestionID: languageQ}, {QuestionID: exclaimQ}, {QuestionID: nameQ}}, got[0].Answers)
	assert.Empty(t, backend.cancelledBatches())
}

func TestCancelledFormCancelsTheBatch(t *testing.T) {
	ed := newFormEditor(cancel, nil)
	backend := questionBackend(t)
	_, after := splitAtBatch(t, loadTrace(t, "question-batch.sse"))
	backend.onCancelQ = after
	conn := connect(t, backend, ed.editor)
	sid := initWith(t, conn, formCaps)

	prompt(t, conn, sid, questionPrompt)

	assert.Equal(t, []string{questionBatch}, backend.cancelledBatches())
	assert.Empty(t, backend.answeredBatches())
}

func TestWithdrawnFormIsCancelledInTheEditorAndNeverAnsweredLate(t *testing.T) {
	type withdraw func(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, backend *fakeCrush, tr trace)
	tests := map[string]withdraw{
		"session cancel": func(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, _ *fakeCrush, _ trace) {
			require.NoError(t, conn.Cancel(t.Context(), acp.CancelNotification{SessionId: sid}))
		},
		"resolved elsewhere": func(t *testing.T, _ *acp.ClientSideConnection, _ acp.SessionId, backend *fakeCrush, tr trace) {
			_, after := splitAtBatch(t, tr)
			backend.push(after[:1])
		},
	}
	for name, withdraw := range tests {
		t.Run(name, func(t *testing.T) {
			tr := loadTrace(t, "question-batch.sse")
			before, _ := splitAtBatch(t, tr)
			backend := newFakeCrush(tr.sessionID(t), before)
			pending := make(chan struct{})
			withdrawn := make(chan struct{})
			ed := &editor{onElicit: func(ctx context.Context, _ acp.CreateElicitationRequest) (acp.CreateElicitationResponse, error) {
				close(pending)
				<-ctx.Done() // the agent's $/cancel_request ends this context
				close(withdrawn)
				return acp.CreateElicitationResponse{}, ctx.Err()
			}}
			conn := connect(t, backend, ed)
			sid := initWith(t, conn, formCaps)

			_ = promptAsync(t, conn, sid, questionPrompt)
			select {
			case <-pending:
			case <-time.After(2 * time.Second):
				require.FailNow(t, "the editor was never asked")
			}

			withdraw(t, conn, sid, backend, tr)

			select {
			case <-withdrawn:
			case <-time.After(2 * time.Second):
				require.FailNow(t, "the editor's request was not cancelled")
			}
			// Give a late call every chance to arrive.
			time.Sleep(100 * time.Millisecond)
			assert.Empty(t, backend.answeredBatches())
			assert.Empty(t, backend.cancelledBatches())
		})
	}
}

func TestEditorErrorOnAnEmptyFieldFallsBackToChat(t *testing.T) {
	// The SDK cannot encode a content value with no variant, so this editor
	// answers with an error instead: the batch must still reach the model.
	ed := newFormEditor(accept(map[string]acp.ElicitationContentValue{"q0": str("fr"), "q2": {}}), nil)
	backend := questionBackend(t)
	conn := connect(t, backend, ed.editor)
	sid := initWith(t, conn, formCaps)

	resp := prompt(t, conn, sid, questionPrompt)

	assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	got := backend.answeredBatches()
	require.Len(t, got, 1)
	for _, a := range got[0].Answers {
		assert.Contains(t, a.Notes[crush.QuestionNoteKey], "Do not call the question tool again")
	}
}

func TestNullFieldOnTheWireFallsBackToChat(t *testing.T) {
	// An editor that sends null for a cleared field makes the agent-side SDK
	// reject the whole response; the user must land in chat, not lose the turn.
	ed := newFormEditor(accept(map[string]acp.ElicitationContentValue{"q0": str("fr"), "q2": str("Ada")}), nil)
	backend := questionBackend(t)
	nullify := func(line []byte) []byte {
		if bytes.Contains(line, []byte(`"action":"accept"`)) {
			return bytes.Replace(line, []byte(`"q2":"Ada"`), []byte(`"q2":null`), 1)
		}
		return line
	}
	conn := serveRewritten(t, func(context.Context, string) (bridge.Backend, error) { return backend.open(), nil }, ed.editor, nullify).conn
	sid := initWith(t, conn, formCaps)

	resp := prompt(t, conn, sid, questionPrompt)

	assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	assert.EqualValues(t, 1, ed.elicited.Load())
	got := backend.answeredBatches()
	require.Len(t, got, 1)
	for _, a := range got[0].Answers {
		assert.Empty(t, a.SelectedIDs, "the editor's other answers are lost with the response")
		assert.Contains(t, a.Notes[crush.QuestionNoteKey], "Do not call the question tool again")
	}
	assert.Empty(t, backend.cancelledBatches())
}
