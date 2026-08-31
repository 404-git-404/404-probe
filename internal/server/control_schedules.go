package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	controlSchedulePathPrefix = "/api/v1/control/schedules/"
	minScheduleInterval       = 30
	maxScheduleInterval       = 604800
)

type controlPutScheduleRequest struct {
	AgentID         string             `json:"agent_id"`
	Name            string             `json:"name"`
	ProbeType       protocol.ProbeType `json:"probe_type"`
	Config          json.RawMessage    `json:"config"`
	TimeoutMS       int                `json:"timeout_ms"`
	IntervalSeconds int                `json:"interval_seconds"`
	Enabled         *bool              `json:"enabled"`
}

type controlScheduleView struct {
	ScheduleID      string             `json:"schedule_id"`
	AgentID         string             `json:"agent_id"`
	Name            string             `json:"name"`
	ProbeType       protocol.ProbeType `json:"probe_type"`
	Config          json.RawMessage    `json:"config"`
	TimeoutMS       int                `json:"timeout_ms"`
	IntervalSeconds int                `json:"interval_seconds"`
	Enabled         bool               `json:"enabled"`
	NextRunAt       int64              `json:"next_run_at"`
	CreatedAt       int64              `json:"created_at"`
	UpdatedAt       int64              `json:"updated_at"`
}

func (a *App) handlePutControlSchedule(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	scheduleID := r.PathValue("schedule_id")
	if !validControlScheduleRequestPath(r, scheduleID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid schedule ID")
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxControlJobBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	request, err := decodeControlPutScheduleRequest(body)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid control schedule request")
		return
	}
	config, err := protocol.DecodeProbeConfig(request.ProbeType, request.Config)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid probe config")
		return
	}
	record, created, err := a.store.PutProbeSchedule(r.Context(), storage.PutScheduleParams{
		ID: scheduleID, AgentID: request.AgentID, Name: request.Name, ProbeType: request.ProbeType,
		Config: config, TimeoutMS: request.TimeoutMS, IntervalSeconds: request.IntervalSeconds,
		Enabled: *request.Enabled, Now: a.now().UnixMilli(),
	})
	if err != nil {
		a.writeControlSchedulePutError(w, scheduleID, err)
		return
	}
	view, err := newControlScheduleView(record)
	if err != nil {
		a.logger.Error("render control schedule", "schedule_id", scheduleID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read control schedule")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", controlSchedulePathPrefix+scheduleID)
	}
	writeJSON(w, status, view)
}

func (a *App) handleGetControlSchedule(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	scheduleID := r.PathValue("schedule_id")
	if !validControlScheduleRequestPath(r, scheduleID) {
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
		a.logger.Error("read control schedule", "schedule_id", scheduleID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read control schedule")
		return
	}
	view, err := newControlScheduleView(record)
	if err != nil {
		a.logger.Error("render control schedule", "schedule_id", scheduleID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read control schedule")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) handleDeleteControlSchedule(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	scheduleID := r.PathValue("schedule_id")
	if !validControlScheduleRequestPath(r, scheduleID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid schedule ID")
		return
	}
	if err := a.store.DeleteProbeSchedule(r.Context(), scheduleID); err != nil {
		if errors.Is(err, storage.ErrScheduleNotFound) {
			writeJobError(w, http.StatusNotFound, "schedule_not_found", "schedule not found")
			return
		}
		a.logger.Error("delete control schedule", "schedule_id", scheduleID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not delete control schedule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleControlScheduleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	if !validControlScheduleRequestPath(r, r.PathValue("schedule_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid schedule ID")
		return
	}
	w.Header().Set("Allow", "GET, PUT, DELETE")
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET, PUT, or DELETE")
}

func (a *App) writeControlSchedulePutError(w http.ResponseWriter, scheduleID string, err error) {
	switch {
	case errors.Is(err, storage.ErrAgentNotFound):
		writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
	case errors.Is(err, storage.ErrScheduleConflict):
		writeJobError(w, http.StatusConflict, "schedule_conflict", "schedule belongs to a different agent")
	case errors.Is(err, storage.ErrScheduleLimit):
		writeJobError(w, http.StatusTooManyRequests, "schedule_limit", "agent schedule limit reached")
	case errors.Is(err, storage.ErrCorruptProbeData):
		a.logger.Error("update corrupt control schedule", "schedule_id", scheduleID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not update control schedule")
	default:
		a.logger.Error("put control schedule", "schedule_id", scheduleID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not update control schedule")
	}
}

func decodeControlPutScheduleRequest(body []byte) (controlPutScheduleRequest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var request controlPutScheduleRequest
	if err := decoder.Decode(&request); err != nil {
		return controlPutScheduleRequest{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return controlPutScheduleRequest{}, err
	}
	if !validControlJobID(request.AgentID) || !validControlScheduleName(request.Name) {
		return controlPutScheduleRequest{}, errors.New("invalid schedule identity")
	}
	if err := request.ProbeType.Validate(); err != nil {
		return controlPutScheduleRequest{}, err
	}
	if !request.ProbeType.IsNetworkProbe() {
		return controlPutScheduleRequest{}, errors.New("probe type is not schedulable")
	}
	if len(request.Config) == 0 || string(request.Config) == "null" {
		return controlPutScheduleRequest{}, errors.New("config is required")
	}
	if request.TimeoutMS < protocol.MinProbeTimeoutMS || request.TimeoutMS > protocol.MaxProbeTimeoutMS {
		return controlPutScheduleRequest{}, errors.New("invalid timeout")
	}
	if request.IntervalSeconds < minScheduleInterval || request.IntervalSeconds > maxScheduleInterval {
		return controlPutScheduleRequest{}, errors.New("invalid interval")
	}
	if request.Enabled == nil {
		return controlPutScheduleRequest{}, errors.New("enabled is required")
	}
	return request, nil
}

func validControlScheduleName(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validControlScheduleRequestPath(r *http.Request, scheduleID string) bool {
	return validControlJobID(scheduleID) && r.URL.EscapedPath() == controlSchedulePathPrefix+scheduleID
}

func newControlScheduleView(record storage.ProbeScheduleRecord) (controlScheduleView, error) {
	config, err := protocol.MarshalProbeConfig(record.ProbeType, record.Config)
	if err != nil {
		return controlScheduleView{}, fmt.Errorf("config: %w", err)
	}
	return controlScheduleView{
		ScheduleID: record.ID, AgentID: record.AgentID, Name: record.Name, ProbeType: record.ProbeType,
		Config: config, TimeoutMS: record.TimeoutMS, IntervalSeconds: record.IntervalSeconds,
		Enabled: record.Enabled, NextRunAt: record.NextRunAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}
