package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

const maxWebAgentRemovalBodyBytes = 512

type webAgentRemovalRequest struct {
	OperationID string `json:"operation_id"`
}

type webAgentRemovalOperationView struct {
	OperationID   string                     `json:"operation_id"`
	Status        storage.AgentRemovalStatus `json:"status"`
	CreatedAt     int64                      `json:"created_at"`
	UpdatedAt     int64                      `json:"updated_at"`
	DeliveryCount int64                      `json:"delivery_count"`
}

func (a *App) handleCreateWebAgentRemoval(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentRemovalPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent ID")
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid Agent removal query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebAgentRemovalBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request webAgentRemovalRequest
	if err := decodeWebAgentRemovalRequest(body, &request); err != nil || !validLowerHexID(request.OperationID, 32) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent removal request")
		return
	}
	operation, created, err := a.store.CreateAgentRemovalOperation(r.Context(), agentID, request.OperationID, a.now())
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentNotFound):
			writeJobError(w, http.StatusNotFound, "agent_not_found", "Agent not found")
		case errors.Is(err, storage.ErrAgentRemovalUnsupported):
			writeJobError(w, http.StatusUpgradeRequired, "agent_upgrade_required", "upgrade Agent to enable remote removal")
		case errors.Is(err, storage.ErrAgentRemovalUpgradeConflict):
			writeJobError(w, http.StatusConflict, "upgrade_in_progress", "finish or cancel the active upgrade before remote removal")
		case errors.Is(err, storage.ErrAgentRemovalPending):
			writeJobError(w, http.StatusConflict, "removal_pending", "a different Agent removal operation is already pending")
		case errors.Is(err, storage.ErrJobIDConflict):
			writeJobError(w, http.StatusConflict, "idempotency_conflict", "operation_id is already used by another request")
		case errors.Is(err, storage.ErrAgentRemovalCompleted):
			writeJobError(w, http.StatusConflict, "removal_completed", "Agent removal operation has already completed")
		default:
			a.logger.Error("create Web Agent removal", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not request Agent removal")
		}
		return
	}
	a.traffic.retire(agentID, a.now(), true)
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, newWebAgentRemovalOperationView(operation))
}

func (a *App) handleWebAgentRemovalMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentRemovalPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}

func validWebAgentRemovalPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/remove"
}

func decodeWebAgentRemovalRequest(body []byte, request *webAgentRemovalRequest) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return errors.New("removal request must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func newWebAgentRemovalOperationView(operation storage.AgentRemovalOperation) webAgentRemovalOperationView {
	return webAgentRemovalOperationView{OperationID: operation.OperationID, Status: operation.Status,
		CreatedAt: operation.CreatedAt, UpdatedAt: operation.UpdatedAt, DeliveryCount: operation.DeliveryCount}
}
