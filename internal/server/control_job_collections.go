package server

import (
	"errors"
	"net/http"
	"strconv"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const controlJobCursorResource = "jobs"

type controlJobSummaryView struct {
	JobID         string                       `json:"job_id"`
	ScheduleID    *string                      `json:"schedule_id"`
	AgentID       string                       `json:"agent_id"`
	ProbeType     protocol.ProbeType           `json:"probe_type"`
	TimeoutMS     int                          `json:"timeout_ms"`
	CreatedAt     int64                        `json:"created_at"`
	ScheduledFor  int64                        `json:"scheduled_for"`
	NotBefore     int64                        `json:"not_before"`
	ExpiresAt     int64                        `json:"expires_at"`
	Status        storage.JobStatus            `json:"status"`
	Attempt       int64                        `json:"attempt"`
	Lease         *controlJobLeaseView         `json:"lease"`
	FinishedAt    *int64                       `json:"finished_at"`
	ResultSummary *controlJobResultSummaryView `json:"result_summary"`
}

type controlJobResultSummaryView struct {
	Success       bool    `json:"success"`
	DurationMS    float64 `json:"duration_ms"`
	ErrorCategory *string `json:"error_category"`
	FinishedAt    int64   `json:"finished_at"`
}

type controlJobCollectionView struct {
	Items      []controlJobSummaryView `json:"items"`
	NextCursor *string                 `json:"next_cursor"`
}

func (a *App) handleGetControlJobs(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	values, err := parseControlQuery(r, "agent_id", "schedule_id", "probe_type", "status", "success",
		"created_after", "created_before", "finished_after", "finished_before", "limit", "cursor")
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	query, err := parseControlJobFilters(values)
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	filters := controlFilterFingerprint(values, "agent_id", "schedule_id", "probe_type", "status", "success",
		"created_after", "created_before", "finished_after", "finished_before")
	query.Limit, query.After, err = parseControlCollectionPage(values, controlJobCursorResource, filters, validControlJobReadID)
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	records, next, err := a.store.QueryProbeJobs(r.Context(), query, a.now())
	if err != nil {
		a.logger.Error("query control jobs", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query jobs")
		return
	}
	items := make([]controlJobSummaryView, 0, len(records))
	for _, record := range records {
		view, err := newControlJobSummaryView(record)
		if err != nil {
			a.logger.Error("render listed control job", "job_id", record.Job.ID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query jobs")
			return
		}
		items = append(items, view)
	}
	nextCursor, err := encodeControlCursor(controlJobCursorResource, filters, next)
	if err != nil {
		a.logger.Error("encode control jobs cursor", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query jobs")
		return
	}
	writeJSON(w, http.StatusOK, controlJobCollectionView{Items: items, NextCursor: nextCursor})
}

func parseControlJobFilters(values map[string][]string) (storage.ProbeJobQuery, error) {
	var query storage.ProbeJobQuery
	query.AgentID = firstControlQueryValue(values, "agent_id")
	if query.AgentID != "" && !validControlAgentID(query.AgentID) {
		return query, errInvalidControlQuery
	}
	query.ScheduleID = firstControlQueryValue(values, "schedule_id")
	if query.ScheduleID != "" && !validControlJobID(query.ScheduleID) {
		return query, errInvalidControlQuery
	}
	query.ProbeType = protocol.ProbeType(firstControlQueryValue(values, "probe_type"))
	if query.ProbeType != "" {
		if err := query.ProbeType.Validate(); err != nil {
			return query, errInvalidControlQuery
		}
	}
	query.Status = storage.JobStatus(firstControlQueryValue(values, "status"))
	switch query.Status {
	case "", storage.JobStatusQueued, storage.JobStatusLeased, storage.JobStatusFinished, storage.JobStatusExpired:
	default:
		return query, errInvalidControlQuery
	}
	if value := firstControlQueryValue(values, "success"); value != "" {
		parsed, err := parseControlBool(value)
		if err != nil {
			return query, err
		}
		query.Success = &parsed
	}
	for name, target := range map[string]*int64{
		"created_after": &query.CreatedAfter, "created_before": &query.CreatedBefore,
		"finished_after": &query.FinishedAfter, "finished_before": &query.FinishedBefore,
	} {
		if value := firstControlQueryValue(values, name); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed <= 0 {
				return query, errInvalidControlQuery
			}
			*target = parsed
		}
	}
	if query.CreatedAfter != 0 && query.CreatedBefore != 0 && query.CreatedAfter >= query.CreatedBefore {
		return query, errInvalidControlQuery
	}
	if query.FinishedAfter != 0 && query.FinishedBefore != 0 && query.FinishedAfter >= query.FinishedBefore {
		return query, errInvalidControlQuery
	}
	if (query.Success != nil || query.FinishedAfter != 0 || query.FinishedBefore != 0) &&
		query.Status != "" && query.Status != storage.JobStatusFinished {
		return query, errInvalidControlQuery
	}
	return query, nil
}

func firstControlQueryValue(values map[string][]string, name string) string {
	entries := values[name]
	if len(entries) == 0 {
		return ""
	}
	return entries[0]
}

func parseControlBool(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errInvalidControlQuery
	}
}

func (a *App) handleControlJobCollectionMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func validControlJobReadID(value string) bool {
	return validLowerHexID(value, 32, 64)
}

func newControlJobSummaryView(record storage.ProbeJobListRecord) (controlJobSummaryView, error) {
	job := record.Job
	view := controlJobSummaryView{
		JobID: job.ID, ScheduleID: optionalControlString(job.ScheduleID), AgentID: job.AgentID,
		ProbeType: job.ProbeType, TimeoutMS: job.TimeoutMS, CreatedAt: job.CreatedAt,
		ScheduledFor: job.ScheduledFor, NotBefore: job.NotBefore, ExpiresAt: job.ExpiresAt,
		Status: job.Status, Attempt: job.Attempt,
	}
	switch job.Status {
	case storage.JobStatusQueued, storage.JobStatusExpired:
		if record.ResultSummary != nil {
			return controlJobSummaryView{}, errors.New("unfinished job has result summary")
		}
	case storage.JobStatusLeased:
		if job.LeasedAt <= 0 || job.LeaseUntil <= job.LeasedAt || record.ResultSummary != nil {
			return controlJobSummaryView{}, errors.New("leased job state is invalid")
		}
		view.Lease = &controlJobLeaseView{LeasedAt: job.LeasedAt, ExpiresAt: job.LeaseUntil}
	case storage.JobStatusFinished:
		if job.FinishedAt <= 0 || record.ResultSummary == nil || record.ResultSummary.ReceivedAt != job.FinishedAt {
			return controlJobSummaryView{}, errors.New("finished job is missing result summary")
		}
		finishedAt := job.FinishedAt
		view.FinishedAt = &finishedAt
		view.ResultSummary = &controlJobResultSummaryView{
			Success: record.ResultSummary.Success, DurationMS: record.ResultSummary.DurationMS,
			ErrorCategory: optionalControlString(record.ResultSummary.ErrorCategory), FinishedAt: record.ResultSummary.FinishedAt,
		}
	default:
		return controlJobSummaryView{}, errors.New("invalid stored job status")
	}
	return view, nil
}
