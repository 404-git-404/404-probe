package server

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

func (a *App) maintainTCPMini() {
	ctx, cancel := context.WithTimeout(a.shutdown, 10*time.Second)
	defer cancel()
	if err := a.store.MaintainTCPMini(ctx, a.now()); err != nil && a.shutdown.Err() == nil {
		a.logger.Error("TCP Mini startup recovery", "error", err)
	}
}

func (a *App) handleTCPMini(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !qualityWebPath(r, "v3/tcp-mini") || len(q) != 1 || len(q["timezone"]) != 1 {
		writeJobError(w, 400, "invalid_request", "one IANA timezone required")
		return
	}
	r, cancel := qualityHTTPBudget(w, r)
	defer cancel()
	result, err := a.store.TCPMini(r.Context(), r.PathValue("agent_id"), q.Get("timezone"), a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
