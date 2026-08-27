package server

import (
	"errors"
	"fmt"
	"net/http"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	webJobPathPrefix     = "/api/v1/web/jobs/"
	webJobCursorResource = "web_jobs"
)

type webJobSummaryView struct {
	JobID         string                   `json:"job_id"`
	ScheduleID    *string                  `json:"schedule_id"`
	AgentID       string                   `json:"agent_id"`
	ProbeType     protocol.ProbeType       `json:"probe_type"`
	TimeoutMS     int                      `json:"timeout_ms"`
	CreatedAt     int64                    `json:"created_at"`
	ScheduledFor  int64                    `json:"scheduled_for"`
	NotBefore     int64                    `json:"not_before"`
	ExpiresAt     int64                    `json:"expires_at"`
	Status        storage.JobStatus        `json:"status"`
	Attempt       int64                    `json:"attempt"`
	Lease         *webJobLeaseView         `json:"lease"`
	FinishedAt    *int64                   `json:"finished_at"`
	ResultSummary *webJobResultSummaryView `json:"result_summary"`
}

type webJobLeaseView struct {
	LeasedAt  int64 `json:"leased_at"`
	ExpiresAt int64 `json:"expires_at"`
}

type webJobResultSummaryView struct {
	Success       bool    `json:"success"`
	DurationMS    float64 `json:"duration_ms"`
	ErrorCategory *string `json:"error_category"`
	FinishedAt    int64   `json:"finished_at"`
}

type webJobDetailView struct {
	webJobSummaryView
	Config webScheduleConfigView `json:"config"`
	Result *webJobResultView     `json:"result"`
}

type webJobResultView struct {
	ReceivedAt    int64                 `json:"received_at"`
	StartedAt     int64                 `json:"started_at"`
	FinishedAt    int64                 `json:"finished_at"`
	DurationMS    float64               `json:"duration_ms"`
	Success       bool                  `json:"success"`
	ResolvedIP    *string               `json:"resolved_ip"`
	ErrorCategory *string               `json:"error_category"`
	ErrorMessage  *string               `json:"error_message"`
	Measurement   webJobMeasurementView `json:"measurement"`
}

type webJobMeasurementView struct {
	HTTP       *webHTTPMeasurementView       `json:"http,omitempty"`
	TCPConnect *webTCPConnectMeasurementView `json:"tcp_connect,omitempty"`
	ICMPPing   *webICMPPingMeasurementView   `json:"icmp_ping,omitempty"`
}

type webHTTPMeasurementView struct {
	DNSMS         float64 `json:"dns_ms"`
	ConnectMS     float64 `json:"connect_ms"`
	TLSMS         float64 `json:"tls_ms"`
	TTFBMS        float64 `json:"ttfb_ms"`
	TotalMS       float64 `json:"total_ms"`
	StatusCode    int     `json:"status_code"`
	BodyBytes     uint64  `json:"body_bytes"`
	BodyTruncated bool    `json:"body_truncated"`
}

type webTCPConnectMeasurementView struct {
	ConnectMS float64 `json:"connect_ms"`
}

type webICMPPingMeasurementView struct {
	Sent              int     `json:"sent"`
	Received          int     `json:"received"`
	PacketLossPercent float64 `json:"packet_loss_percent"`
	LatencyMinMS      float64 `json:"latency_min_ms"`
	LatencyAvgMS      float64 `json:"latency_avg_ms"`
	LatencyMaxMS      float64 `json:"latency_max_ms"`
}

