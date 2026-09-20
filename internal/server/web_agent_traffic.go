package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"404-probe/internal/storage"
)

const maxWebTrafficResetBytes = 1 << 10

type webTrafficResetRequest struct {
	RequestID string `json:"request_id"`
}

func (a *App) handleResetWebAgentTraffic(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentTrafficPath(r, agentID) || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid traffic reset request")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebTrafficResetBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request webTrafficResetRequest
	if decoder.Decode(&request) != nil || !validLowerHexID(request.RequestID, 32) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid traffic reset request")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid traffic reset request")
		return
	}
	totals, err := a.store.ResetTrafficTotals(r.Context(), agentID, request.RequestID, a.now(), a.offlineTimeout)
	if err != nil {
		if errors.Is(err, storage.ErrTrafficSampleUnavailable) {
			writeJobError(w, http.StatusConflict, "fresh_sample_required", "agent must be online with a fresh traffic sample")
			return
		}
		a.logger.Error("reset traffic totals", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not reset traffic totals")
		return
	}
	writeJSON(w, http.StatusOK, totals)
}

func validWebAgentTrafficPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/traffic/reset"
}

func (a *App) handleWebAgentTrafficMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebAgentTrafficPath(r, r.PathValue("agent_id")) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}
