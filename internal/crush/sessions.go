package crush

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Session is a stored Crush session.
type Session struct {
	ID           string
	ParentID     string // set for a sub-agent's session
	Title        string
	MessageCount int64
	UpdatedAt    time.Time
}

// Sessions lists the workspace's sessions, including sub-agent sessions.
func (w *Workspace) Sessions(ctx context.Context) ([]Session, error) {
	var raw []struct {
		ID           string `json:"id"`
		ParentID     string `json:"parent_session_id"`
		Title        string `json:"title"`
		MessageCount int64  `json:"message_count"`
		UpdatedAt    int64  `json:"updated_at"` // Unix seconds
	}
	if err := w.c.do(ctx, http.MethodGet, w.path("/sessions"), nil, &raw); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out := make([]Session, len(raw))
	for i, s := range raw {
		out[i] = Session{ID: s.ID, ParentID: s.ParentID, Title: s.Title, MessageCount: s.MessageCount}
		if s.UpdatedAt != 0 { // unset, not 1970
			out[i].UpdatedAt = time.Unix(s.UpdatedAt, 0)
		}
	}
	return out, nil
}

// Messages returns a session's messages in order, each in the form the event
// stream carries.
func (w *Workspace) Messages(ctx context.Context, sessionID string) ([]jsontext.Value, error) {
	var out []jsontext.Value
	if err := w.c.do(ctx, http.MethodGet, w.path("/sessions/"+url.PathEscape(sessionID)+"/messages"), nil, &out); err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return out, nil
}
