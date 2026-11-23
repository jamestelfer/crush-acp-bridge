package bridge

import (
	"context"

	"github.com/coder/acp-go-sdk"
)

// unsupported answers every optional ACP method with "method not found".
// Agent embeds it so only implemented methods appear in agent.go.
type unsupported struct{}

func (unsupported) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

func (unsupported) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound("logout")
}

func (unsupported) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound("session/close")
}

func (unsupported) DeleteSession(context.Context, acp.DeleteSessionRequest) (acp.DeleteSessionResponse, error) {
	return acp.DeleteSessionResponse{}, acp.NewMethodNotFound("session/delete")
}
