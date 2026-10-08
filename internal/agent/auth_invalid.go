package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

var ErrAgentAuthInvalid = errors.New("agent Server authentication is invalid")

type agentAuthenticationKey struct{}

type agentAuthentication struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
}

// One context-owned state per Run, installed before any worker starts. It does
// not change the shared HTTP client or affect Clash/probe-target requests.
func withAgentAuthentication(parent context.Context) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	state := &agentAuthentication{ctx: ctx, cancel: cancel}
	return context.WithValue(ctx, agentAuthenticationKey{}, state), cancel
}

// Only Agent-to-Server authenticated call sites use this boundary. Existing
// HTTP timeouts and response limits still apply; a 401 does not require a
// readable/recognized error message. Only explicit agent_revoked is terminal.
func doAgentServerRequest(client *http.Client, request *http.Request) (*http.Response, error) {
	state, _ := request.Context().Value(agentAuthenticationKey{}).(*agentAuthentication)
	if state != nil && state.ctx.Err() != nil {
		return nil, context.Cause(state.ctx)
	}
	if request.Context().Err() != nil {
		return nil, context.Cause(request.Context())
	}
	response, err := client.Do(request)
	if err != nil || state == nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	// The original request context/client timeout bounds a real response body.
	// Keep classification small, close it, and never log arbitrary response text.
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	_ = response.Body.Close()
	if state.ctx.Err() != nil {
		return nil, context.Cause(state.ctx)
	}
	if request.Context().Err() != nil {
		return nil, context.Cause(request.Context())
	}
	cause := ErrAgentAuthInvalid
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if readErr == nil && len(body) <= 4096 && json.Unmarshal(body, &envelope) == nil && envelope.Error.Code == "agent_revoked" {
		cause = ErrAgentRevoked
	}
	state.cancel(cause)
	return nil, context.Cause(state.ctx)
}

func authenticationFailure(ctx context.Context) error {
	cause := context.Cause(ctx)
	if errors.Is(cause, ErrAgentAuthInvalid) || errors.Is(cause, ErrAgentRevoked) {
		return cause
	}
	return nil
}

func (r *Runner) waitForAuthConfigurationRestart(parent context.Context) error {
	r.logger.Error("Server authentication invalid; update server/token configuration and restart Agent")
	<-parent.Done()
	return nil
}
