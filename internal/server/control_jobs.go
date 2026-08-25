package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	maxControlJobBodyBytes = 8 << 10
	minJobTTLSeconds       = 60
	maxJobTTLSeconds       = 24 * 60 * 60
	controlJobPathPrefix   = "/api/v1/control/jobs/"
)

type Option func(*App) error

func WithControlTokenHash(hash []byte) Option {
	return func(app *App) error {
		if len(hash) != sha256.Size {
			return errors.New("control token hash must be a SHA-256 digest")
		}
		app.controlTokenHash = append([]byte(nil), hash...)
		return nil
	}
}

type controlCreateJobRequest struct {
	AgentID          string             `json:"agent_id"`
	ProbeType        protocol.ProbeType `json:"probe_type"`
	Config           json.RawMessage    `json:"config"`
	TimeoutMS        int                `json:"timeout_ms"`
	ExpiresInSeconds int64              `json:"expires_in_seconds"`
}

type controlJobView struct {
	JobID      string                `json:"job_id"`
	AgentID    string                `json:"agent_id"`
	ProbeType  protocol.ProbeType    `json:"probe_type"`
	Config     json.RawMessage       `json:"config"`
	TimeoutMS  int                   `json:"timeout_ms"`
	CreatedAt  int64                 `json:"created_at"`
	NotBefore  int64                 `json:"not_before"`
	ExpiresAt  int64                 `json:"expires_at"`
	Status     storage.JobStatus     `json:"status"`
	Attempt    int64                 `json:"attempt"`
	Lease      *controlJobLeaseView  `json:"lease"`
	FinishedAt *int64                `json:"finished_at"`
	Result     *controlJobResultView `json:"result"`
}

type controlJobLeaseView struct {
	LeasedAt  int64 `json:"leased_at"`
	ExpiresAt int64 `json:"expires_at"`
}

type controlJobResultView struct {
	ReceivedAt    int64           `json:"received_at"`
	StartedAt     int64           `json:"started_at"`
	FinishedAt    int64           `json:"finished_at"`
	DurationMS    float64         `json:"duration_ms"`
	Success       bool            `json:"success"`
	ResolvedIP    *string         `json:"resolved_ip"`
	ErrorCategory *string         `json:"error_category"`
	ErrorMessage  *string         `json:"error_message"`
	Measurement   json.RawMessage `json:"measurement"`
}

func (a *App) handlePutControlJob(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	jobID := r.PathValue("job_id")
	if !validControlJobRequestPath(r, jobID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid job ID")
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
	request, err := decodeControlCreateJobRequest(body)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid control job request")
		return
	}
	now := a.now()
	createdAt := now.UnixMilli()
	ttlMillis := request.ExpiresInSeconds * int64(time.Second/time.Millisecond)
	if createdAt <= 0 || createdAt > math.MaxInt64-ttlMillis {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "job expiry is outside the supported time range")
		return
	}
	config, err := protocol.DecodeProbeConfig(request.ProbeType, request.Config)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid probe config")
		return
	}
	_, created, err := a.store.CreateOneShotJobIdempotent(r.Context(), storage.CreateOneShotJobParams{
		ID: jobID, AgentID: request.AgentID, ProbeType: request.ProbeType, Config: config,
		TimeoutMS: request.TimeoutMS, CreatedAt: createdAt, NotBefore: createdAt, ExpiresAt: createdAt + ttlMillis,
	})
	if err != nil {
		a.writeControlCreateError(w, err)
		return
	}
	job, result, err := a.store.GetProbeJobSnapshot(r.Context(), jobID, now)
	if err != nil {
		a.logger.Error("read created control job", "job_id", jobID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read control job")
		return
	}
	view, err := newControlJobView(job, result)
	if err != nil {
		a.logger.Error("render created control job", "job_id", jobID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read control job")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", controlJobPathPrefix+jobID)
	}
	writeJSON(w, status, view)
}

func (a *App) handleGetControlJob(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	jobID := r.PathValue("job_id")
	if !validControlJobReadRequestPath(r, jobID) {
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
		a.logger.Error("read control job", "job_id", jobID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read control job")
		return
	}
	view, err := newControlJobView(job, result)
	if err != nil {
		a.logger.Error("render control job", "job_id", jobID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read control job")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) handleControlJobMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	if !validControlJobRequestPath(r, r.PathValue("job_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid job ID")
		return
	}
	w.Header().Set("Allow", "GET, PUT")
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET or PUT")
}

func (a *App) handleControlInvalidPath(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid control job path")
}

func (a *App) authenticateControlRequest(w http.ResponseWriter, r *http.Request) bool {
	if len(a.controlTokenHash) == 0 {
		http.NotFound(w, r)
		return false
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	candidate := auth.Hash(token)
	if !ok || subtle.ConstantTimeCompare(candidate, a.controlTokenHash) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJobError(w, http.StatusUnauthorized, "unauthorized", "invalid control token")
		return false
	}
	return true
}

func (a *App) writeControlCreateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrAgentNotFound):
		writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
	case errors.Is(err, storage.ErrJobIDConflict):
		writeJobError(w, http.StatusConflict, "idempotency_conflict", "job ID is already used by a different request")
	case errors.Is(err, storage.ErrOutstandingJobsFull):
		w.Header().Set("Retry-After", "10")
		writeJobError(w, http.StatusTooManyRequests, "queue_full", "agent job queue is full")
	default:
		a.logger.Error("create control job", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create control job")
	}
}

