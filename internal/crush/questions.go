package crush

import (
	"context"
	"fmt"
	"net/http"
)

// QuestionAnswer is the response to one question of a batch that Crush's
// question tool asked, as Crush's proto.QuestionResponse encodes it. An answer
// that sets nothing tells the model the user skipped the question.
type QuestionAnswer struct {
	QuestionID  string   `json:"request_id"`
	SelectedIDs []string `json:"selected_ids,omitempty"`
	FillInText  string   `json:"fill_in_text,omitempty"`
	Yes         *bool    `json:"yes,omitempty"`
}

// resolution is Crush's reply to a question answer or cancel. Resolved is
// false when the batch was no longer pending, such as when another client
// answered it first.
type resolution struct {
	Resolved bool `json:"resolved"`
}

// AnswerQuestions answers the pending question batch batchID, one answer per
// question. It reports whether this call resolved the batch.
func (w *Workspace) AnswerQuestions(ctx context.Context, batchID string, answers []QuestionAnswer) (bool, error) {
	body := struct {
		BatchID   string           `json:"batch_request_id"`
		Responses []QuestionAnswer `json:"responses"`
	}{batchID, answers}
	var out resolution
	if err := w.c.do(ctx, http.MethodPost, w.path("/questions/answer"), body, &out); err != nil {
		return false, fmt.Errorf("answer questions: %w", err)
	}
	return out.Resolved, nil
}

// CancelQuestions cancels the pending question batch batchID. Crush ends the
// turn that asked it. It reports whether this call resolved the batch.
func (w *Workspace) CancelQuestions(ctx context.Context, batchID string) (bool, error) {
	body := struct {
		BatchID string `json:"batch_request_id"`
	}{batchID}
	var out resolution
	if err := w.c.do(ctx, http.MethodPost, w.path("/questions/cancel"), body, &out); err != nil {
		return false, fmt.Errorf("cancel questions: %w", err)
	}
	return out.Resolved, nil
}
