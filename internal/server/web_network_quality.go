package server

import (
	"404-probe/internal/storage"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

func (a *App) qualityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/agent/network-quality", a.handleAgentQuality)
	mux.HandleFunc("POST /api/v1/agent/network-quality/config", a.handleAgentQualityConfig)
	path := "/api/v1/web/agents/{agent_id}/network-quality/"
	mux.Handle("GET "+path+"history", qualityBudgetHandler(a.requireWebSession(http.HandlerFunc(a.handleQualityHistory), true)))
	mux.Handle("GET "+path+"v2/history", qualityBudgetHandler(a.requireWebSession(http.HandlerFunc(a.handleQualityHistoryV2), true)))
	mux.Handle("GET "+path+"v3/history", qualityBudgetHandler(a.requireWebSession(http.HandlerFunc(a.handleQualityHistoryRollup), true)))
	mux.Handle("GET "+path+"v3/card-history", qualityBudgetHandler(a.requireWebSession(http.HandlerFunc(a.handleQualityCardHistory), true)))
	a.qualityV4Routes(mux, path)
	// Routes of the catalog era. The static files are embedded in the binary, but a page that was open
	// during a Server upgrade still calls them, so they answer 410 instead of vanishing (R07 4.1).
	outdated := http.HandlerFunc(a.handleQualityClientOutdated)
	mux.Handle("GET /api/v1/web/network-quality/catalog", qualityBudgetHandler(a.requireWebSession(outdated, true)))
	for _, suffix := range []string{"config", "v2/config"} {
		mux.Handle("GET "+path+suffix, qualityBudgetHandler(a.requireWebSession(outdated, true)))
		mux.Handle("PUT "+path+suffix, qualityBudgetHandler(a.requireWebMutation(outdated)))
	}
	mux.Handle("GET "+path+"v3/card-config", qualityBudgetHandler(a.requireWebSession(outdated, true)))
	// Schema 35 retires the TCP Mini evidence tables; R25 judges the stored minute buckets instead.
	mux.Handle("GET "+path+"v3/tcp-mini", qualityBudgetHandler(a.requireWebSession(outdated, true)))
}

func (a *App) handleQualityClientOutdated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJobError(w, http.StatusGone, "client_outdated", "页面版本已过期，请刷新页面后重试")
}
func qualityWebPath(r *http.Request, suffix string) bool {
	return validWebAgentID(r.PathValue("agent_id")) && r.URL.EscapedPath() == webAgentPathPrefix+r.PathValue("agent_id")+"/network-quality/"+suffix
}
func (a *App) handleQualityHistory(w http.ResponseWriter, r *http.Request) {
	a.qualityHistoryResponse(w, r, false)
}
func (a *App) handleQualityHistoryV2(w http.ResponseWriter, r *http.Request) {
	a.qualityHistoryResponse(w, r, true)
}
func (a *App) handleQualityHistoryRollup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, err := url.ParseQuery(r.URL.RawQuery)
	if !qualityWebPath(r, "v3/history") || err != nil || len(q) != 2 || len(q["target_id"]) != 1 || len(q["view"]) != 1 || !validLowerHexID(q.Get("target_id"), 32) {
		writeJobError(w, 400, "invalid_query", "target_id and view required")
		return
	}
	history, err := a.store.QualityHistoryRollup(r.Context(), r.PathValue("agent_id"), q.Get("target_id"), q.Get("view"), a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, history)
}
func (a *App) handleQualityCardHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, err := url.ParseQuery(r.URL.RawQuery)
	if !qualityWebPath(r, "v3/card-history") || err != nil || len(q) != 1 || len(q["target_id"]) != 1 || !validLowerHexID(q.Get("target_id"), 32) {
		writeJobError(w, 400, "invalid_query", "target_id required")
		return
	}
	history, err := a.store.QualityCardHistory(r.Context(), r.PathValue("agent_id"), q.Get("target_id"), a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, history)
}
func (a *App) qualityHistoryResponse(w http.ResponseWriter, r *http.Request, v2 bool) {
	w.Header().Set("Cache-Control", "no-store")
	suffix := "history"
	if v2 {
		suffix = "v2/history"
	}
	if !qualityWebPath(r, suffix) {
		writeJobError(w, 400, "invalid_request", "invalid quality path")
		return
	}
	q, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil || len(q) != 2 || len(q["target_id"]) != 1 || len(q["hours"]) != 1 || !validLowerHexID(q.Get("target_id"), 32) {
		writeJobError(w, 400, "invalid_query", "target_id and hours required")
		return
	}
	hours, err := strconv.Atoi(q.Get("hours"))
	if err != nil || (hours != 1 && hours != 24) {
		writeJobError(w, 400, "invalid_query", "hours must be 1 or 24")
		return
	}
	var history any
	if v2 {
		history, err = a.store.QualityHistoryV2(r.Context(), r.PathValue("agent_id"), q.Get("target_id"), hours, a.now())
	} else {
		history, err = a.store.QualityHistory(r.Context(), r.PathValue("agent_id"), q.Get("target_id"), hours, a.now())
	}
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, history)
}
func qualityHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrQualityConflict):
		writeJobError(w, 409, "revision_conflict", "configuration changed")
	case errors.Is(err, storage.ErrQualityQuota):
		writeJobError(w, 409, "quality_quota", "quality capacity reached")
	case errors.Is(err, storage.ErrQualityInvalid):
		writeJobError(w, 400, "invalid_request", "invalid quality data")
	case errors.Is(err, storage.ErrQualityUnsupported):
		writeJobError(w, 409, "unsupported", "current session unsupported")
	case errors.Is(err, storage.ErrQualityStoragePressure):
		writeJobError(w, 503, "storage_pressure", "quality store under pressure")
	case errors.Is(err, storage.ErrAgentNotFound):
		writeJobError(w, 404, "agent_not_found", "Agent not found")
	case errors.Is(err, storage.ErrUnauthorized), errors.Is(err, storage.ErrAgentRemovalPending):
		writeJobError(w, 403, "unavailable", "Agent unavailable")
	default:
		writeJobError(w, 503, "retryable", "quality store unavailable")
	}
}
