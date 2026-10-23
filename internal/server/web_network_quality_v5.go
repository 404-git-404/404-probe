package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// R25 (v1.0.7): the judgement is computed by the Server; the page only draws what these two answers say.
const (
	qualityCardResponseLimit  = 64 << 10
	qualityMarksResponseLimit = 256 << 10
)

// writeQualityJSON answers with the encoded value, or 503 when it would not fit the response budget.
func writeQualityJSON(w http.ResponseWriter, value any, limit int) {
	wire, err := json.Marshal(value)
	if err != nil || len(wire)+1 > limit {
		writeJobError(w, 503, "response_budget", "quality answer exceeds its budget")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(append(wire, '\n'))
}

func qualityV5Query(w http.ResponseWriter, r *http.Request, suffix string, names ...string) (url.Values, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !qualityWebPath(r, suffix) || len(q) != len(names) {
		writeJobError(w, 400, "invalid_request", "invalid query")
		return nil, false
	}
	for _, name := range names {
		if len(q[name]) != 1 {
			writeJobError(w, 400, "invalid_request", "invalid query")
			return nil, false
		}
	}
	return q, true
}

func (a *App) handleQualityCardV5(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, ok := qualityV5Query(w, r, "v5/card", "family")
	if !ok {
		return
	}
	family := q.Get("family")
	if family != "ipv4" && family != "ipv6" {
		writeJobError(w, 400, "invalid_request", "family must be ipv4 or ipv6")
		return
	}
	summary, err := a.store.QualityCardSummary(r.Context(), r.PathValue("agent_id"), family, a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeQualityJSON(w, summary, qualityCardResponseLimit)
}

func (a *App) handleQualityMarksV5(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, ok := qualityV5Query(w, r, "v5/marks", "family", "from", "to")
	if !ok {
		return
	}
	family := q.Get("family")
	from, errFrom := strconv.ParseInt(q.Get("from"), 10, 64)
	to, errTo := strconv.ParseInt(q.Get("to"), 10, 64)
	if (family != "ipv4" && family != "ipv6") || errFrom != nil || errTo != nil {
		writeJobError(w, 400, "invalid_request", "family, from and to are required")
		return
	}
	marks, err := a.store.QualityMarks(r.Context(), r.PathValue("agent_id"), family, from, to, a.now())
	if err != nil {
		qualityHTTPError(w, err)
		return
	}
	writeQualityJSON(w, marks, qualityMarksResponseLimit)
}
