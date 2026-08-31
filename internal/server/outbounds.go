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

func (a *App) handleAgentOutbounds(w http.ResponseWriter, r *http.Request) {
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	agentID, ok := a.authenticateJobRequest(w, r, "authenticate outbound snapshot")
	if !ok {
		return
	}
	body, err := readBoundedBody(w, r, protocol.MaxOutboundSnapshotBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var snapshot protocol.OutboundSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid outbound snapshot")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid outbound snapshot")
		return
	}
	if err := snapshot.Validate(); err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := a.store.SaveOutboundSnapshot(r.Context(), agentID, snapshot, a.now()); err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentRevoked):
			writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
		case errors.Is(err, storage.ErrAgentDisabled):
			writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
		case errors.Is(err, storage.ErrUnauthorized):
			writeJobError(w, http.StatusUnauthorized, "unauthorized", "invalid agent token")
		default:
			a.logger.Error("save outbound snapshot", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not save outbound snapshot")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Accepted bool `json:"accepted"`
	}{Accepted: true})
}
