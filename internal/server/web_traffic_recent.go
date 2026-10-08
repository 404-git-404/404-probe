package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

func validTrafficRecentPath(r *http.Request) bool {
	id := r.PathValue("agent_id")
	return validWebAgentID(id) && r.URL.EscapedPath() == webAgentPathPrefix+id+"/traffic/recent"
}
func (a *App) handleTrafficRecentMethod(w http.ResponseWriter, r *http.Request) {
	if !validTrafficRecentPath(r) {
		writeJobError(w, 400, "invalid_request", "invalid Agent path")
		return
	}
	w.Header().Set("Allow", "GET")
	writeJobError(w, 405, "method_not_allowed", "method must be GET")
}
func (a *App) handleTrafficRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.handleTrafficRecentMethod(w, r)
		return
	}
	if !validTrafficRecentPath(r) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeJobError(w, 400, "invalid_request", "recent traffic accepts exact path and no query/body")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	id := r.PathValue("agent_id")
	owner := a.traffic.readOwner(id)
	now := a.now()
	// Authoritative DB gate every GET, deliberately outside App/cache mutexes.
	record, err := a.store.GetAgentSnapshot(r.Context(), id, now, a.offlineTimeout)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, storage.ErrAgentNotFound) {
			a.traffic.retireRead(id, owner, a.now())
			writeJobError(w, 404, "agent_not_found", "Agent not found")
			return
		}
		writeJobError(w, 500, "internal_error", "could not read Agent")
		return
	}
	_, removing, err := a.store.GetAgentRemovalOperation(r.Context(), id)
	if err != nil {
		if r.Context().Err() == nil {
			writeJobError(w, 500, "internal_error", "could not read Agent lifecycle")
		}
		return
	}
	status := ""
	if record.Agent.Revoked {
		status = "revoked"
	} else if removing {
		status = "removing"
	} else if record.Agent.DisabledAt != nil {
		status = "paused"
	}
	if status != "" {
		a.traffic.retireRead(id, owner, a.now())
	}
	v := a.traffic.snapshot(id, a.now())
	if status != "" {
		v.Status = status
		v.Reason = "agent_" + status
		v.Points = []trafficPointView{}
		v.OldestReceivedAt = nil
		v.LastReceivedAt = nil
		v.Truncated = false
	} else if !record.Online {
		v.Status = "offline"
		if len(v.Points) > 0 {
			v.Reason = "last_known_observations"
		}
	}
	if r.Context().Err() != nil {
		return
	}
	writeTrafficRecentJSON(w, r, v)
}
func writeTrafficRecentJSON(w http.ResponseWriter, r *http.Request, v trafficRecentView) {
	data, err := json.Marshal(v)
	if r.Context().Err() != nil {
		return
	}
	if err != nil || len(data) > trafficResponseLimit {
		writeJobError(w, 500, "response_too_large", "recent traffic response exceeds bounded serialization")
		return
	}
	setWebNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
