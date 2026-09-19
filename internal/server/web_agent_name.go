package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"404-probe/internal/storage"
)

const maxWebAgentRenameBytes = 1 << 10

type webAgentNameRequest struct {
	Name string `json:"name"`
}

func (a *App) handlePutWebAgentName(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentNamePath(r, agentID) || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent name request")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxWebAgentRenameBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request webAgentNameRequest
	if decoder.Decode(&request) != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent name request")
		return
	}
	var extra any
	name := strings.TrimSpace(request.Name)
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || name == "" || len(name) > 100 || strings.ContainsAny(name, "\r\n\x00") {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "name must be 1-100 characters without control line breaks")
		return
	}
	agent, err := a.store.RenameAgent(r.Context(), agentID, name, a.now())
	if err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "active agent not found")
			return
		}
		a.logger.Error("rename Web agent", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not rename agent")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func validWebAgentNamePath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/name"
}

func (a *App) handleWebAgentNameMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebAgentNamePath(r, r.PathValue("agent_id")) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Allow", http.MethodPut)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be PUT")
}
