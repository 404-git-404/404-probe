package server

import (
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

func readWebAgentControl(w http.ResponseWriter, r *http.Request, id, path string, request any) bool {
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+path || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent control request")
		return false
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return false
	}
	body, err := readBoundedBody(w, r, 256)
	if err != nil {
		writeBodyError(w, err)
		return false
	}
	if decodeStrictJSON(body, request) != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent control request")
		return false
	}
	return true
}

func (a *App) handleSetWebAgentCheck(w http.ResponseWriter, r *http.Request) {
	id, check := r.PathValue("agent_id"), storage.AgentCheck(r.PathValue("check"))
	if check != storage.CountryLookupCheck && check != storage.YouTubeCheck {
		writeJobError(w, http.StatusNotFound, "unknown_check", "unknown Agent check")
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if !readWebAgentControl(w, r, id, "/checks/"+string(check), &request) {
		return
	}
	if request.Enabled == nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "enabled must be a boolean")
		return
	}
	if err := a.store.SetAgentCheckEnabled(r.Context(), id, check, *request.Enabled); err != nil {
		a.writeAgentControlError(w, id, err)
		return
	}
	a.publishDashboardChange(id, string(check)+"-check", false)
	writeJSON(w, http.StatusOK, request)
}

func (a *App) handleAcknowledgeWebDiskFault(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	var request struct {
		FaultAt int64 `json:"fault_at"`
	}
	if !readWebAgentControl(w, r, id, "/disk-fault-acknowledgement", &request) {
		return
	}
	if request.FaultAt <= 0 {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "fault_at must be a positive timestamp")
		return
	}
	if err := a.store.AcknowledgeDiskFault(r.Context(), id, request.FaultAt, a.now(), a.offlineTimeout); err != nil {
		a.writeAgentControlError(w, id, err)
		return
	}
	a.publishDashboardChange(id, "disk-fault-acknowledgement", false)
	writeJSON(w, http.StatusOK, struct {
		AcknowledgedAt int64 `json:"disk_fault_acknowledged_at"`
	}{request.FaultAt})
}

func (a *App) writeAgentControlError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, storage.ErrAgentNotFound), errors.Is(err, storage.ErrAgentRevoked):
		writeJobError(w, http.StatusNotFound, "agent_not_found", "active Agent not found")
	case errors.Is(err, storage.ErrDiskFaultChanged), errors.Is(err, storage.ErrAgentDisabled):
		writeJobError(w, http.StatusConflict, "disk_fault_changed", "磁盘状态已更新")
	default:
		a.logger.Error("update Agent control", "agent_id", id, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not update Agent control")
	}
}