func decodeControlCreateJobRequest(body []byte) (controlCreateJobRequest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var request controlCreateJobRequest
	if err := decoder.Decode(&request); err != nil {
		return controlCreateJobRequest{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return controlCreateJobRequest{}, err
	}
	if !validPathJobID(request.AgentID) {
		return controlCreateJobRequest{}, errors.New("invalid agent ID")
	}
	if err := request.ProbeType.Validate(); err != nil {
		return controlCreateJobRequest{}, err
	}
	if len(request.Config) == 0 || string(request.Config) == "null" {
		return controlCreateJobRequest{}, errors.New("config is required")
	}
	if request.TimeoutMS < protocol.MinProbeTimeoutMS || request.TimeoutMS > protocol.MaxProbeTimeoutMS {
		return controlCreateJobRequest{}, errors.New("invalid timeout")
	}
	if request.ExpiresInSeconds < minJobTTLSeconds || request.ExpiresInSeconds > maxJobTTLSeconds {
		return controlCreateJobRequest{}, errors.New("invalid expiry")
	}
	return request, nil
}

func validControlJobRequestPath(r *http.Request, jobID string) bool {
	return validControlJobID(jobID) && r.URL.EscapedPath() == controlJobPathPrefix+jobID
}

func validControlJobReadRequestPath(r *http.Request, jobID string) bool {
	return validLowerHexID(jobID, 32, 64) && r.URL.EscapedPath() == controlJobPathPrefix+jobID
}

func validControlJobID(value string) bool {
	return validLowerHexID(value, 32)
}

func validLowerHexID(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		if len(value) == length {
			validLength = true
			break
		}
	}
	if !validLength {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func newControlJobView(job storage.ProbeJobRecord, result *storage.ProbeResultRecord) (controlJobView, error) {
	config, err := protocol.MarshalProbeConfig(job.ProbeType, job.Config)
	if err != nil {
		return controlJobView{}, fmt.Errorf("config: %w", err)
	}
	view := controlJobView{
		JobID: job.ID, AgentID: job.AgentID, ProbeType: job.ProbeType, Config: config,
		TimeoutMS: job.TimeoutMS, CreatedAt: job.CreatedAt, NotBefore: job.NotBefore,
		ExpiresAt: job.ExpiresAt, Status: job.Status, Attempt: job.Attempt,
	}
	switch job.Status {
	case storage.JobStatusQueued, storage.JobStatusExpired:
	case storage.JobStatusLeased:
		if job.LeasedAt <= 0 || job.LeaseUntil <= job.LeasedAt {
			return controlJobView{}, errors.New("invalid stored lease")
		}
		view.Lease = &controlJobLeaseView{LeasedAt: job.LeasedAt, ExpiresAt: job.LeaseUntil}
	case storage.JobStatusFinished:
		if job.FinishedAt <= 0 || result == nil || result.ReceivedAt != job.FinishedAt {
			return controlJobView{}, errors.New("finished job is missing result data")
		}
		finishedAt := job.FinishedAt
		view.FinishedAt = &finishedAt
		measurement, err := protocol.MarshalProbeResult(job.ProbeType, result.Result.Result)
		if err != nil {
			return controlJobView{}, fmt.Errorf("result: %w", err)
		}
		view.Result = &controlJobResultView{
			ReceivedAt: result.ReceivedAt, StartedAt: result.Result.StartedAt,
			FinishedAt: result.Result.FinishedAt, DurationMS: result.Result.DurationMS,
			Success: result.Result.Success, ResolvedIP: optionalControlString(result.Result.ResolvedIP),
			ErrorCategory: optionalControlString(result.Result.ErrorCategory),
			ErrorMessage:  optionalControlString(result.Result.ErrorMessage), Measurement: measurement,
		}
	default:
		return controlJobView{}, errors.New("invalid stored job status")
	}
	if job.Status != storage.JobStatusFinished && result != nil {
		return controlJobView{}, errors.New("unfinished job has result data")
	}
	return view, nil
}

func optionalControlString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
