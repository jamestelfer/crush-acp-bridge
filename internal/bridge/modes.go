package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// Crush's main agents. Crush starts every workspace on coder; plan analyses
// and plans without modifying files.
const (
	modeCoder acp.SessionModeId = "coder"
	modePlan  acp.SessionModeId = "plan"
)

var (
	coderDescription = "Executes coding tasks, with every tool available."
	planDescription  = "Analyses and prepares implementation plans without modifying files."
	crushModes       = []acp.SessionMode{
		{Id: modeCoder, Name: "Coder", Description: &coderDescription},
		{Id: modePlan, Name: "Plan", Description: &planDescription},
	}
)

func modeState(current acp.SessionModeId) *acp.SessionModeState {
	return &acp.SessionModeState{AvailableModes: crushModes, CurrentModeId: current}
}

// modeLocked returns Crush's active agent for cwd. Crush offers no way to read
// it, so the bridge tracks what it set; a.mu must be held.
func (a *Agent) modeLocked(cwd string) acp.SessionModeId {
	if m, ok := a.modes[cwd]; ok {
		return m
	}
	return modeCoder
}

// SetSessionMode switches Crush's main agent. Crush keeps one agent per
// directory, so every session there changes mode, and the others are told.
func (a *Agent) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	if req.ModeId != modeCoder && req.ModeId != modePlan {
		return acp.SetSessionModeResponse{}, acp.NewInvalidParams(map[string]string{
			"error": fmt.Sprintf("crush has no %q mode", req.ModeId),
		})
	}
	s := a.session(string(req.SessionId))
	if s == nil {
		return acp.SetSessionModeResponse{}, fmt.Errorf("unknown session %q", req.SessionId)
	}

	if err := s.backend().SetMainAgent(ctx, string(req.ModeId)); err != nil {
		a.log.Warn("mode change failed", "session_id", string(req.SessionId), "mode", string(req.ModeId), "error", err)
		if errors.Is(err, crush.ErrBusy) {
			return acp.SetSessionModeResponse{}, acp.NewRequestError(-32603, "crush is busy; change mode when the turn ends", nil)
		}
		return acp.SetSessionModeResponse{}, err
	}

	a.mu.Lock()
	a.modes[s.cwd] = req.ModeId
	var others []acp.SessionId
	for id, other := range a.sessions {
		if other.cwd == s.cwd && id != string(req.SessionId) {
			others = append(others, acp.SessionId(id))
		}
	}
	a.mu.Unlock()
	a.log.Info("mode changed", "session_id", string(req.SessionId), "mode", string(req.ModeId), "cwd", s.cwd)

	for _, id := range others {
		a.send(id, acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{
			CurrentModeId: req.ModeId, SessionUpdate: "current_mode_update",
		}})
	}
	return acp.SetSessionModeResponse{}, nil
}
