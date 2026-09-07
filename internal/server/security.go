package server

import (
	"errors"
	"net/http"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func (a *App) handleAgentSecurity(w http.ResponseWriter, r *http.Request) {
	agentID, ok := a.authenticateJobRequest(w, r, "authenticate security submission")
	if !ok {
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, protocol.MaxSecurityBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	submission, err := protocol.DecodeSecuritySubmission(body)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid security submission")
		return
	}
	changed, err := a.store.SaveSecuritySubmission(r.Context(), agentID, submission, a.now())
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentRevoked):
			writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
		case errors.Is(err, storage.ErrAgentDisabled):
			writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
		case errors.Is(err, storage.ErrUnauthorized):
			writeJobError(w, http.StatusUnauthorized, "unauthorized", "invalid agent token")
		case errors.Is(err, storage.ErrSecurityFence):
			writeJobError(w, http.StatusConflict, "stale_session", "security submission session is stale")
		case errors.Is(err, storage.ErrSecurityConflict):
			writeJobError(w, http.StatusConflict, "batch_conflict", "batch ID conflicts with stored content")
		default:
			a.logger.Error("store security submission", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not store security submission")
		}
		return
	}
	if changed {
		if err := a.publishAgentDetail(r.Context(), agentID); err != nil {
			a.logger.Warn("publish security Web event", "agent_id", agentID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, protocol.SecurityResponse{Accepted: true})
}
