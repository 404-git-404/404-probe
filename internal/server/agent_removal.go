package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	agentRemovalPathPrefix      = "/api/v1/agent/removals/"
	maxAgentRemovalRequestBytes = 2 << 10
	maxAgentRemovalReceiptBytes = 1 << 10
)

func (a *App) handleClaimAgentRemoval(w http.ResponseWriter, r *http.Request) {
	setAgentRemovalNoStore(w)
	agentID, ok := a.authenticateJobRequest(w, r, "claim Agent removal")
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid Agent removal claim query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxAgentRemovalRequestBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request protocol.AgentRemovalClaimRequest
	if err := decodeAgentRemovalJSON(body, &request); err != nil || request.Validate() != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent removal claim request")
		return
	}
	delivery, err := a.store.ClaimAgentRemoval(r.Context(), agentID, request.AgentEpoch, request.SessionID, a.now())
	if err != nil {
		if errors.Is(err, storage.ErrAgentRevoked) {
			writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
			return
		}
		if errors.Is(err, storage.ErrAgentDisabled) {
			writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
			return
		}
		if errors.Is(err, storage.ErrUnauthorized) {
			writeJobError(w, http.StatusUnauthorized, "invalid_agent", "Agent is not authorized for removal claim")
			return
		}
		a.logger.Error("claim Agent removal", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not claim Agent removal")
		return
	}
	if delivery == nil {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	response := protocol.AgentRemovalDelivery{
		ProtocolVersion: protocol.AgentRemovalProtocolVersion,
		OperationID:     delivery.Operation.OperationID,
		Status:          string(delivery.Operation.Status),
		ReceiptToken:    delivery.ReceiptToken,
	}
	if err := response.Validate(); err != nil {
		a.logger.Error("invalid stored Agent removal delivery", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not claim Agent removal")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleAgentRemovalStatus(w http.ResponseWriter, r *http.Request) {
	setAgentRemovalNoStore(w)
	agentID, ok := a.authenticateJobRequest(w, r, "update Agent removal status")
	if !ok {
		return
	}
	operationID := r.PathValue("operation_id")
	if !validAgentRemovalActionPath(r, operationID, "/status") {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent removal operation ID")
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid Agent removal status query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxAgentRemovalRequestBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request protocol.AgentRemovalStatusRequest
	if err := decodeAgentRemovalJSON(body, &request); err != nil || request.Validate() != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent removal status request")
		return
	}
	operation, err := a.store.MarkAgentRemovalUninstalling(r.Context(), agentID, operationID, request.AgentEpoch, request.SessionID, a.now())
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentRevoked):
			writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
		case errors.Is(err, storage.ErrAgentDisabled):
			writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
		case errors.Is(err, storage.ErrAgentRemovalNotFound):
			writeJobError(w, http.StatusNotFound, "removal_not_found", "Agent removal operation not found")
		case errors.Is(err, storage.ErrAgentRemovalTransition):
			writeJobError(w, http.StatusConflict, "invalid_transition", "Agent removal state transition is not allowed")
		case errors.Is(err, storage.ErrUnauthorized):
			writeJobError(w, http.StatusConflict, "stale_session", "Agent removal claim belongs to a different session")
		default:
			a.logger.Error("update Agent removal status", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not update Agent removal status")
		}
		return
	}
	a.traffic.retire(agentID, a.now(), true)
	writeJSON(w, http.StatusOK, map[string]any{"operation_id": operation.OperationID, "status": operation.Status})
}

func (a *App) handleAgentRemovalReceipt(w http.ResponseWriter, r *http.Request) {
	setAgentRemovalNoStore(w)
	operationID := r.PathValue("operation_id")
	if !validAgentRemovalActionPath(r, operationID, "/receipt") {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid Agent removal operation ID")
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid Agent removal receipt query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeJobError(w, http.StatusUnauthorized, "invalid_receipt_credential", "invalid removal receipt credential")
		return
	}
	body, err := readBoundedBody(w, r, maxAgentRemovalReceiptBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request protocol.AgentRemovalReceiptRequest
	if err := decodeAgentRemovalJSON(body, &request); err != nil || request.Validate() != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_receipt", "invalid Agent removal receipt")
		return
	}
	ack, err := a.store.CompleteAgentRemoval(r.Context(), operationID, token, request.Receipt, a.now())
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentRemovalCredential):
			writeJobError(w, http.StatusUnauthorized, "invalid_receipt_credential", "invalid or expired removal receipt credential")
		case errors.Is(err, storage.ErrAgentRemovalTransition):
			writeJobError(w, http.StatusConflict, "invalid_transition", "Agent removal receipt arrived before uninstalling")
		default:
			a.logger.Error("verify Agent removal receipt", "operation_id", operationID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not verify Agent removal receipt")
		}
		return
	}
	writeJSON(w, http.StatusOK, protocol.AgentRemovalReceiptResponse{Completed: ack.Completed, Duplicate: ack.Duplicate})
}

func decodeAgentRemovalJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validAgentRemovalActionPath(r *http.Request, operationID, suffix string) bool {
	return validLowerHexID(operationID, 32) && r.URL.EscapedPath() == agentRemovalPathPrefix+operationID+suffix
}

func setAgentRemovalNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}
