package server

import (
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

func (a *App) handleDisableWebAgent(w http.ResponseWriter, r *http.Request) {
	a.handleSetWebAgentDisabled(w, r, true)
}

func (a *App) handleEnableWebAgent(w http.ResponseWriter, r *http.Request) {
	a.handleSetWebAgentDisabled(w, r, false)
}

func (a *App) handleSetWebAgentDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	agentID := r.PathValue("agent_id")
	action := "enable"
	if disabled {
		action = "disable"
	}
	if !validWebAgentStateRequestPath(r, agentID, action) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid agent state query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebAgentRevokeBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	if err := decodeWebAgentRevokeRequest(body); err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent state request")
		return
	}

	if disabled {
		_, err = a.store.DisableAgent(r.Context(), agentID, a.now())
	} else {
		_, err = a.store.EnableAgent(r.Context(), agentID, a.now())
	}
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentNotFound):
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		case errors.Is(err, storage.ErrAgentRevoked):
			writeJobError(w, http.StatusConflict, "agent_revoked", "revoked agent cannot be enabled or disabled")
		default:
			a.logger.Error(action+" Web Agent", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not change Agent state")
		}
		return
	}
	record, err := a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		a.logger.Error("read Web Agent after "+action, "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not change Agent state")
		return
	}
	writeJSON(w, http.StatusOK, webAgentRevokeView{Agent: newWebAgentSummaryView(record)})
}

func validWebAgentStateRequestPath(r *http.Request, agentID, action string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/"+action
}

func (a *App) handleWebAgentStateMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action == "" {
		path := r.URL.EscapedPath()
		if len(path) >= len("/disable") && path[len(path)-len("/disable"):] == "/disable" {
			action = "disable"
		} else {
			action = "enable"
		}
	}
	if !validWebAgentStateRequestPath(r, r.PathValue("agent_id"), action) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}
