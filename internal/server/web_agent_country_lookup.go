package server

import (
	"errors"
	"net/http"
	"time"

	"404-probe/internal/storage"
)

const maxWebAgentCountryLookupBodyBytes = 512

type webAgentCountryCodeLookupView struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	ResultCode  string `json:"result_code,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	LastCode    string `json:"last_country_code,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func (a *App) handleCreateWebAgentCountryCodeLookup(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentCountryLookupPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent ID")
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid country-code lookup query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebAgentCountryLookupBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request struct{}
	if err := decodeAgentRemovalJSON(body, &request); err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "country-code lookup request must be an empty object")
		return
	}
	snapshot, err := a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "active Agent not found")
			return
		}
		a.logger.Error("read Agent before country-code lookup", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read Agent")
		return
	}
	if !snapshot.Online || snapshot.Agent.DisabledAt != nil || snapshot.Agent.Revoked {
		writeJobError(w, http.StatusConflict, "agent_unavailable", "Agent must be online and enabled for a one-time country-code lookup")
		return
	}
	operation, err := a.store.CreateAgentCountryCodeLookup(r.Context(), agentID, a.now())
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentCountryLookupUnsupported):
			writeJobError(w, http.StatusUpgradeRequired, "agent_upgrade_required", "upgrade Agent to enable one-time country-code lookup")
		case errors.Is(err, storage.ErrAgentCountryLookupPending):
			writeJobError(w, http.StatusConflict, "lookup_pending", "a country-code lookup is already pending; wait or start a new one after it expires")
		case errors.Is(err, storage.ErrAgentNotFound):
			writeJobError(w, http.StatusNotFound, "agent_not_found", "active Agent not found")
		default:
			a.logger.Error("create Web Agent country-code lookup", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not request country-code lookup")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, newWebAgentCountryCodeLookupView(operation, a.now()))
}

func (a *App) handleWebAgentCountryCodeLookupMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebAgentCountryLookupPath(r, r.PathValue("agent_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}

func validWebAgentCountryLookupPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/country-code/refresh"
}

func newWebAgentCountryCodeLookupView(operation storage.AgentCountryCodeLookup, now time.Time) webAgentCountryCodeLookupView {
	status := string(operation.Status)
	if (operation.Status == storage.AgentCountryCodeLookupRequested || operation.Status == storage.AgentCountryCodeLookupDelivered) &&
		now.UnixMilli()-operation.UpdatedAt >= storage.AgentCountryCodeLookupPendingTTL.Milliseconds() {
		status = "unknown"
	}
	return webAgentCountryCodeLookupView{
		OperationID: operation.OperationID, Status: status, ResultCode: operation.ResultCode,
		ErrorCode: operation.ErrorCode, LastCode: operation.LastCode, CreatedAt: operation.CreatedAt, UpdatedAt: operation.UpdatedAt,
	}
}
