package server

import (
	"net/http"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const controlScheduleCursorResource = "schedules"

type controlScheduleCollectionView struct {
	Items      []controlScheduleView `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}

func (a *App) handleGetControlSchedules(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	values, err := parseControlQuery(r, "agent_id", "enabled", "probe_type", "limit", "cursor")
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	agentID := values.Get("agent_id")
	if agentID != "" && !validControlAgentID(agentID) {
		writeControlCollectionError(w, errInvalidControlQuery)
		return
	}
	var enabled *bool
	if value := values.Get("enabled"); value != "" {
		parsed := false
		switch value {
		case "true":
			parsed = true
		case "false":
		default:
			writeControlCollectionError(w, errInvalidControlQuery)
			return
		}
		enabled = &parsed
	}
	probeType := protocol.ProbeType(values.Get("probe_type"))
	if probeType != "" {
		if err := probeType.Validate(); err != nil {
			writeControlCollectionError(w, errInvalidControlQuery)
			return
		}
	}
	filters := controlFilterFingerprint(values, "agent_id", "enabled", "probe_type")
	limit, after, err := parseControlCollectionPage(values, controlScheduleCursorResource, filters, validControlJobID)
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	records, next, err := a.store.QueryProbeSchedules(r.Context(), storage.ProbeScheduleQuery{
		AgentID: agentID, Enabled: enabled, ProbeType: probeType, After: after, Limit: limit,
	})
	if err != nil {
		a.logger.Error("query control schedules", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query schedules")
		return
	}
	items := make([]controlScheduleView, 0, len(records))
	for _, record := range records {
		view, err := newControlScheduleView(record)
		if err != nil {
			a.logger.Error("render listed control schedule", "schedule_id", record.ID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query schedules")
			return
		}
		items = append(items, view)
	}
	nextCursor, err := encodeControlCursor(controlScheduleCursorResource, filters, next)
	if err != nil {
		a.logger.Error("encode control schedules cursor", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query schedules")
		return
	}
	writeJSON(w, http.StatusOK, controlScheduleCollectionView{Items: items, NextCursor: nextCursor})
}

func (a *App) handleControlScheduleCollectionMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}
