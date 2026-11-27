package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// questionHandler settles one batch from Crush's question tool. It returns an
// error only when the outcome did not reach Crush; the batch may then be
// re-delivered and handled again. ctx ends when the batch no longer needs an
// answer, and the handler must not post one after that.
type questionHandler interface {
	Handle(ctx context.Context, backend Backend, u translate.Update) error
}

// batchRun is an active question batch's cancellation.
type batchRun struct {
	run        uint64 // tells this registration from a later one for the same batch
	sessionID  string
	toolCallID string
	cancel     context.CancelFunc
}

func (a *Agent) questionEvent(s *session, u *translate.Update) {
	if u.Kind == translate.QuestionAsked {
		a.askQuestions(s, u)
		return
	}
	a.cancelBatches(func(id string, _ batchRun) bool { return id == u.BatchID })
}

// askQuestions shows a batch on its tool call, then hands it to the question
// handler, unless it already is handling it.
func (a *Agent) askQuestions(s *session, u *translate.Update) {
	if !a.startAsking(u.BatchID) {
		return // re-delivered after a resubscribe while the handler works on it
	}
	// Paseo shows a request from the tool call it already holds, so the
	// questions go to the tool call first.
	a.send(acp.SessionId(u.SessionID), acp.UpdateToolCall(
		acp.ToolCallId(u.ToolCallID),
		acp.WithUpdateTitle(batchTitle(u.Questions)),
		acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(batchText(u.Questions)))}),
	))

	ctx, run := a.beginBatch(u)
	// A handler may wait minutes on the editor; do not stall the event loop.
	go func(u translate.Update, backend Backend) {
		if err := a.questions.Handle(ctx, backend, u); err != nil {
			a.log.Warn("question outcome not delivered",
				"error", err, "batch_id", u.BatchID, "session_id", u.SessionID)
			a.dropBatch(u.BatchID, run)
		}
	}(*u, a.backend(s.cwd))
}

// beginBatch returns the context a batch runs under. A batch stays registered
// after it is answered, until Crush resolves it, its tool call finishes or the
// session is cancelled, so a re-delivery in between is ignored. Registering
// happens on the event loop, so a later event for the batch always finds it.
func (a *Agent) beginBatch(u *translate.Update) (context.Context, uint64) {
	ctx, cancel := context.WithCancel(a.serveCtx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.batchRuns++
	a.batches[u.BatchID] = batchRun{run: a.batchRuns, sessionID: u.SessionID, toolCallID: u.ToolCallID, cancel: cancel}
	return ctx, a.batchRuns
}

// dropBatch forgets the registration run made for a batch whose outcome did
// not reach Crush, so Crush's re-delivery of it is handled again. A later
// registration of the same batch, after it was resolved and re-sent, is left
// alone.
func (a *Agent) dropBatch(batchID string, run uint64) {
	a.mu.Lock()
	b, ok := a.batches[batchID]
	ok = ok && b.run == run
	if ok {
		delete(a.batches, batchID)
		delete(a.asking, batchID)
	}
	a.mu.Unlock()
	if ok {
		b.cancel()
	}
}

// cancelBatches cancels the active batches that match and forgets them as
// asked: a batch Crush resolved or whose tool call finished is never
// re-delivered as pending.
func (a *Agent) cancelBatches(match func(id string, b batchRun) bool) {
	a.mu.Lock()
	var cancels []context.CancelFunc
	for id, b := range a.batches {
		if match(id, b) {
			cancels = append(cancels, b.cancel)
			delete(a.batches, id)
			delete(a.asking, id)
		}
	}
	a.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// chatFallbackNote tells the model why its questions were not asked.
const chatFallbackNote = "The user's client cannot show interactive question forms, so none of these questions " +
	"was asked and no answer was given. Do not call the question tool again. Ask the user in plain text, " +
	"or state your assumptions and proceed."

// chatFallbackQuestionHandler answers a batch at once with empty answers and a
// note that sends the model back to asking in chat. It never asks the editor.
type chatFallbackQuestionHandler struct{}

func (chatFallbackQuestionHandler) Handle(ctx context.Context, backend Backend, u translate.Update) error {
	answers := make([]crush.QuestionAnswer, len(u.Questions))
	for i, q := range u.Questions {
		answers[i] = crush.QuestionAnswer{
			QuestionID: q.ID,
			Notes:      map[string]string{crush.QuestionNoteKey: chatFallbackNote},
		}
	}
	// A cancelled or externally resolved batch needs no answer; one now would
	// be late.
	if ctx.Err() == nil {
		if _, err := backend.AnswerQuestions(ctx, u.BatchID, answers); err != nil && ctx.Err() == nil {
			return err
		}
	}
	return nil
}

// batchTitle is the question tool call's title while the batch waits.
func batchTitle(qs []translate.Question) string {
	if len(qs) == 1 {
		return qs[0].Text
	}
	return fmt.Sprintf("%d questions", len(qs))
}

// batchText lists every question of the batch, for the tool call's content.
func batchText(qs []translate.Question) string {
	parts := make([]string, len(qs))
	for i, q := range qs {
		parts[i] = "**" + q.Text + "**\n\n" + questionText(q)
	}
	return strings.Join(parts, "\n\n")
}

// questionText is a question's description followed by its choices.
func questionText(q translate.Question) string {
	var b strings.Builder
	b.WriteString(q.Description)
	if len(q.Choices) > 0 && q.Description != "" {
		b.WriteString("\n")
	}
	for _, c := range q.Choices {
		b.WriteString("\n- " + c.Label)
		if c.Description != "" {
			b.WriteString(": " + c.Description)
		}
	}
	return strings.TrimSpace(b.String())
}