type webJobCollectionView struct {
	Items      []webJobSummaryView `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

func (a *App) handleGetWebJobs(w http.ResponseWriter, r *http.Request) {
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
	query.Limit, query.After, err = parseControlCollectionPage(values, webJobCursorResource, filters, validWebJobID)
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	records, next, err := a.store.QueryProbeJobs(r.Context(), query, a.now())
	if err != nil {
		a.logger.Error("query Web jobs", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query jobs")
		return
	}
	items := make([]webJobSummaryView, 0, len(records))
	for _, record := range records {
		view, err := newWebJobSummaryView(record)
		if err != nil {
			a.logger.Error("render listed Web job", "job_id", record.Job.ID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query jobs")
			return
		}
		items = append(items, view)
	}
	nextCursor, err := encodeControlCursor(webJobCursorResource, filters, next)
	if err != nil {
		a.logger.Error("encode Web jobs cursor", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query jobs")
		return
	}
	writeJSON(w, http.StatusOK, webJobCollectionView{Items: items, NextCursor: nextCursor})
}

func (a *App) handleGetWebJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job_id")
	if !validWebJobRequestPath(r, jobID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid job ID")
		return
	}
	if _, err := parseControlQuery(r); err != nil {
		writeControlCollectionError(w, err)
		return
	}
	job, result, err := a.store.GetProbeJobSnapshot(r.Context(), jobID, a.now())
	if err != nil {
		if errors.Is(err, storage.ErrJobNotFound) {
			writeJobError(w, http.StatusNotFound, "job_not_found", "job not found")
			return
		}
		a.logger.Error("read Web job", "job_id", jobID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read job")
		return
	}
	view, err := newWebJobDetailView(job, result)
	if err != nil {
		a.logger.Error("render Web job", "job_id", jobID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read job")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) handleWebJobCollectionMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func (a *App) handleWebJobMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebJobRequestPath(r, r.PathValue("job_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid job ID")
		return
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func validWebJobRequestPath(r *http.Request, jobID string) bool {
	return validWebJobID(jobID) && r.URL.EscapedPath() == webJobPathPrefix+jobID
}

func validWebJobID(value string) bool { return validLowerHexID(value, 32, 64) }

func newWebJobSummaryView(record storage.ProbeJobListRecord) (webJobSummaryView, error) {
	job := record.Job
	view := webJobSummaryView{
		JobID: job.ID, ScheduleID: optionalWebString(job.ScheduleID), AgentID: job.AgentID,
		ProbeType: job.ProbeType, TimeoutMS: job.TimeoutMS, CreatedAt: job.CreatedAt,
		ScheduledFor: job.ScheduledFor, NotBefore: job.NotBefore, ExpiresAt: job.ExpiresAt,
		Status: job.Status, Attempt: job.Attempt,
	}
	switch job.Status {
	case storage.JobStatusQueued, storage.JobStatusExpired:
		if record.ResultSummary != nil {
			return webJobSummaryView{}, errors.New("unfinished job has result summary")
		}
	case storage.JobStatusLeased:
		if job.LeasedAt <= 0 || job.LeaseUntil <= job.LeasedAt || record.ResultSummary != nil {
			return webJobSummaryView{}, errors.New("leased job state is invalid")
		}
		view.Lease = &webJobLeaseView{LeasedAt: job.LeasedAt, ExpiresAt: job.LeaseUntil}
	case storage.JobStatusFinished:
		if job.FinishedAt <= 0 || record.ResultSummary == nil || record.ResultSummary.ReceivedAt != job.FinishedAt {
			return webJobSummaryView{}, errors.New("finished job is missing result summary")
		}
		finishedAt := job.FinishedAt
		view.FinishedAt = &finishedAt
		view.ResultSummary = &webJobResultSummaryView{
			Success: record.ResultSummary.Success, DurationMS: record.ResultSummary.DurationMS,
			ErrorCategory: optionalWebString(record.ResultSummary.ErrorCategory), FinishedAt: record.ResultSummary.FinishedAt,
		}
	default:
		return webJobSummaryView{}, errors.New("invalid stored job status")
	}
	return view, nil
}

func newWebJobDetailView(job storage.ProbeJobRecord, result *storage.ProbeResultRecord) (webJobDetailView, error) {
	summary, err := newWebJobSummaryView(storage.ProbeJobListRecord{Job: job, ResultSummary: webResultSummaryRecord(result)})
	if err != nil {
		return webJobDetailView{}, err
	}
	config, err := newWebScheduleConfigView(job.ProbeType, job.Config)
	if err != nil {
		return webJobDetailView{}, fmt.Errorf("config: %w", err)
	}
	view := webJobDetailView{webJobSummaryView: summary, Config: config}
	if result != nil {
		measurement, err := newWebJobMeasurementView(job.ProbeType, result.Result.Result)
		if err != nil {
			return webJobDetailView{}, fmt.Errorf("result: %w", err)
		}
		view.Result = &webJobResultView{
			ReceivedAt: result.ReceivedAt, StartedAt: result.Result.StartedAt,
			FinishedAt: result.Result.FinishedAt, DurationMS: result.Result.DurationMS,
			Success: result.Result.Success, ResolvedIP: optionalWebString(result.Result.ResolvedIP),
			ErrorCategory: optionalWebString(result.Result.ErrorCategory),
			ErrorMessage:  optionalWebString(result.Result.ErrorMessage), Measurement: measurement,
		}
	}
	return view, nil
}

func webResultSummaryRecord(result *storage.ProbeResultRecord) *storage.ProbeResultSummaryRecord {
	if result == nil {
		return nil
	}
	return &storage.ProbeResultSummaryRecord{
		ReceivedAt: result.ReceivedAt, FinishedAt: result.Result.FinishedAt,
		DurationMS: result.Result.DurationMS, Success: result.Result.Success,
		ErrorCategory: result.Result.ErrorCategory,
	}
}

func newWebJobMeasurementView(probeType protocol.ProbeType, measurement protocol.ProbeResult) (webJobMeasurementView, error) {
	if err := measurement.Validate(probeType); err != nil {
		return webJobMeasurementView{}, err
	}
	switch probeType {
	case protocol.ProbeTypeHTTP:
		value := measurement.HTTP
		return webJobMeasurementView{HTTP: &webHTTPMeasurementView{
			DNSMS: value.DNSMS, ConnectMS: value.ConnectMS, TLSMS: value.TLSMS,
			TTFBMS: value.TTFBMS, TotalMS: value.TotalMS, StatusCode: value.StatusCode,
			BodyBytes: value.BodyBytes, BodyTruncated: value.BodyTruncated,
		}}, nil
	case protocol.ProbeTypeTCPConnect:
		return webJobMeasurementView{TCPConnect: &webTCPConnectMeasurementView{ConnectMS: measurement.TCPConnect.ConnectMS}}, nil
	case protocol.ProbeTypeICMPPing:
		value := measurement.ICMPPing
		return webJobMeasurementView{ICMPPing: &webICMPPingMeasurementView{
			Sent: value.Sent, Received: value.Received, PacketLossPercent: value.PacketLossPercent,
			LatencyMinMS: value.LatencyMinMS, LatencyAvgMS: value.LatencyAvgMS, LatencyMaxMS: value.LatencyMaxMS,
		}}, nil
	default:
		return webJobMeasurementView{}, fmt.Errorf("unknown probe type %q", probeType)
	}
}

func optionalWebString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
