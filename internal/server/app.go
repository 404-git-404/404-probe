package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"404-probe/web"
)

type App struct {
	store            *storage.Store
	offlineTimeout   time.Duration
	logger           *slog.Logger
	now              func() time.Time
	controlTokenHash []byte
	shutdown         context.Context
	cancel           context.CancelFunc
	mu               sync.RWMutex
	states           map[string]storage.State
	hub              *hub
	handler          http.Handler
}

const (
	maxSSESubscribers        = 64
	maxClaimBodyBytes        = 8 << 10
	maxResultBodyBytes       = 16 << 10
	jobResultSubmissionGrace = 30 * time.Second
	jobLeaseDuration         = time.Duration(protocol.MaxProbeTimeoutMS)*time.Millisecond + jobResultSubmissionGrace
)

var errRequestTooLarge = errors.New("request body is too large")

type agentView struct {
	storage.State
	Online bool `json:"online"`
}

func NewApp(store *storage.Store, offlineTimeout time.Duration, logger *slog.Logger, options ...Option) (*App, error) {
	if offlineTimeout <= 0 {
		return nil, errors.New("offline timeout must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	states, err := store.ListStates(context.Background())
	if err != nil {
		return nil, err
	}
	shutdown, cancel := context.WithCancel(context.Background())
	a := &App{store: store, offlineTimeout: offlineTimeout, logger: logger, now: time.Now, shutdown: shutdown, cancel: cancel, states: make(map[string]storage.State), hub: newHub(maxSSESubscribers)}
	for _, option := range options {
		if option == nil {
			cancel()
			return nil, errors.New("server option must not be nil")
		}
		if err := option(a); err != nil {
			cancel()
			return nil, err
		}
	}
	for _, state := range states {
		a.states[state.AgentID] = state
	}
	a.handler = a.routes()
	return a, nil
}

func (a *App) Handler() http.Handler { return a.handler }

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/report", a.handleReport)
	mux.HandleFunc("POST /api/v1/agent/jobs/claim", a.handleClaimJob)
	mux.HandleFunc("/api/v1/agent/jobs/claim", requirePost)
	mux.HandleFunc("POST /api/v1/agent/jobs/{job_id}/result", a.handleJobResult)
	mux.HandleFunc("/api/v1/agent/jobs/{job_id}/result", requirePost)
	mux.HandleFunc("PUT /api/v1/control/jobs/{job_id}", a.handlePutControlJob)
	mux.HandleFunc("GET /api/v1/control/jobs/{job_id}", a.handleGetControlJob)
	mux.HandleFunc("/api/v1/control/jobs/{job_id}", a.handleControlJobMethodNotAllowed)
	mux.HandleFunc("/api/v1/control/jobs", a.handleControlCollectionNotFound)
	mux.HandleFunc("/api/v1/control/jobs/", a.handleControlInvalidPath)
	mux.HandleFunc("PUT /api/v1/control/schedules/{schedule_id}", a.handlePutControlSchedule)
	mux.HandleFunc("GET /api/v1/control/schedules/{schedule_id}", a.handleGetControlSchedule)
	mux.HandleFunc("DELETE /api/v1/control/schedules/{schedule_id}", a.handleDeleteControlSchedule)
	mux.HandleFunc("/api/v1/control/schedules/{schedule_id}", a.handleControlScheduleMethodNotAllowed)
	mux.HandleFunc("/api/v1/control/schedules", a.handleControlCollectionNotFound)
	mux.HandleFunc("/api/v1/control/schedules/", a.handleControlInvalidPath)
	mux.HandleFunc("GET /api/v1/control/agents/{agent_id}", a.handleGetControlAgent)
	mux.HandleFunc("/api/v1/control/agents/{agent_id}", a.handleControlAgentMethodNotAllowed)
	mux.HandleFunc("GET /api/v1/control/agents", a.handleGetControlAgents)
	mux.HandleFunc("/api/v1/control/agents", a.handleControlAgentCollectionMethodNotAllowed)
	mux.HandleFunc("/api/v1/control/agents/", a.handleControlInvalidPath)
	mux.HandleFunc("GET /api/v1/agents", a.handleAgents)
	mux.HandleFunc("GET /api/v1/agents/{id}/history", a.handleHistory)
	mux.HandleFunc("GET /api/v1/events", a.handleEvents)
	static, err := fs.Sub(web.Files, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(static)))
	return securityHeaders(a.rejectAmbiguousJobPaths(mux))
}

func requirePost(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
}

func (a *App) rejectAmbiguousJobPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.ToLower(r.URL.EscapedPath())
		controlPrefix := ""
		if strings.HasPrefix(path, controlJobPathPrefix) {
			controlPrefix = controlJobPathPrefix
		} else if strings.HasPrefix(path, controlSchedulePathPrefix) {
			controlPrefix = controlSchedulePathPrefix
		} else if strings.HasPrefix(path, controlAgentPathPrefix) {
			controlPrefix = controlAgentPathPrefix
		}
		if controlPrefix != "" && ambiguousControlItemPath(path, controlPrefix) {
			setControlNoStore(w)
			if !a.authenticateControlRequest(w, r) {
				return
			}
			writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid control path")
			return
		}
		if strings.HasPrefix(path, "/api/v1/agent/jobs/") &&
			(strings.Contains(path, "//") || strings.Contains(path, "%2f") || strings.Contains(path, "%5c")) {
			writeJobError(w, http.StatusNotFound, "job_not_found", "job not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func ambiguousControlItemPath(path, prefix string) bool {
	suffix := strings.TrimPrefix(path, prefix)
	return strings.HasPrefix(suffix, "/") || strings.Contains(suffix, "//") || strings.Contains(suffix, "%2f") ||
		strings.Contains(suffix, "%5c") || strings.Contains(suffix, ".") || strings.Contains(suffix, "%2e")
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleReport(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	authenticatedID, err := a.store.Authenticate(r.Context(), token)
	if err != nil {
		if !errors.Is(err, storage.ErrUnauthorized) {
			a.logger.Error("authenticate report", "error", err)
		}
		writeError(w, http.StatusUnauthorized, "invalid agent token")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxReportBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var report protocol.Report
	if err := decoder.Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid report: "+err.Error())
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "invalid report: "+err.Error())
		return
	}
	if err := report.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, accepted, reason, err := a.store.ProcessReport(r.Context(), authenticatedID, report, a.now())
	if err != nil {
		if errors.Is(err, storage.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "agent ID does not match token")
			return
		}
		a.logger.Error("process report", "agent_id", authenticatedID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not process report")
		return
	}
	if accepted {
		a.publishState(state)
	}
	writeJSON(w, http.StatusOK, protocol.ReportResponse{Accepted: accepted, Reason: reason})
}

func (a *App) handleClaimJob(w http.ResponseWriter, r *http.Request) {
	agentID, ok := a.authenticateJobRequest(w, r, "claim job")
	if !ok {
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	body, err := readBoundedBody(w, r, maxClaimBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	request, err := protocol.DecodeClaimRequest(body)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid claim request")
		return
	}
	job, err := a.store.ClaimJob(r.Context(), agentID, request, a.now(), jobLeaseDuration)
	if err != nil {
		a.writeClaimError(w, agentID, err)
		return
	}
	if job == nil {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (a *App) handleJobResult(w http.ResponseWriter, r *http.Request) {
	agentID, ok := a.authenticateJobRequest(w, r, "submit job result")
	if !ok {
		return
	}
	if !hasJSONContentType(r) {
		writeJobError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	jobID := r.PathValue("job_id")
	if !validPathJobID(jobID) {
		writeJobError(w, http.StatusNotFound, "job_not_found", "job not found")
		return
	}
	job, err := a.store.GetProbeJob(r.Context(), jobID)
	if err != nil {
		if errors.Is(err, storage.ErrJobNotFound) {
			writeJobError(w, http.StatusNotFound, "job_not_found", "job not found")
			return
		}
		a.logger.Error("read job for result", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not process job result")
		return
	}
	if job.AgentID != agentID {
		writeJobError(w, http.StatusNotFound, "job_not_found", "job not found")
		return
	}
	body, err := readBoundedBody(w, r, maxResultBodyBytes)
	if err != nil {
		writeBodyError(w, err)
		return
	}
	result, err := protocol.DecodeJobResult(body, job.ProbeType)
	if err != nil {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid job result")
		return
	}
	ack, err := a.store.SubmitJobResult(r.Context(), agentID, jobID, result, a.now())
	if err != nil {
		a.writeResultError(w, agentID, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Accepted  bool   `json:"accepted"`
		Duplicate bool   `json:"duplicate"`
		JobStatus string `json:"job_status"`
	}{Accepted: true, Duplicate: ack.Duplicate, JobStatus: string(storage.JobStatusFinished)})
}

func (a *App) authenticateJobRequest(w http.ResponseWriter, r *http.Request, operation string) (string, bool) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeJobError(w, http.StatusUnauthorized, "unauthorized", "invalid agent token")
		return "", false
	}
	agentID, err := a.store.Authenticate(r.Context(), token)
	if err != nil {
		if errors.Is(err, storage.ErrUnauthorized) {
			writeJobError(w, http.StatusUnauthorized, "unauthorized", "invalid agent token")
			return "", false
		}
		a.logger.Error(operation, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not authenticate agent")
		return "", false
	}
	return agentID, true
}

func (a *App) writeClaimError(w http.ResponseWriter, agentID string, err error) {
	switch {
	case errors.Is(err, storage.ErrUnauthorized):
		writeJobError(w, http.StatusUnauthorized, "unauthorized", "invalid agent token")
	case errors.Is(err, storage.ErrAttemptExhausted):
		writeJobError(w, http.StatusConflict, "attempt_exhausted", "job attempt limit exhausted")
	default:
		a.logger.Error("claim job", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not claim job")
	}
}

func (a *App) writeResultError(w http.ResponseWriter, agentID string, err error) {
	switch {
	case errors.Is(err, storage.ErrUnauthorized):
		writeJobError(w, http.StatusUnauthorized, "unauthorized", "invalid agent token")
	case errors.Is(err, storage.ErrJobNotFound):
		writeJobError(w, http.StatusNotFound, "job_not_found", "job not found")
	case errors.Is(err, storage.ErrLeaseLost):
		writeJobError(w, http.StatusConflict, "lease_lost", "job lease is no longer valid")
	case errors.Is(err, storage.ErrResultConflict):
		writeJobError(w, http.StatusConflict, "result_conflict", "job result conflicts with the stored result")
	case errors.Is(err, storage.ErrJobExpired):
		writeJobError(w, http.StatusGone, "job_expired", "job has expired")
	case errors.Is(err, storage.ErrInvalidJobResult):
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid job result")
	default:
		a.logger.Error("submit job result", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not process job result")
	}
}

func hasJSONContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func readBoundedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errRequestTooLarge
		}
		return nil, err
	}
	return body, nil
}

func writeBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errRequestTooLarge) {
		writeJobError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
		return
	}
	writeJobError(w, http.StatusBadRequest, "invalid_request", "could not read request body")
}

func validPathJobID(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\%?#") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (a *App) publishState(state storage.State) bool {
	a.mu.Lock()
	current, exists := a.states[state.AgentID]
	if exists && !stateAfter(state, current) {
		a.mu.Unlock()
		return false
	}
	a.states[state.AgentID] = state
	payload, _ := json.Marshal(agentView{State: state, Online: true})
	a.hub.publish(payload)
	a.mu.Unlock()
	return true
}

func stateAfter(candidate, current storage.State) bool {
	if candidate.Epoch != current.Epoch {
		return candidate.Epoch > current.Epoch
	}
	return candidate.SessionID == current.SessionID && candidate.Sequence > current.Sequence
}

func (a *App) handleAgents(w http.ResponseWriter, r *http.Request) {
	now := a.now()
	a.mu.RLock()
	views := make([]agentView, 0, len(a.states))
	for _, state := range a.states {
		views = append(views, agentView{State: state, Online: state.LastSeen > 0 && now.Sub(time.UnixMilli(state.LastSeen)) <= a.offlineTimeout})
	}
	a.mu.RUnlock()
	sort.Slice(views, func(i, j int) bool {
		if views[i].Name == views[j].Name {
			return views[i].AgentID < views[j].AgentID
		}
		return views[i].Name < views[j].Name
	})
	writeJSON(w, http.StatusOK, views)
}

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	hours := 24
	if value := r.URL.Query().Get("hours"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 24*30 {
			writeError(w, http.StatusBadRequest, "hours must be between 1 and 720")
			return
		}
		hours = parsed
	}
	points, err := a.store.History(r.Context(), id, a.now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		a.logger.Error("read history", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	if points == nil {
		points = []storage.HistoryPoint{}
	}
	writeJSON(w, http.StatusOK, points)
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, ok := a.hub.subscribe()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "too many event subscribers")
		return
	}
	defer a.hub.unsubscribe(ch)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-a.shutdown.Done():
			return
		case <-r.Context().Done():
			return
		case message := <-ch:
			_, _ = fmt.Fprintf(w, "event: agent\ndata: %s\n\n", message)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (a *App) Shutdown() { a.cancel() }

func (a *App) CleanupLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-a.shutdown.Done():
			return
		case <-ticker.C:
			if err := a.store.CleanupHistory(a.shutdown, a.now().Add(-30*24*time.Hour)); err != nil && a.shutdown.Err() == nil {
				a.logger.Error("clean history", "error", err)
			}
		}
	}
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		returnValue = parts[1]
	}
	return returnValue, returnValue != ""
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJobError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
