package server

import "net/http"

func (a *App) handleStartupDiskFailure(w http.ResponseWriter, r *http.Request) {
	received := a.now()
	agentID, ok := a.authenticateJobRequest(w, r, "authenticate startup disk failure")
	if !ok {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	created, err := a.store.RecordStartupDiskFailure(r.Context(), agentID, token, received)
	if err != nil {
		a.logger.Error("store startup disk failure", "agent_id", agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not store startup disk failure")
		return
	}
	if created {
		a.publishDashboardChange(agentID, "startup-disk-failure", false)
	}
	w.WriteHeader(http.StatusNoContent)
}
