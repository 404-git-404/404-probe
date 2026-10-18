package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

// The switch and the "check now" button follow the Google Status manual-check endpoint: administrator
// session with CSRF protection (requireWebMutation), JSON only, a tiny bounded body and no query.

type webPublicEgressCheckView struct {
	Enabled       bool   `json:"enabled"`
	Supported     bool   `json:"supported"`
	Pending       bool   `json:"pending"`
	ServerNow     int64  `json:"server_now"`
	LastManualAt  *int64 `json:"last_manual_at,omitempty"`
	NextCheckAt   *int64 `json:"next_check_at,omitempty"`
	CooldownUntil *int64 `json:"cooldown_until,omitempty"`
}

func newWebPublicEgressCheckView(c storage.PublicEgressControl, now int64) webPublicEgressCheckView {
	view := webPublicEgressCheckView{Enabled: c.Enabled, Supported: c.Supported, Pending: c.Pending, ServerNow: now}
	if c.LastManualAt > 0 {
		last := c.LastManualAt
		view.LastManualAt = &last
	}
	if c.Enabled && c.NextCheckAt > 0 {
		next := c.NextCheckAt
		view.NextCheckAt = &next
	}
	if c.CooldownUntil > now {
		until := c.CooldownUntil
		view.CooldownUntil = &until
	}
	return view
}

func validWebPublicEgressPath(r *http.Request, agentID, suffix string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+suffix
}

func (a *App) handleSetWebPublicEgress(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebPublicEgressPath(r, agentID, "/public-egress") {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	if r.URL.RawQuery != "" || !hasJSONContentType(r) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid public egress request")
		return
	}
	body, err := readBoundedBody(w, r, 64)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid public egress request")
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Enabled == nil || decoder.More() {
		writeJobError(w, http.StatusBadRequest, "invalid_request", `request body must be {"enabled":true|false}`)
		return
	}
	created, err := a.store.SetPublicEgressEnabled(r.Context(), agentID, *request.Enabled, a.now(), a.offlineTimeout)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrAgentNotFound):
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		case errors.Is(err, storage.ErrAgentRevoked):
			writeJobError(w, http.StatusConflict, "agent_revoked", "revoked Agent cannot be configured")
		default:
			a.logger.Error("set Web public egress", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not change public egress detection")
		}
		return
	}
	_ = a.publishAgentDetail(r.Context(), agentID)
	control, err := a.store.GetPublicEgressControl(r.Context(), agentID, a.now())
	if err != nil {
		a.logger.Error("read Web public egress", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read public egress detection")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		webPublicEgressCheckView
		Created bool `json:"created"`
	}{newWebPublicEgressCheckView(control, a.now().UnixMilli()), created})
}

func (a *App) handleCreateWebPublicEgressCheck(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebPublicEgressPath(r, agentID, "/public-egress/check") {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	if r.URL.RawQuery != "" || !hasJSONContentType(r) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid public egress request")
		return
	}
	body, err := readBoundedBody(w, r, 64)
	if err != nil || string(body) != "{}" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "request body must be an empty object")
		return
	}
	err = a.store.CreateManualPublicEgressJob(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		var cooldown *storage.PublicEgressCooldownError
		switch {
		case errors.As(err, &cooldown):
			writeJSON(w, http.StatusTooManyRequests, struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				NextAllowedAt int64 `json:"next_allowed_at"`
			}{Error: struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}{"cooldown", "manual public egress checks are limited to one per 24 hours"}, NextAllowedAt: cooldown.Until})
		case errors.Is(err, storage.ErrAgentNotFound):
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		case errors.Is(err, storage.ErrAgentRevoked):
			writeJobError(w, http.StatusConflict, "agent_revoked", "revoked Agent cannot run a public egress check")
		case errors.Is(err, storage.ErrAgentDisabled):
			writeJobError(w, http.StatusConflict, "agent_disabled", "paused Agent cannot run a public egress check")
		case errors.Is(err, storage.ErrAgentOffline):
			writeJobError(w, http.StatusConflict, "agent_offline", "offline Agent cannot run a public egress check")
		case errors.Is(err, storage.ErrPublicEgressDisabled):
			writeJobError(w, http.StatusConflict, "public_egress_disabled", "public egress detection is switched off")
		case errors.Is(err, storage.ErrPublicEgressUnsupported):
			writeJobError(w, http.StatusConflict, "public_egress_unsupported", "Agent does not support public egress checks")
		case errors.Is(err, storage.ErrPublicEgressPending):
			writeJobError(w, http.StatusConflict, "public_egress_pending", "a public egress check is already pending")
		case errors.Is(err, storage.ErrOutstandingJobsFull):
			writeJobError(w, http.StatusConflict, "queue_full", "Agent job queue is full")
		default:
			a.logger.Error("create Web public egress check", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create public egress check")
		}
		return
	}
	_ = a.publishAgentDetail(r.Context(), agentID)
	writeJSON(w, http.StatusAccepted, struct {
		Pending bool `json:"pending"`
	}{true})
}

func (a *App) handleWebPublicEgressMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentID(agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	suffix := "/public-egress"
	if r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/public-egress/check" {
		suffix = "/public-egress/check"
	}
	if !validWebPublicEgressPath(r, agentID, suffix) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}
