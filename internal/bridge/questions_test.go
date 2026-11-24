package bridge_test

import (
	"encoding/json/v2"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// The batch in question-batch.sse: a single_choice, a yes_no and a free_text
// question.
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

// asker is an editor that answers each question by picking an option by its
// name, and records what it was asked.
type asker struct {
	mu        sync.Mutex
	asked     []acp.RequestPermissionRequest
	elicited  []acp.CreateElicitationRequest
	pick      map[string]string // option name to pick, by question title; else the first option
	dismissAt int               // 1-based request to dismiss; 0 answers all
}

func (q *asker) permission(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.asked = append(q.asked, r)
	if len(q.asked) == q.dismissAt {
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	}
	title, _ := r.ToolCall.Title.Value()
	if name, ok := q.pick[title]; ok {
		for _, o := range r.Options {
			if o.Name == name {
				return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(o.OptionId)}
			}
		}
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(r.Options[0].OptionId)}
}

func (q *asker) elicit(r acp.CreateElicitationRequest) acp.CreateElicitationResponse {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.elicited = append(q.elicited, r)
	return acp.CreateElicitationResponse{Accept: &acp.CreateElicitationAccept{
		Action:  "accept",
		Content: map[string]acp.ElicitationContentValue{"answer": acp.NewElicitationContentValueString("Ada")},
	}}
}

func (q *asker) requests() []acp.RequestPermissionRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]acp.RequestPermissionRequest(nil), q.asked...)
}

// awaitAnswered waits for the bridge to answer or cancel a question batch.
func awaitAnswered(t *testing.T, backend *fakeCrush) {
	t.Helper()
	require.Eventually(t, func() bool {
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return len(backend.answered)+len(backend.cancelledQ) > 0
	}, 2*time.Second, 10*time.Millisecond, "the question batch was neither answered nor cancelled")
}

func (f *fakeCrush) questionOutcome() ([]answered, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]answered(nil), f.answered...), append([]string(nil), f.cancelledQ...)
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

var formElicitation = acp.ClientCapabilities{
	Elicitation: &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}},
}

func TestEditorViewOfQuestionTurn(t *testing.T) {
	tr := loadTrace(t, "question-batch.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	q := &asker{}
	ed := &editor{onPermisson: q.permission}
	conn := connect(t, backend, ed)
	sid := start(t, conn)

	prompt(t, conn, sid, questionPrompt)
	awaitAnswered(t, backend)

	answers, cancelled := backend.questionOutcome()
	out, err := json.Marshal(struct {
		Updates   []acp.SessionUpdate            `json:"updates"`
		Asked     []acp.RequestPermissionRequest `json:"asked"`
		Answered  []answered                     `json:"answered"`
		Cancelled []string                       `json:"cancelled"`
	}{ed.snapshot(), q.requests(), answers, cancelled})
	require.NoError(t, err)
	snaps.MatchJSON(t, out)
}

func TestFreeTextQuestionUsesAFormWhenTheEditorOffersOne(t *testing.T) {
	tr := loadTrace(t, "question-batch.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	q := &asker{}
	ed := &editor{onPermisson: q.permission, onElicit: q.elicit}
	conn := connect(t, backend, ed)
	sid := initWith(t, conn, formElicitation)

	prompt(t, conn, sid, questionPrompt)
	awaitAnswered(t, backend)

	answers, _ := backend.questionOutcome()
	require.Len(t, answers, 1)
	assert.Equal(t, crush.QuestionAnswer{QuestionID: nameQ, FillInText: "Ada"}, answers[0].Answers[2])
	assert.Len(t, q.requests(), 2, "the choice questions still go through a permission request")

	q.mu.Lock()
	defer q.mu.Unlock()
	out, err := json.Marshal(q.elicited)
	require.NoError(t, err)
	snaps.MatchJSON(t, out)
}

func TestQuestionAnswersAreRelayedToCrush(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name string
		pick map[string]string
		want []crush.QuestionAnswer
	}{
		{
			name: "first options",
			want: []crush.QuestionAnswer{
				{QuestionID: languageQ, SelectedIDs: []string{"en"}},
				{QuestionID: exclaimQ, Yes: &yes},
				{QuestionID: nameQ},
			},
		},
		{
			name: "other options",
			pick: map[string]string{
				"(1/3) Should the greeting be in English or French?": "French",
				"(2/3) Should we end it with an exclamation mark?":   "No",
			},
			want: []crush.QuestionAnswer{
				{QuestionID: languageQ, SelectedIDs: []string{"fr"}},
				{QuestionID: exclaimQ, Yes: &no},
				{QuestionID: nameQ},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadTrace(t, "question-batch.sse")
			backend := newFakeCrush(tr.sessionID(t), tr)
			q := &asker{pick: tc.pick}
			conn := connect(t, backend, &editor{onPermisson: q.permission})
			sid := start(t, conn)

			prompt(t, conn, sid, questionPrompt)
			awaitAnswered(t, backend)

			answers, cancelled := backend.questionOutcome()
			assert.Empty(t, cancelled)
			require.Len(t, answers, 1)
			assert.Equal(t, questionBatch, answers[0].BatchID)
			assert.Equal(t, tc.want, answers[0].Answers)
			for _, r := range q.requests() {
				assert.Equal(t, sid, r.SessionId)
				assert.Equal(t, acp.ToolCallId("call_5373993239042528834"), r.ToolCall.ToolCallId)
			}
		})
	}
}

func TestDismissedQuestionCancelsTheBatch(t *testing.T) {
	tr := loadTrace(t, "question-batch.sse")
	backend := newFakeCrush(tr.sessionID(t), tr)
	q := &asker{dismissAt: 2}
	conn := connect(t, backend, &editor{onPermisson: q.permission})
	sid := start(t, conn)

	prompt(t, conn, sid, questionPrompt)
	awaitAnswered(t, backend)

	answers, cancelled := backend.questionOutcome()
	assert.Empty(t, answers)
	assert.Equal(t, []string{questionBatch}, cancelled)
	assert.Len(t, q.requests(), 2, "no question is asked after one is dismissed")
}

func TestQuestionBatchRedeliveredWhileAskedIsAskedOnce(t *testing.T) {
	tr := loadTrace(t, "question-batch.sse")
	var batch []byte
	for _, data := range tr {
		var ev struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(data, &ev))
		if ev.Type == "question_batch_request" {
			batch = data
		}
	}
	require.NotNil(t, batch)
	// Crush re-delivers a pending batch after a resubscribe.
	backend := newFakeCrush(tr.sessionID(t), append(trace{batch, batch}, tr...))
	q := &asker{}
	conn := connect(t, backend, &editor{onPermisson: q.permission})
	sid := start(t, conn)

	prompt(t, conn, sid, questionPrompt)
	awaitAnswered(t, backend)

	answers, _ := backend.questionOutcome()
	assert.Len(t, answers, 1)
	assert.Len(t, q.requests(), 3)
}
