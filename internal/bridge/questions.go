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

	ctx := a.beginBatch(u)
	// A handler may wait minutes on the editor; do not stall the event loop.
	go func(u translate.Update, backend Backend) {
		defer a.endBatch(u.BatchID)
		if err := a.questions.Handle(ctx, backend, u); err != nil {
			a.log.Warn("question outcome not delivered",
				"error", err, "batch_id", u.BatchID, "session_id", u.SessionID)
			a.doneAsking(u.BatchID)
		}
	}(*u, a.backend(s.cwd))
}

// beginBatch returns the context a batch runs under. Registering happens on
// the event loop, so a later event for the batch always finds it.
func (a *Agent) beginBatch(u *translate.Update) context.Context {
	ctx, cancel := context.WithCancel(a.serveCtx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.batches[u.BatchID] = batchRun{sessionID: u.SessionID, toolCallID: u.ToolCallID, cancel: cancel}
	return ctx
}

func (a *Agent) endBatch(batchID string) {
	a.mu.Lock()
	b, ok := a.batches[batchID]
	delete(a.batches, batchID)
	a.mu.Unlock()
	if ok {
		b.cancel()
	}
}

// cancelBatches cancels the active batches that match, and leaves them
// recorded as asked so a late re-delivery is not handled again.
func (a *Agent) cancelBatches(match func(id string, b batchRun) bool) {
	a.mu.Lock()
	var cancels []context.CancelFunc
	for id, b := range a.batches {
		if match(id, b) {
			cancels = append(cancels, b.cancel)
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
