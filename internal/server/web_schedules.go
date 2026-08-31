package server

import (
	"errors"
	"fmt"
	"net/http"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	webSchedulePathPrefix     = "/api/v1/web/schedules/"
	webScheduleCursorResource = "web_schedules"
)

type webScheduleSummaryView struct {
	ScheduleID      string             `json:"schedule_id"`
	AgentID         string             `json:"agent_id"`
	Name            string             `json:"name"`
	ProbeType       protocol.ProbeType `json:"probe_type"`
	TimeoutMS       int                `json:"timeout_ms"`
	IntervalSeconds int                `json:"interval_seconds"`
	Enabled         bool               `json:"enabled"`
	NextRunAt       int64              `json:"next_run_at"`
}

type webScheduleDetailView struct {
	webScheduleSummaryView
	Config    webScheduleConfigView `json:"config"`
	CreatedAt int64                 `json:"created_at"`
	UpdatedAt int64                 `json:"updated_at"`
}

type webScheduleConfigView struct {
	URL            string `json:"url,omitempty"`
	Method         string `json:"method,omitempty"`
	ExpectedStatus *int   `json:"expected_status,omitempty"`
	Host           string `json:"host,omitempty"`
	Port           int    `json:"port,omitempty"`
	Target         string `json:"target,omitempty"`
	Count          int    `json:"count,omitempty"`
	Selector       string `json:"selector,omitempty"`
	Choice         string `json:"choice,omitempty"`
}

type webScheduleCollectionView struct {
	Items      []webScheduleSummaryView `json:"items"`
	NextCursor *string                  `json:"next_cursor"`
}

func (a *App) handleGetWebSchedules(w http.ResponseWriter, r *http.Request) {
	values, err := parseControlQuery(r, "agent_id", "enabled", "probe_type", "limit", "cursor")
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	agentID := values.Get("agent_id")
	if agentID != "" && !validWebAgentID(agentID) {
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
	limit, after, err := parseControlCollectionPage(values, webScheduleCursorResource, filters, validWebScheduleID)
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	records, next, err := a.store.QueryProbeSchedules(r.Context(), storage.ProbeScheduleQuery{
		AgentID: agentID, Enabled: enabled, ProbeType: probeType, After: after, Limit: limit,
	})
	if err != nil {
		a.logger.Error("query Web schedules", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query schedules")
		return
	}
	items := make([]webScheduleSummaryView, 0, len(records))
	for _, record := range records {
		items = append(items, newWebScheduleSummaryView(record))
	}
	nextCursor, err := encodeControlCursor(webScheduleCursorResource, filters, next)
	if err != nil {
		a.logger.Error("encode Web schedules cursor", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query schedules")
		return
	}
	writeJSON(w, http.StatusOK, webScheduleCollectionView{Items: items, NextCursor: nextCursor})
}

func (a *App) handleGetWebSchedule(w http.ResponseWriter, r *http.Request) {
	scheduleID := r.PathValue("schedule_id")
	if !validWebScheduleRequestPath(r, scheduleID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid schedule ID")
		return
	}
	if _, err := parseControlQuery(r); err != nil {
		writeControlCollectionError(w, err)
		return
	}
	record, err := a.store.GetProbeSchedule(r.Context(), scheduleID)
	if err != nil {
		if errors.Is(err, storage.ErrScheduleNotFound) {
			writeJobError(w, http.StatusNotFound, "schedule_not_found", "schedule not found")
			return
		}
		a.logger.Error("read Web schedule", "schedule_id", scheduleID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read schedule")
		return
	}
	view, err := newWebScheduleDetailView(record)
	if err != nil {
		a.logger.Error("render Web schedule", "schedule_id", scheduleID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read schedule")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) handleWebScheduleCollectionMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func (a *App) handleWebScheduleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebScheduleRequestPath(r, r.PathValue("schedule_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid schedule ID")
		return
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func validWebScheduleRequestPath(r *http.Request, scheduleID string) bool {
	return validWebScheduleID(scheduleID) && r.URL.EscapedPath() == webSchedulePathPrefix+scheduleID
}

func validWebScheduleID(value string) bool { return validLowerHexID(value, 32) }

func newWebScheduleSummaryView(record storage.ProbeScheduleRecord) webScheduleSummaryView {
	return webScheduleSummaryView{
		ScheduleID: record.ID, AgentID: record.AgentID, Name: record.Name, ProbeType: record.ProbeType,
		TimeoutMS: record.TimeoutMS, IntervalSeconds: record.IntervalSeconds,
		Enabled: record.Enabled, NextRunAt: record.NextRunAt,
	}
}

func newWebScheduleDetailView(record storage.ProbeScheduleRecord) (webScheduleDetailView, error) {
	config, err := newWebScheduleConfigView(record.ProbeType, record.Config)
	if err != nil {
		return webScheduleDetailView{}, err
	}
	return webScheduleDetailView{
		webScheduleSummaryView: newWebScheduleSummaryView(record), Config: config,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}

func newWebScheduleConfigView(probeType protocol.ProbeType, config protocol.ProbeConfig) (webScheduleConfigView, error) {
	if err := config.Validate(probeType); err != nil {
		return webScheduleConfigView{}, fmt.Errorf("config: %w", err)
	}
	switch probeType {
	case protocol.ProbeTypeHTTP:
		view := webScheduleConfigView{URL: config.HTTP.URL, Method: config.HTTP.Method}
		if config.HTTP.ExpectedStatus != nil {
			expected := *config.HTTP.ExpectedStatus
			view.ExpectedStatus = &expected
		}
		return view, nil
	case protocol.ProbeTypeTCPConnect:
		return webScheduleConfigView{Host: config.TCPConnect.Host, Port: config.TCPConnect.Port}, nil
	case protocol.ProbeTypeICMPPing:
		return webScheduleConfigView{Target: config.ICMPPing.Target, Count: config.ICMPPing.Count}, nil
	case protocol.ProbeTypeSelectorSwitch:
		return webScheduleConfigView{Selector: config.SelectorSwitch.Selector, Choice: config.SelectorSwitch.Choice}, nil
	default:
		return webScheduleConfigView{}, fmt.Errorf("unknown probe type %q", probeType)
	}
}
