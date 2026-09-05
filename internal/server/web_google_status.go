package server

import (
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

func (a *App) handleCreateWebGoogleStatus(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebGoogleStatusPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	if r.URL.RawQuery != "" || !hasJSONContentType(r) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Google Status request")
		return
	}
	body, err := readBoundedBody(w, r, 64)
	if err != nil || string(body) != "{}" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "request body must be an empty object")
		return
	}
	created, err := a.store.CreateManualGoogleStatusJob(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentNotFound):
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		case errors.Is(err, storage.ErrAgentRevoked):
			writeJobError(w, http.StatusConflict, "agent_revoked", "revoked Agent cannot run Google Status")
		case errors.Is(err, storage.ErrAgentDisabled):
			writeJobError(w, http.StatusConflict, "agent_disabled", "paused Agent cannot run Google Status")
		case errors.Is(err, storage.ErrAgentOffline):
			writeJobError(w, http.StatusConflict, "agent_offline", "offline Agent cannot run Google Status")
		case errors.Is(err, storage.ErrGoogleStatusUnsupported):
			writeJobError(w, http.StatusConflict, "google_status_unsupported", "Agent does not support Google Status")
		case errors.Is(err, storage.ErrGoogleStatusPending):
			writeJobError(w, http.StatusConflict, "google_status_pending", "Google Status is already pending")
		case errors.Is(err, storage.ErrOutstandingJobsFull):
			writeJobError(w, http.StatusConflict, "queue_full", "Agent job queue is full")
		default:
			a.logger.Error("create Web Google Status", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create Google Status check")
		}
		return
	}
	if created {
		_ = a.publishAgentDetail(r.Context(), agentID)
	}
	writeJSON(w, http.StatusAccepted, struct {
		Pending bool `json:"pending"`
	}{true})
}

func (a *App) handleWebGoogleStatusMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebGoogleStatusPath(r, r.PathValue("agent_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}

func validWebGoogleStatusPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/google-status"
}
