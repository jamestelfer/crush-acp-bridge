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
	s, err := a.lookup(req.SessionId)
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}

	if err := a.backend(s.cwd).SetMainAgent(ctx, string(req.ModeId)); err != nil {
		a.log.Warn("mode change failed", "session_id", string(req.SessionId), "mode", string(req.ModeId), "error", err)
		if errors.Is(err, crush.ErrBusy) {
			return acp.SetSessionModeResponse{}, internalError("crush is busy; change mode when the turn ends")
		}
		return acp.SetSessionModeResponse{}, err
	}

	a.mu.Lock()
	a.modes[s.cwd] = req.ModeId
	a.mu.Unlock()
	a.log.Info("mode changed", "session_id", string(req.SessionId), "mode", string(req.ModeId), "cwd", s.cwd)

	for id := range a.sessionsIn(s.cwd) {
		if id == string(req.SessionId) {
			continue
		}
		a.send(acp.SessionId(id), acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{
			CurrentModeId: req.ModeId, SessionUpdate: "current_mode_update",
		}})
	}
	return acp.SetSessionModeResponse{}, nil
}
