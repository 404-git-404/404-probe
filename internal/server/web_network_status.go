package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"404-probe/internal/storage"
)

const (
	networkFleetPath = "/api/v1/web/network-quality/fleet"
	networkAgentPath = "/api/v1/web/agents/{agent_id}/"
)

func (a *App) networkStatusRoutes(mux *http.ServeMux) {
	register := func(path string, handler http.HandlerFunc) {
		bounded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			handler(w, r.WithContext(ctx))
		})
		mux.Handle("GET "+path, a.requireWebSession(bounded, true))
	}
	register(networkFleetPath, a.handleNetworkFleet)
	register(networkAgentPath+"network-quality/range", a.handleNetworkRange)
	register(networkAgentPath+"network-quality/heatmap", a.handleNetworkHeatmap)
	register(networkAgentPath+"network-quality/alerts", a.handleNetworkAlerts)
	register(networkAgentPath+"presence", a.handleAgentPresence)
}

func networkQuery(r *http.Request, required, optional string) (url.Values, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, false
	}
	requiredKeys, optionalKeys := strings.Fields(required), strings.Fields(optional)
	for _, k := range requiredKeys {
		if len(q[k]) != 1 || q.Get(k) == "" {
			return nil, false
		}
	}
	for k, vals := range q {
		if len(vals) != 1 || vals[0] == "" || (!slices.Contains(requiredKeys, k) && !slices.Contains(optionalKeys, k)) {
			return nil, false
		}
	}
	return q, true
}

func networkRange(q url.Values, now time.Time) (int64, int64, bool) {
	if window := q.Get("window"); window != "" {
		var duration time.Duration
		switch window {
		case "1h":
			duration = time.Hour
		case "24h":
			duration = 24 * time.Hour
		case "7d":
			duration = 7 * 24 * time.Hour
		case "30d":
			duration = 30 * 24 * time.Hour
		}
		return now.Add(-duration).UnixMilli(), now.UnixMilli(), duration > 0 && !q.Has("start") && !q.Has("end")
	}
	start, startErr := strconv.ParseInt(q.Get("start"), 10, 64)
	end, endErr := strconv.ParseInt(q.Get("end"), 10, 64)
	return start, end, startErr == nil && endErr == nil
}

func networkLine(q url.Values) storage.NetworkLine {
	return storage.NetworkLine{Slot: q.Get("slot"), Family: q.Get("family"), Protocol: q.Get("protocol")}
}

func networkStatusResponse(w http.ResponseWriter, result any, err error) {
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleNetworkFleet(w http.ResponseWriter, r *http.Request) {
	q, valid := networkQuery(r, "", "start end window agent_id family protocol")
	now := a.now()
	start, end, rangeOK := networkRange(q, now)
	id := q.Get("agent_id")
	if r.URL.EscapedPath() != networkFleetPath || !valid || !rangeOK || (id != "" && !validWebAgentID(id)) {
		writeJobError(w, 400, "invalid_query", "choose start and end or window")
		return
	}
	result, err := a.store.NetworkFleet(r.Context(), id, q.Get("family"), q.Get("protocol"), start, end, now, a.offlineTimeout)
	networkStatusResponse(w, result, err)
}

func (a *App) handleNetworkRange(w http.ResponseWriter, r *http.Request) {
	q, valid := networkQuery(r, "slot family protocol", "start end window")
	now := a.now()
	start, end, rangeOK := networkRange(q, now)
	if !valid || !rangeOK || !qualityWebPath(r, "range") {
		writeJobError(w, 400, "invalid_query", "slot, family, protocol and a time range required")
		return
	}
	result, err := a.store.NetworkHistory(r.Context(), r.PathValue("agent_id"), networkLine(q), start, end, now)
	networkStatusResponse(w, result, err)
}

func (a *App) handleNetworkHeatmap(w http.ResponseWriter, r *http.Request) {
	q, valid := networkQuery(r, "days slot family protocol", "")
	days, err := strconv.Atoi(q.Get("days"))
	if err != nil || !valid || !qualityWebPath(r, "heatmap") {
		writeJobError(w, 400, "invalid_query", "days, slot, family and protocol required")
		return
	}
	result, err := a.store.NetworkHeatmap(r.Context(), r.PathValue("agent_id"), networkLine(q), days, a.now())
	networkStatusResponse(w, result, err)
}

func (a *App) handleNetworkAlerts(w http.ResponseWriter, r *http.Request) {
	if !qualityWebPath(r, "alerts") || r.URL.RawQuery != "" {
		writeJobError(w, 400, "invalid_query", "invalid alert request")
		return
	}
	result, err := a.store.NetworkAlerts(r.Context(), r.PathValue("agent_id"), a.now())
	networkStatusResponse(w, result, err)
}

func (a *App) handleAgentPresence(w http.ResponseWriter, r *http.Request) {
	q, valid := networkQuery(r, "start end", "")
	now := a.now()
	start, end, rangeOK := networkRange(q, now)
	id := r.PathValue("agent_id")
	if !valid || !rangeOK || !validWebAgentID(id) || r.URL.EscapedPath() != webAgentPathPrefix+id+"/presence" {
		writeJobError(w, 400, "invalid_query", "start and end required")
		return
	}
	result, err := a.store.AgentPresenceHistory(r.Context(), id, start, end, now)
	networkStatusResponse(w, result, err)
}
