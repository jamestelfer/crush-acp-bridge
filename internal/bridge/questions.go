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
// ends Crush's turn. A batch that reached Crush stays recorded as asked, so a
// late re-delivery is not asked again; one that did not may be re-delivered
// and asked again.
func (a *Agent) answerQuestions(backend Backend, u translate.Update) {
	outcome := "answer"
	answers := make([]crush.QuestionAnswer, 0, len(u.Questions))
	for i, q := range u.Questions {
		ans, ok := a.askQuestion(u, q, i)
		if !ok {
			outcome = "cancel"
			break
		}
		ans.QuestionID = q.ID
		answers = append(answers, ans)
	}

	var resolved bool
	var err error
	if outcome == "cancel" {
		resolved, err = backend.CancelQuestions(a.serveCtx, u.BatchID)
	} else {
		resolved, err = backend.AnswerQuestions(a.serveCtx, u.BatchID, answers)
	}
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

// askQuestion asks the editor q, question i of the batch. It returns false
// when the editor dismissed it or could not be asked.
func (a *Agent) askQuestion(u translate.Update, q translate.Question, i int) (crush.QuestionAnswer, bool) {
	title := q.Text
	if n := len(u.Questions); n > 1 {
		title = fmt.Sprintf("(%d/%d) %s", i+1, n, q.Text)
	}
	forms := a.formElicitation.Load()
	switch {
	case q.Type == translate.FreeText && forms:
		return a.elicitText(u, q, title)
	case q.Type == translate.MultiChoice && forms:
		return a.elicitChoices(u, q, title)
	}
	text := questionText(q)
	switch q.Type {
	case translate.FreeText:
		text += "\n\nThis editor cannot send a typed answer here: skip the question, then reply in the chat."
	case translate.MultiChoice:
		text += "\n\nPick one; this editor takes one choice per question."
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
// question takes one choice. ACP has no option kind for an answer, so Yes and
// every choice are allow_once: an editor that approves permission requests
// unasked answers Yes, or the first choice.
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

// elicitText asks a free-text question through a form.
func (a *Agent) elicitText(u translate.Update, q translate.Question, title string) (crush.QuestionAnswer, bool) {
	field := acp.ElicitationPropertySchemaString{Type: "string", Title: &q.Text}
	if q.Description != "" {
		field.Description = &q.Description
	}
	v, ok := a.elicit(u, title, acp.ElicitationPropertySchema{String: &field}, true)
	if !ok {
		return crush.QuestionAnswer{}, false
	}
	if v != nil && v.String != nil {
		return crush.QuestionAnswer{FillInText: string(*v.String)}, true
	}
	return crush.QuestionAnswer{}, true
}

// elicitChoices asks a multi-choice question through a form, so the editor can
// pick several choices. Selecting none skips the question.
func (a *Agent) elicitChoices(u translate.Update, q translate.Question, title string) (crush.QuestionAnswer, bool) {
	opts := make([]acp.EnumOption, 0, len(q.Choices))
	for _, c := range q.Choices {
		o := acp.EnumOption{Const: c.ID, Title: c.Label}
		if c.Description != "" {
			o.Description = &c.Description
		}
		opts = append(opts, o)
	}
	field := acp.ElicitationPropertySchemaArray{
		Type:  "array",
		Title: &q.Text,
		Items: acp.MultiSelectItems{Titled: &acp.TitledMultiSelectItems{AnyOf: opts}},
	}
	if q.Description != "" {
		field.Description = &q.Description
	}
	v, ok := a.elicit(u, title, acp.ElicitationPropertySchema{Array: &field}, false)
	if !ok {
		return crush.QuestionAnswer{}, false
	}
	var ans crush.QuestionAnswer
	if v != nil && v.StringArray != nil {
		for _, id := range *v.StringArray {
			for _, c := range q.Choices {
				if c.ID == id {
					ans.SelectedIDs = append(ans.SelectedIDs, id)
					break
				}
			}
		}
	}
	return ans, true
}

// elicit asks the editor for one form field on the question tool call. It
// returns the field's value, nil when the editor declined or left it empty.
// Cancelling, or a failed request, dismisses the question and returns false.
func (a *Agent) elicit(u translate.Update, title string, field acp.ElicitationPropertySchema, required bool) (*acp.ElicitationContentValue, bool) {
	toolCall := acp.ToolCallId(u.ToolCallID)
	schema := acp.ElicitationSchema{
		Type:       "object",
		Properties: map[string]acp.ElicitationPropertySchema{answerField: field},
	}
	if required {
		schema.Required = []string{answerField}
	}
	resp, err := a.conn.CreateElicitation(a.serveCtx, acp.CreateElicitationRequest{Form: &acp.CreateElicitationForm{
		Mode:            "form",
		Message:         title,
		RequestedSchema: schema,
		Session:         &acp.ElicitationSessionScope{SessionId: acp.SessionId(u.SessionID), ToolCallId: &toolCall},
	}})
	switch {
	case err != nil:
		a.log.Warn("question not asked", "error", err, "batch_id", u.BatchID, "session_id", u.SessionID)
		return nil, false
	case resp.Accept != nil:
		if v, ok := resp.Accept.Content[answerField]; ok {
			return &v, true
		}
		return nil, true
	case resp.Decline != nil:
		return nil, true
	default:
		return nil, false
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
	return strings.TrimSpace(b.String())
}
