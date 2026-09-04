package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	maxWebSelectorSwitchBodyBytes = 4 << 10
	selectorSwitchTimeoutMS       = 10_000
	selectorSwitchTTL             = 2 * time.Minute
)

type webSelectorSwitchRequest struct {
	RequestID string `json:"request_id"`
	Selector  string `json:"selector"`
	Choice    string `json:"choice"`
}

func (a *App) handleWebSelectorSwitch(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebSelectorSwitchPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid selector switch query")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebSelectorSwitchBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request webSelectorSwitchRequest
	if err := decoder.Decode(&request); err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid selector switch request")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid selector switch request")
		return
	}
	config := protocol.ProbeConfig{SelectorSwitch: &protocol.SelectorSwitchConfig{Selector: request.Selector, Choice: request.Choice}}
	if !validLowerHexID(request.RequestID, 32) || config.Validate(protocol.ProbeTypeSelectorSwitch) != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid selector switch request")
		return
	}
	agent, err := a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
			return
		}
		a.logger.Error("read selector switch agent", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create selector switch")
		return
	}
	if agent.Agent.Revoked {
		writeJobError(w, http.StatusConflict, "agent_revoked", "revoked agent cannot switch selectors")
		return
	}
	if agent.Agent.DisabledAt != nil {
		writeJobError(w, http.StatusLocked, "agent_disabled", "disabled agent cannot switch selectors")
		return
	}
	if !agent.Online {
		writeJobError(w, http.StatusConflict, "agent_offline", "offline agent cannot switch selectors")
		return
	}
	snapshot, configured, err := a.store.GetOutboundSnapshot(r.Context(), agentID)
	if err != nil {
		a.logger.Error("read selector switch snapshot", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create selector switch")
		return
	}
	if !configured {
		writeJobError(w, http.StatusConflict, "outbounds_not_configured", "outbound discovery is not configured")
		return
	}
	if !snapshot.Available {
		writeJobError(w, http.StatusConflict, "outbounds_unavailable", "outbound discovery is currently unavailable")
		return
	}
	if !snapshotAllowsSwitch(snapshot, request.Selector, request.Choice) {
		writeJobError(w, http.StatusConflict, "selector_choice_not_allowed", "selector or choice is not present in the latest snapshot")
		return
	}
	now := a.now().UnixMilli()
	job, created, err := a.store.CreateOneShotJobIdempotent(r.Context(), storage.CreateOneShotJobParams{
		ID: request.RequestID, AgentID: agentID, ProbeType: protocol.ProbeTypeSelectorSwitch, Config: config,
		TimeoutMS: selectorSwitchTimeoutMS, CreatedAt: now, NotBefore: now, ExpiresAt: now + selectorSwitchTTL.Milliseconds(),
	})
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentNotFound):
			writeJobError(w, http.StatusConflict, "agent_unavailable", "agent is no longer active")
		case errors.Is(err, storage.ErrJobIDConflict):
			writeJobError(w, http.StatusConflict, "idempotency_conflict", "request_id is already used by a different operation")
		case errors.Is(err, storage.ErrOutstandingJobsFull):
			w.Header().Set("Retry-After", "10")
			writeJobError(w, http.StatusTooManyRequests, "queue_full", "agent job queue is full")
		case errors.Is(err, storage.ErrSelectorSwitchPending):
			writeJobError(w, http.StatusConflict, "selector_switch_pending", "selector already has a queued or running switch")
		default:
			a.logger.Error("create selector switch", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create selector switch")
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.notifyControl(agentID)
	}
	writeJSON(w, status, struct {
		JobID   string            `json:"job_id"`
		Status  storage.JobStatus `json:"status"`
		Created bool              `json:"created"`
	}{JobID: job.ID, Status: job.Status, Created: created})
}

func snapshotAllowsSwitch(snapshot storage.OutboundSnapshot, selectorName, choiceName string) bool {
	for _, selector := range snapshot.Selectors {
		if selector.Name != selectorName {
			continue
		}
		for _, choice := range selector.Choices {
			if choice == choiceName {
				return true
			}
		}
	}
	return false
}

func validWebSelectorSwitchPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/outbounds/switch"
}

func (a *App) handleWebSelectorSwitchMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebSelectorSwitchPath(r, r.PathValue("agent_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}
