package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"404-probe/internal/storage"
)

const resourceHistoryResponseLimit = 1 << 20

type webResourceHistoryView struct {
	AgentID         string                     `json:"agent_id"`
	ServerNow       int64                      `json:"server_now"`
	From            int64                      `json:"from"`
	To              int64                      `json:"to"`
	BucketFrom      int64                      `json:"bucket_from"`
	BucketTo        int64                      `json:"bucket_to"`
	IntervalMS      int64                      `json:"interval_ms"`
	Source          string                     `json:"source"`
	Aggregation     string                     `json:"aggregation"`
	RetentionDays   int                        `json:"retention_days"`
	Coverage        string                     `json:"coverage"`
	PointLimit      int                        `json:"point_limit"`
	OldestTimestamp *int64                     `json:"oldest_timestamp"`
	LastTimestamp   *int64                     `json:"last_timestamp"`
	Points          []webAgentHistoryPointView `json:"points"`
}

func validResourceHistoryPath(r *http.Request) bool {
	id := r.PathValue("agent_id")
	return validWebAgentID(id) && r.URL.EscapedPath() == webAgentPathPrefix+id+"/resources/history"
}
func (a *App) handleResourceHistoryMethod(w http.ResponseWriter, r *http.Request) {
	if !validResourceHistoryPath(r) {
		writeJobError(w, 400, "invalid_request", "invalid Agent path")
		return
	}
	w.Header().Set("Allow", "GET")
	writeJobError(w, 405, "method_not_allowed", "method must be GET")
}

func canonicalResourceMillis(value string) (int64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errInvalidControlQuery
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, errInvalidControlQuery
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, errInvalidControlQuery
	}
	return n, nil
}
func resourceHistoryRange(r *http.Request, now int64) (int64, int64, error) {
	values, err := parseControlQuery(r, "hours", "from", "to")
	if err != nil || (r.URL.ForceQuery && r.URL.RawQuery == "") {
		return 0, 0, errInvalidControlQuery
	}
	if values.Has("from") || values.Has("to") {
		if values.Has("hours") || !values.Has("from") || !values.Has("to") {
			return 0, 0, errInvalidControlQuery
		}
		from, e1 := canonicalResourceMillis(values.Get("from"))
		to, e2 := canonicalResourceMillis(values.Get("to"))
		if e1 != nil || e2 != nil || to <= from || to-from > storage.ResourceHistoryMaxSpanMS || from < now-30*24*60*60*1000 || to > now {
			return 0, 0, errInvalidControlQuery
		}
		return from, to, nil
	}
	hours := 1
	if values.Has("hours") {
		hours, err = strconv.Atoi(values.Get("hours"))
		if err != nil || hours < 1 || hours > 24 {
			return 0, 0, errInvalidControlQuery
		}
	}
	from := now - int64(hours)*60*60*1000
	if from < 0 {
		return 0, 0, errInvalidControlQuery
	}
	return from, now, nil
}

func (a *App) handleResourceHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.handleResourceHistoryMethod(w, r)
		return
	}
	if !validResourceHistoryPath(r) || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeJobError(w, 400, "invalid_request", "resource history accepts exact GET path and no body")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	now := a.now() // One request time basis for the gate, normalized SQL and DTO.
	from, to, err := resourceHistoryRange(r, now.UnixMilli())
	if err != nil {
		if r.Context().Err() == nil {
			writeControlCollectionError(w, err)
		}
		return
	}
	id := r.PathValue("agent_id")
	// Existing-agent gate and history are DB reads outside App/cache locks.
	if _, err = a.store.GetAgentSnapshot(r.Context(), id, now, a.offlineTimeout); err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, 404, "agent_not_found", "Agent not found")
			return
		}
		writeJobError(w, 500, "internal_error", "could not read Agent")
		return
	}
	points, err := a.store.ResourceHistory(r.Context(), id, time.UnixMilli(from), time.UnixMilli(to))
	if err != nil {
		if r.Context().Err() == nil {
			writeJobError(w, 500, "internal_error", "could not read resource history")
		}
		return
	}
	v := webResourceHistoryView{AgentID: id, ServerNow: now.UnixMilli(), From: from, To: to,
		BucketFrom: from / storage.ResourceHistoryIntervalMS * storage.ResourceHistoryIntervalMS, BucketTo: to / storage.ResourceHistoryIntervalMS * storage.ResourceHistoryIntervalMS,
		IntervalMS: storage.ResourceHistoryIntervalMS, Source: "minute_metrics", Aggregation: "arithmetic_mean", RetentionDays: 30, Coverage: "partial", PointLimit: storage.ResourceHistoryPointLimit,
		Points: make([]webAgentHistoryPointView, 0, len(points))}
	for _, point := range points {
		v.Points = append(v.Points, newWebAgentHistoryPointView(point))
	}
	if len(v.Points) > 0 {
		first, last := v.Points[0].Timestamp, v.Points[len(v.Points)-1].Timestamp
		v.OldestTimestamp = &first
		v.LastTimestamp = &last
	}
	writeResourceHistoryJSON(w, r, v)
}

func writeResourceHistoryJSON(w http.ResponseWriter, r *http.Request, v webResourceHistoryView) {
	if r.Context().Err() != nil {
		return
	}
	data, err := json.Marshal(v)
	if r.Context().Err() != nil {
		return
	}
	if err != nil || len(data) > resourceHistoryResponseLimit {
		writeJobError(w, 500, "response_too_large", "resource history response exceeds bounded serialization")
		return
	}
	setWebNoStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
