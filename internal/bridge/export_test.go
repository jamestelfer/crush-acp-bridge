package bridge

import (
	"context"

	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// QuestionHandler is the question strategy seam, exported for tests.
type QuestionHandler interface {
	Handle(ctx context.Context, backend Backend, u translate.Update) error
}

// ChatFallbackQuestionHandler is the default strategy.
type ChatFallbackQuestionHandler = chatFallbackQuestionHandler

// WithQuestionHandler replaces the question strategy.
func WithQuestionHandler(h QuestionHandler) Option {
	return func(a *Agent) { a.questions = h }
}
