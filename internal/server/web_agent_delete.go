package server

import (
	"context"
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

func (a *App) recordPausedAgentContact(ctx context.Context, token string) {
	if err := a.store.RecordPausedAgentContact(ctx, token, a.now()); err != nil {
		a.logger.Warn("record paused Agent contact", "error", err)
	}
}

func (a *App) handleDeleteWebAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("agent_id")
	if !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/delete" || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid delete agent request")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, 2<<10)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	var request struct {
		Name string `json:"name"`
	}
	if err := decodeStrictJSON(body, &request); err != nil || request.Name == "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid delete confirmation")
		return
	}
	err = a.deleteOfflineAgent(r.Context(), id, request.Name)
	switch {
	case errors.Is(err, storage.ErrAgentNotFound):
		writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
	case errors.Is(err, storage.ErrAgentServerLocal):
		writeJobError(w, http.StatusConflict, "agent_server_local", "server-local agent cannot be permanently deleted")
	case errors.Is(err, storage.ErrAgentOnline):
		writeJobError(w, http.StatusConflict, "agent_online", "agent is online")
	case errors.Is(err, storage.ErrAgentRevoked):
		writeJobError(w, http.StatusConflict, "agent_revoked", "revoked agents cannot be deleted")
	case errors.Is(err, storage.ErrAgentNameMismatch):
		writeJobError(w, http.StatusConflict, "agent_name_mismatch", "agent name does not match")
	case err != nil:
		a.logger.Error("permanently delete Agent", "agent_id", id, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not delete Agent")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) deleteOfflineAgent(ctx context.Context, id, confirmedName string) error {
	a.reportLifecycleMu.Lock()
	defer a.reportLifecycleMu.Unlock()
	now := a.now()
	if err := a.store.DeleteOfflineAgent(ctx, id, confirmedName, now, a.offlineTimeout); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.states, id)
	a.mu.Unlock()
	a.traffic.retire(id, now, true)
	a.removeDeviceScore(id)
	a.notifyControl(id)
	a.publishDashboardChange(id, "delete", false)
	return nil
}
