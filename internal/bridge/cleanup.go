package bridge

import (
	"context"
	"slices"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// deleteEmpty deletes the sessions this bridge created and never prompted,
// when Crush reports them idle and without messages. Editors open sessions
// they never use, such as Paseo's provider probes; without this, each one
// stays in Crush's history. Another client may have used such a session, so
// Crush's count decides, and any doubt keeps the session.
func (a *Agent) deleteEmpty(ctx context.Context) {
	candidates := map[string][]string{} // session IDs by directory
	a.mu.Lock()
	for id, s := range a.sessions {
		if s.created && !s.prompted.Load() {
			candidates[s.cwd] = append(candidates[s.cwd], id)
		}
	}
	a.mu.Unlock()

	for cwd, ids := range candidates {
		backend := a.backend(cwd)
		if backend == nil {
			continue
		}
		stored, err := backend.Sessions(ctx)
		if err != nil {
			a.log.Warn("empty sessions not deleted", "cwd", cwd, "error", err)
			continue
		}
		for _, id := range ids {
			i := slices.IndexFunc(stored, func(s crush.Session) bool { return s.ID == id })
			if i < 0 || stored[i].MessageCount > 0 || stored[i].Busy {
				continue
			}
			if err := backend.DeleteSession(ctx, id); err != nil {
				a.log.Warn("empty session not deleted", "session_id", id, "cwd", cwd, "error", err)
				continue
			}
			a.log.Info("empty session deleted", "session_id", id, "cwd", cwd)
		}
	}
}
