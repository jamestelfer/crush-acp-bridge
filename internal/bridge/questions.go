package bridge

import (
	"fmt"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// Option IDs for the answers that are not one of a question's choices.
const (
	optionYes  acp.PermissionOptionId = "yes"
	optionNo   acp.PermissionOptionId = "no"
	optionSkip acp.PermissionOptionId = "skip"
)

// answerField is the elicitation form field that holds a free-text answer.
const answerField = "answer"

// askQuestions shows a batch from Crush's question tool on its tool call,
// then asks the editor each question in turn, unless it already is.
func (a *Agent) askQuestions(s *session, u *translate.Update) {
	if !a.startAsking(u.BatchID) {
		return // re-delivered after a resubscribe while the editor answers it
	}
	// Paseo shows a permission request from the tool call it already holds,
	// so the questions go to the tool call first.
	a.send(acp.SessionId(u.SessionID), acp.UpdateToolCall(
		acp.ToolCallId(u.ToolCallID),
		acp.WithUpdateTitle(batchTitle(u.Questions)),
		acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(batchText(u.Questions)))}),
	))
	// The editor may take minutes to answer; do not stall the event loop.
	go a.answerQuestions(a.backend(s.cwd), *u)
}

// answerQuestions asks the editor each question of the batch and sends the
// answers to Crush. A question the editor dismisses cancels the batch, which
// ends Crush's turn.
func (a *Agent) answerQuestions(backend Backend, u translate.Update) {
	answers := make([]crush.QuestionAnswer, 0, len(u.Questions))
	for i, q := range u.Questions {
		ans, ok := a.askQuestion(u, i)
		if !ok {
			a.settleQuestions(u, "cancel", func() (bool, error) {
				return backend.CancelQuestions(a.serveCtx, u.BatchID)
			})
			return
		}
		ans.QuestionID = q.ID
		answers = append(answers, ans)
	}
	a.settleQuestions(u, "answer", func() (bool, error) {
		return backend.AnswerQuestions(a.serveCtx, u.BatchID, answers)
	})
}

// settleQuestions sends the batch's outcome to Crush. A batch that reached
// Crush stays recorded as asked, so a late re-delivery is not asked again; one
// that did not may be re-delivered and asked again.
func (a *Agent) settleQuestions(u translate.Update, outcome string, send func() (bool, error)) {
	resolved, err := send()
	if err != nil {
		a.log.Warn("question outcome not delivered",
			"error", err, "outcome", outcome, "batch_id", u.BatchID, "session_id", u.SessionID)
		a.doneAsking(u.BatchID)
		return
	}
	a.log.Info("questions settled",
		"outcome", outcome, "resolved", resolved, "questions", len(u.Questions),
		"batch_id", u.BatchID, "session_id", u.SessionID)
}

// askQuestion asks the editor question i of the batch. It returns false when
// the editor dismissed it or could not be asked.
func (a *Agent) askQuestion(u translate.Update, i int) (crush.QuestionAnswer, bool) {
	q := u.Questions[i]
	title := q.Text
	if n := len(u.Questions); n > 1 {
		title = fmt.Sprintf("(%d/%d) %s", i+1, n, q.Text)
	}
	text := questionText(q)
	if q.Type == translate.FreeText {
		if a.formElicitation.Load() {
			return a.elicitText(u, q, title)
		}
		text += "\n\nThis editor cannot send a typed answer here: skip the question, then reply in the chat."
	}

	resp, err := a.conn.RequestPermission(a.serveCtx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(u.SessionID),
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(u.ToolCallID),
			Title:      acp.PatchSet(title),
			Kind:       acp.PatchSet(acp.ToolKindOther),
			Status:     acp.PatchSet(acp.ToolCallStatusPending),
			Content:    acp.PatchSet([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(strings.TrimSpace(text)))}),
		},
		Options: questionOptions(q),
	})
	if err != nil {
		a.log.Warn("question not asked", "error", err, "batch_id", u.BatchID, "session_id", u.SessionID)
		return crush.QuestionAnswer{}, false
	}
	if resp.Outcome.Selected == nil {
		return crush.QuestionAnswer{}, false
	}
	return selectedAnswer(q, resp.Outcome.Selected.OptionId), true
}

// questionOptions are the editor's choices for q. A permission request takes
// no text, so a free-text question can only be skipped, and a multi-choice
// question takes one choice.
func questionOptions(q translate.Question) []acp.PermissionOption {
	switch q.Type {
	case translate.YesNo:
		return []acp.PermissionOption{
			{OptionId: optionYes, Name: "Yes", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: optionNo, Name: "No", Kind: acp.PermissionOptionKindRejectOnce},
		}
	case translate.SingleChoice, translate.MultiChoice:
		opts := make([]acp.PermissionOption, 0, len(q.Choices))
		for _, c := range q.Choices {
			opts = append(opts, acp.PermissionOption{
				OptionId: acp.PermissionOptionId(c.ID), Name: c.Label, Kind: acp.PermissionOptionKindAllowOnce,
			})
		}
		return opts
	default:
		return []acp.PermissionOption{
			{OptionId: optionSkip, Name: "Skip", Kind: acp.PermissionOptionKindRejectOnce},
		}
	}
}

// selectedAnswer is the answer to q that the option id stands for. An option
// that names nothing skips the question.
func selectedAnswer(q translate.Question, id acp.PermissionOptionId) crush.QuestionAnswer {
	switch q.Type {
	case translate.YesNo:
		switch id {
		case optionYes:
			return crush.QuestionAnswer{Yes: new(true)}
		case optionNo:
			return crush.QuestionAnswer{Yes: new(false)}
		}
	case translate.SingleChoice, translate.MultiChoice:
		for _, c := range q.Choices {
			if c.ID == string(id) {
				return crush.QuestionAnswer{SelectedIDs: []string{c.ID}}
			}
		}
	}
	return crush.QuestionAnswer{}
}

// elicitText asks a free-text question through a form. Declining skips the
// question; cancelling, or a failed request, dismisses it.
func (a *Agent) elicitText(u translate.Update, q translate.Question, title string) (crush.QuestionAnswer, bool) {
	toolCall := acp.ToolCallId(u.ToolCallID)
	field := acp.ElicitationPropertySchemaString{Type: "string", Title: &q.Text}
	if q.Description != "" {
		field.Description = &q.Description
	}
	resp, err := a.conn.CreateElicitation(a.serveCtx, acp.CreateElicitationRequest{Form: &acp.CreateElicitationForm{
		Mode:    "form",
		Message: title,
		RequestedSchema: acp.ElicitationSchema{
			Type:       "object",
			Properties: map[string]acp.ElicitationPropertySchema{answerField: {String: &field}},
			Required:   []string{answerField},
		},
		Session: &acp.ElicitationSessionScope{SessionId: acp.SessionId(u.SessionID), ToolCallId: &toolCall},
	}})
	switch {
	case err != nil:
		a.log.Warn("question not asked", "error", err, "batch_id", u.BatchID, "session_id", u.SessionID)
		return crush.QuestionAnswer{}, false
	case resp.Accept != nil:
		if v, ok := resp.Accept.Content[answerField]; ok && v.String != nil {
			return crush.QuestionAnswer{FillInText: string(*v.String)}, true
		}
		return crush.QuestionAnswer{}, true
	case resp.Decline != nil:
		return crush.QuestionAnswer{}, true
	default:
		return crush.QuestionAnswer{}, false
	}
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
	if q.Type == translate.MultiChoice {
		b.WriteString("\n\nPick one; this editor takes one choice per question.")
	}
	return strings.TrimSpace(b.String())
}
