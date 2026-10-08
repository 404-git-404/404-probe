package server

import (
	"errors"
	"net/http"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const countryCodeLookupAgentPathPrefix = "/api/v1/agent/country-code-lookups/"

func (a *App) handleClaimAgentCountryCodeLookup(w http.ResponseWriter, r *http.Request) {
	setAgentCountryLookupNoStore(w)
	agentID, ok := a.authenticateJobRequest(w, r, "claim Agent country-code lookup")
	if !ok {
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
	body, err := readBoundedBody(w, r, maxAgentRemovalRequestBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request protocol.AgentCountryCodeLookupClaimRequest
	if err := decodeAgentRemovalJSON(body, &request); err != nil || request.Validate() != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid country-code lookup claim request")
		return
	}
	delivery, err := a.store.ClaimAgentCountryCodeLookup(r.Context(), agentID, request.AgentEpoch, request.SessionID, a.now())
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
			writeJobError(w, http.StatusUnauthorized, "invalid_agent", "Agent is not authorized for country-code lookup")
			return
		}
		a.logger.Error("claim Agent country-code lookup", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not claim country-code lookup")
		return
	}
	if delivery == nil {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	response := protocol.AgentCountryCodeLookupDelivery{ProtocolVersion: protocol.CountryCodeLookupProtocolVersion, OperationID: delivery.OperationID}
	if err := response.Validate(); err != nil {
		a.logger.Error("invalid stored country-code lookup delivery", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not claim country-code lookup")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleAgentCountryCodeLookupResult(w http.ResponseWriter, r *http.Request) {
	setAgentCountryLookupNoStore(w)
	agentID, ok := a.authenticateJobRequest(w, r, "submit Agent country-code lookup result")
	if !ok {
		return
	}
	operationID := r.PathValue("operation_id")
	if !validAgentCountryCodeLookupPath(r, operationID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid country-code lookup operation ID")
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid country-code lookup result query")
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
	var request protocol.AgentCountryCodeLookupResultRequest
	if err := decodeAgentRemovalJSON(body, &request); err != nil || request.Validate() != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid country-code lookup result")
		return
	}
	ack, err := a.store.CompleteAgentCountryCodeLookup(r.Context(), agentID, operationID, request.AgentEpoch, request.SessionID, request.CountryCode, request.ErrorCode, a.now())
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentRevoked):
			writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
		case errors.Is(err, storage.ErrAgentDisabled):
			writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
		case errors.Is(err, storage.ErrAgentCountryLookupNotFound):
			writeJobError(w, http.StatusNotFound, "lookup_not_found", "country-code lookup operation not found")
		case errors.Is(err, storage.ErrAgentCountryLookupTransition):
			writeJobError(w, http.StatusConflict, "invalid_transition", "country-code lookup result is not allowed")
		case errors.Is(err, storage.ErrUnauthorized):
			writeJobError(w, http.StatusConflict, "stale_session", "country-code lookup belongs to a different Agent session")
		default:
			a.logger.Error("submit Agent country-code lookup result", "agent_id", agentID, "operation_id", operationID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not save country-code lookup result")
		}
		return
	}
	writeJSON(w, http.StatusOK, protocol.AgentCountryCodeLookupResultResponse{Accepted: ack.Accepted, Duplicate: ack.Duplicate, Status: string(ack.Status)})
}

func validAgentCountryCodeLookupPath(r *http.Request, operationID string) bool {
	return validLowerHexID(operationID, 32) && r.URL.EscapedPath() == countryCodeLookupAgentPathPrefix+operationID+"/result"
}

func setAgentCountryLookupNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}
