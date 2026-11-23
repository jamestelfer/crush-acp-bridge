package bridge

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

var _ acp.AgentLoader = (*Agent)(nil)

// LoadSession attaches to a stored Crush session and replays its messages to
// the editor as session updates, before answering.
func (a *Agent) LoadSession(ctx context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	sid := string(req.SessionId)
	backend, err := a.stored(ctx, req.Cwd, sid)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	messages, err := backend.Messages(ctx, sid)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	modes, err := a.attach(ctx, backend, sid, req.Cwd)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}

	// A fresh translator: the replay must not mark messages as sent for the
	// live stream, which never sees them again.
	tr := translate.New()
	for _, raw := range messages {
		updates, err := tr.FeedMessage(raw)
		if err != nil {
			a.log.Warn("stored message not translated", "session_id", sid, "error", err)
			continue
		}
		for i := range updates {
			a.forward(&updates[i])
		}
	}
	a.log.Info("session loaded", "session_id", sid, "cwd", req.Cwd, "messages", len(messages))
	return acp.LoadSessionResponse{
		ConfigOptions: a.sessionConfig(ctx, backend, sid),
		Modes:         modes,
	}, nil
}

// ResumeSession attaches to a stored Crush session without replaying it.
func (a *Agent) ResumeSession(ctx context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	sid := string(req.SessionId)
	backend, err := a.stored(ctx, req.Cwd, sid)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	modes, err := a.attach(ctx, backend, sid, req.Cwd)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	a.log.Info("session resumed", "session_id", sid, "cwd", req.Cwd)
	return acp.ResumeSessionResponse{
		ConfigOptions: a.sessionConfig(ctx, backend, sid),
		Modes:         modes,
	}, nil
}

// stored returns the backend for cwd after checking that Crush has the
// session there.
func (a *Agent) stored(ctx context.Context, cwd, sid string) (Backend, error) {
	backend, err := a.workspace(ctx, cwd)
	if err != nil {
		return nil, err
	}
	sessions, err := backend.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(sessions, func(s crush.Session) bool { return s.ID == sid }) {
		return nil, acp.NewInvalidParams(map[string]string{
			"error": fmt.Sprintf("crush has no session %q in %s", sid, cwd),
		})
	}
	return backend, nil
}

// ListSessions lists the conversations Crush holds for a directory, newest
// first. Without a directory it covers the directories this bridge has open,
// rather than open a new one. Sub-agent sessions and sessions with no
// messages are left out. Every session is returned on one page.
func (a *Agent) ListSessions(ctx context.Context, req acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	dirs := map[string]Backend{}
	if req.Cwd != nil {
		backend, err := a.workspace(ctx, *req.Cwd)
		if err != nil {
			return acp.ListSessionsResponse{}, err
		}
		dirs[*req.Cwd] = backend
	} else {
		a.mu.Lock()
		maps.Copy(dirs, a.workspaces)
		a.mu.Unlock()
	}

	type listed struct {
		info    acp.SessionInfo
		updated time.Time
	}
	var all []listed
	for cwd, backend := range dirs {
		sessions, err := backend.Sessions(ctx)
		if err != nil {
			return acp.ListSessionsResponse{}, err
		}
		for _, s := range sessions {
			if s.ParentID != "" || s.MessageCount == 0 {
				continue
			}
			info := acp.SessionInfo{SessionId: acp.SessionId(s.ID), Cwd: cwd}
			if s.Title != "" {
				info.Title = &s.Title
			}
			if !s.UpdatedAt.IsZero() {
				updated := s.UpdatedAt.UTC().Format(time.RFC3339)
				info.UpdatedAt = &updated
			}
			all = append(all, listed{info, s.UpdatedAt})
		}
	}
	slices.SortStableFunc(all, func(x, y listed) int {
		return cmp.Or(y.updated.Compare(x.updated), cmp.Compare(x.info.SessionId, y.info.SessionId))
	})

	out := make([]acp.SessionInfo, len(all))
	for i, l := range all {
		out[i] = l.info
	}
	return acp.ListSessionsResponse{Sessions: out}, nil
}
