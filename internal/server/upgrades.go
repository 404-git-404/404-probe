package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"404-probe/internal/auth"
	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

type webUpgradeView struct {
	OperationID    string                `json:"operation_id"`
	FromVersion    string                `json:"from_version"`
	TargetVersion  string                `json:"target_version"`
	Status         storage.UpgradeStatus `json:"status"`
	FailureCode    string                `json:"failure_code,omitempty"`
	FailureMessage string                `json:"failure_message,omitempty"`
	CreatedAt      int64                 `json:"created_at"`
	StartedAt      *int64                `json:"started_at,omitempty"`
	FinishedAt     *int64                `json:"finished_at,omitempty"`
	UpdatedAt      int64                 `json:"updated_at"`
}

func newWebUpgradeView(operation storage.UpgradeOperation) webUpgradeView {
	return webUpgradeView{OperationID: operation.OperationID, FromVersion: operation.FromVersion, TargetVersion: operation.TargetVersion,
		Status: operation.Status, FailureCode: operation.FailureCode, FailureMessage: operation.FailureMessage,
		CreatedAt: operation.CreatedAt, StartedAt: operation.StartedAt, FinishedAt: operation.FinishedAt, UpdatedAt: operation.UpdatedAt}
}

func (a *App) handleCreateWebUpgrade(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebUpgradePath(r, agentID) || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid upgrade request")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, 1024)
	if err != nil || !strictEmptyObject(body) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "upgrade request must be an empty JSON object")
		return
	}
	record, err := a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		} else {
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not inspect Agent")
		}
		return
	}
	if record.Agent.Revoked {
		writeJobError(w, http.StatusConflict, "agent_revoked", "revoked Agent cannot be upgraded")
		return
	}
	if record.Agent.DisabledAt != nil {
		writeJobError(w, http.StatusConflict, "agent_paused", "resume the Agent before upgrading")
		return
	}
	if !record.Online {
		writeJobError(w, http.StatusConflict, "agent_offline", "offline Agent cannot be upgraded")
		return
	}
	if record.State == nil || !record.State.AgentUpgradeCapable {
		writeJobError(w, http.StatusConflict, "bootstrap_required", "local v0.8 bootstrap is required before remote upgrades")
		return
	}
	if !a.buildInfo.UpgradeEligible() {
		writeJobError(w, http.StatusConflict, "server_build_ineligible", "remote upgrade is unavailable on this Server build")
		return
	}
	if comparison, ok := buildinfo.CompareVersions(record.State.AgentVersion, a.buildInfo.Version); !ok || comparison >= 0 {
		writeJobError(w, http.StatusConflict, "upgrade_not_available", "a newer eligible Agent version is not available")
		return
	}
	operationID, err := auth.NewID()
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create upgrade")
		return
	}
	created := a.now().UnixMilli()
	operation, err := a.store.CreateUpgrade(r.Context(), storage.UpgradeOperation{OperationID: operationID, AgentID: agentID,
		FromVersion: record.State.AgentVersion, TargetVersion: a.buildInfo.Version, CreatedAt: created})
	if err != nil {
		if errors.Is(err, storage.ErrUpgradeConflict) {
			writeJobError(w, http.StatusConflict, "operation_conflict", "Agent already has an active upgrade")
		} else {
			a.logger.Error("create Agent upgrade", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create upgrade")
		}
		return
	}
	writeJSON(w, http.StatusCreated, newWebUpgradeView(operation))
}

func (a *App) handleGetWebUpgrade(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebUpgradePath(r, agentID) || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid upgrade request")
		return
	}
	operation, err := a.store.GetLatestUpgrade(r.Context(), agentID)
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read upgrade")
		return
	}
	if operation == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, newWebUpgradeView(*operation))
}

func (a *App) handleClaimUpgrade(w http.ResponseWriter, r *http.Request) {
	agentID, ok := a.authenticateJobRequest(w, r, "claim upgrade")
	if !ok {
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, protocol.MaxUpgradeBodyBytes)
	var request protocol.UpgradeClaimRequest
	if err != nil || decodeStrictJSON(body, &request) != nil || request.Validate() != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid upgrade claim")
		return
	}
	operation, err := a.store.ClaimUpgrade(r.Context(), agentID, a.now())
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not claim upgrade")
		return
	}
	if operation == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, protocol.UpgradeOperation{ProtocolVersion: protocol.UpgradeProtocolVersion, OperationID: operation.OperationID,
		FromVersion: operation.FromVersion, TargetVersion: operation.TargetVersion, Status: string(operation.Status)})
}

func (a *App) handleUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	agentID, ok := a.authenticateJobRequest(w, r, "update upgrade status")
	if !ok {
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	operationID := r.PathValue("operation_id")
	if !validLowerHexID(operationID, 32) {
		writeJobError(w, http.StatusNotFound, "upgrade_not_found", "upgrade not found")
		return
	}
	body, err := readBoundedBody(w, r, protocol.MaxUpgradeBodyBytes)
	var request protocol.UpgradeStatusRequest
	if err != nil || decodeStrictJSON(body, &request) != nil || request.Validate() != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid upgrade status")
		return
	}
	operation, err := a.store.UpdateUpgradeStatus(r.Context(), agentID, operationID, storage.UpgradeStatus(request.Status), request.FailureCode, request.FailureMessage, a.now())
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrUpgradeNotFound):
			writeJobError(w, http.StatusNotFound, "upgrade_not_found", "upgrade not found")
		case errors.Is(err, storage.ErrUpgradeTransition):
			writeJobError(w, http.StatusConflict, "invalid_transition", "invalid upgrade status transition")
		default:
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not update upgrade")
		}
		return
	}
	writeJSON(w, http.StatusOK, newWebUpgradeView(operation))
}

func validWebUpgradePath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/upgrade"
}

func strictEmptyObject(body []byte) bool {
	var value map[string]json.RawMessage
	return decodeStrictJSON(body, &value) == nil && len(value) == 0
}

func decodeStrictJSON(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}
