package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"404-probe/internal/protocol"
)

const controlLongPollDuration = 27 * time.Second

func (a *App) handleClaimControl(w http.ResponseWriter, r *http.Request) {
	agentID, ok := a.authenticateJobRequest(w, r, "claim interactive control")
	if !ok {
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxClaimBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request protocol.ControlClaimRequest
	if err := decoder.Decode(&request); err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid control claim request")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || request.Validate() != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid control claim request")
		return
	}
	claim := func() (*protocol.Job, error) {
		return a.store.ClaimJob(r.Context(), agentID, protocol.ClaimRequest{
			ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: request.AgentEpoch,
			SessionID: request.SessionID, SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeSelectorSwitch},
		}, a.now(), jobLeaseDuration)
	}
	if job, err := claim(); err != nil {
		a.writeClaimError(w, agentID, err)
		return
	} else if job != nil {
		writeJSON(w, http.StatusOK, job)
		return
	}
	wake := a.registerControlWaiter(agentID)
	defer a.unregisterControlWaiter(agentID, wake)
	// Recheck after registration so creation between the first claim and waiter
	// registration cannot be missed.
	if job, err := claim(); err != nil {
		a.writeClaimError(w, agentID, err)
		return
	} else if job != nil {
		writeJSON(w, http.StatusOK, job)
		return
	}
	timer := time.NewTimer(a.controlPollDuration)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-a.shutdown.Done():
		return
	case <-timer.C:
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusNoContent)
		return
	case <-wake:
	}
	job, err := claim()
	if err != nil {
		a.writeClaimError(w, agentID, err)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (a *App) registerControlWaiter(agentID string) chan struct{} {
	ch := make(chan struct{}, 1)
	a.controlMu.Lock()
	if a.controlWaiters[agentID] == nil {
		a.controlWaiters[agentID] = make(map[chan struct{}]struct{})
	}
	a.controlWaiters[agentID][ch] = struct{}{}
	a.controlMu.Unlock()
	return ch
}

func (a *App) unregisterControlWaiter(agentID string, ch chan struct{}) {
	a.controlMu.Lock()
	delete(a.controlWaiters[agentID], ch)
	if len(a.controlWaiters[agentID]) == 0 {
		delete(a.controlWaiters, agentID)
	}
	a.controlMu.Unlock()
}

func (a *App) notifyControl(agentID string) {
	a.controlMu.Lock()
	for ch := range a.controlWaiters[agentID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	a.controlMu.Unlock()
}
