package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

const maxWebAgentRevokeBodyBytes = 64

type webAgentRevokeRequest struct{}

type webAgentRevokeView struct {
	Agent webAgentSummaryView `json:"agent"`
}

func (a *App) handleRevokeWebAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentRevokeRequestPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid revoke agent query")
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
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid revoke request")
		return
	}

	record, err := a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
			return
		}
		a.logger.Error("read Web Agent for revoke", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not revoke Agent")
		return
	}
	if !record.Agent.Revoked {
		changed, err := a.store.RevokeAgent(r.Context(), agentID, a.now())
		if err != nil {
			a.logger.Error("revoke Web Agent", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not revoke Agent")
			return
		}
		if !changed {
			record, err = a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
			if err != nil || !record.Agent.Revoked {
				a.logger.Error("verify Web Agent revoke", "agent_id", agentID, "error", err)
				writeJobError(w, http.StatusInternalServerError, "internal_error", "could not revoke Agent")
				return
			}
		}
	}
	record, err = a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		a.logger.Error("read revoked Web Agent", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not revoke Agent")
		return
	}
	writeJSON(w, http.StatusOK, webAgentRevokeView{Agent: newWebAgentSummaryView(record)})
}

func decodeWebAgentRevokeRequest(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return errors.New("revoke request must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request webAgentRevokeRequest
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func validWebAgentRevokeRequestPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/revoke"
}

func (a *App) handleWebAgentRevokeMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebAgentRevokeRequestPath(r, r.PathValue("agent_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}
