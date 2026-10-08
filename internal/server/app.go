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
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"404-probe/web"
)

type App struct {
	store                         *storage.Store
	offlineTimeout                time.Duration
	logger                        *slog.Logger
	now                           func() time.Time
	controlTokenHash              []byte
	shutdown                      context.Context
	cancel                        context.CancelFunc
	mu                            sync.RWMutex
	states                        map[string]storage.State
	hub                           *hub
	traffic                       *trafficCache
	controlMu                     sync.Mutex
	controlWaiters                map[string]map[chan struct{}]struct{}
	controlPollDuration           time.Duration
	removalReceiptCleanupInterval time.Duration
	walHealthInterval             time.Duration
	walHealthAlertMu              sync.Mutex
	walHealthAlert                sqliteWALAlertState
	webAuth                       *webAuthenticator
	buildInfo                     buildinfo.Info
	handler                       http.Handler
	staticAssets                  *staticAssets
}

const (
	maxSSESubscribers        = 64
	sseFrameWriteTimeout     = 5 * time.Second
	maxClaimBodyBytes        = 8 << 10
	maxResultBodyBytes       = 16 << 10
	jobResultSubmissionGrace = 30 * time.Second
	jobLeaseDuration         = time.Duration(protocol.MaxProbeTimeoutMS)*time.Millisecond + jobResultSubmissionGrace
)

var errRequestTooLarge = errors.New("request body is too large")

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
	traffic, err := newTrafficCache()
	if err != nil {
		cancel()
		return nil, err
	}
	a := &App{store: store, offlineTimeout: offlineTimeout, logger: logger, now: time.Now, shutdown: shutdown, cancel: cancel, states: make(map[string]storage.State), hub: newHub(maxSSESubscribers), controlWaiters: make(map[string]map[chan struct{}]struct{}), controlPollDuration: controlLongPollDuration, removalReceiptCleanupInterval: 5 * time.Minute, walHealthInterval: sqliteWALHealthInterval, buildInfo: buildinfo.Current()}
	a.traffic = traffic
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
	static, err := fs.Sub(web.Files, "static")
	if err == nil {
		a.staticAssets, err = newStaticAssets(static)
	}
	if err != nil {
		cancel()
		return nil, fmt.Errorf("initialize static assets: %w", err)
	}
	a.handler = a.routes()
	return a, nil
}

func (a *App) Handler() http.Handler { return a.handler }

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	a.qualityRoutes(mux)
	mux.HandleFunc("POST /api/v1/report", a.handleReport)
	mux.HandleFunc("POST /api/v1/agent/outbounds", a.handleAgentOutbounds)
	mux.HandleFunc("/api/v1/agent/outbounds", requirePost)
	mux.HandleFunc("POST /api/v1/agent/security", a.handleAgentSecurity)
	mux.HandleFunc("/api/v1/agent/security", requirePost)
	mux.HandleFunc("POST /api/v1/agent/jobs/claim", a.handleClaimJob)
	mux.HandleFunc("/api/v1/agent/jobs/claim", requirePost)
	mux.HandleFunc("POST /api/v1/agent/control/claim", a.handleClaimControl)
	mux.HandleFunc("/api/v1/agent/control/claim", requirePost)
	mux.HandleFunc("POST /api/v1/agent/removals/claim", a.handleClaimAgentRemoval)
	mux.HandleFunc("POST /api/v1/agent/removals/{operation_id}/status", a.handleAgentRemovalStatus)
	mux.HandleFunc("POST /api/v1/agent/removals/{operation_id}/receipt", a.handleAgentRemovalReceipt)
	mux.HandleFunc("POST /api/v1/agent/country-code-lookups/claim", a.handleClaimAgentCountryCodeLookup)
	mux.HandleFunc("POST /api/v1/agent/country-code-lookups/{operation_id}/result", a.handleAgentCountryCodeLookupResult)
	mux.HandleFunc("POST /api/v1/agent/jobs/{job_id}/result", a.handleJobResult)
	mux.HandleFunc("/api/v1/agent/jobs/{job_id}/result", requirePost)
	mux.HandleFunc("POST /api/v1/agent/upgrades/claim", a.handleClaimUpgrade)
	mux.HandleFunc("POST /api/v1/agent/upgrades/{operation_id}/status", a.handleUpgradeStatus)
	mux.HandleFunc("PUT /api/v1/control/jobs/{job_id}", a.handlePutControlJob)
	mux.HandleFunc("GET /api/v1/control/jobs/{job_id}", a.handleGetControlJob)
	mux.HandleFunc("/api/v1/control/jobs/{job_id}", a.handleControlJobMethodNotAllowed)
	mux.HandleFunc("GET /api/v1/control/jobs", a.handleGetControlJobs)
	mux.HandleFunc("/api/v1/control/jobs", a.handleControlJobCollectionMethodNotAllowed)
	mux.HandleFunc("/api/v1/control/jobs/", a.handleControlInvalidPath)
	mux.HandleFunc("PUT /api/v1/control/schedules/{schedule_id}", a.handlePutControlSchedule)
	mux.HandleFunc("GET /api/v1/control/schedules/{schedule_id}", a.handleGetControlSchedule)
	mux.HandleFunc("DELETE /api/v1/control/schedules/{schedule_id}", a.handleDeleteControlSchedule)
	mux.HandleFunc("/api/v1/control/schedules/{schedule_id}", a.handleControlScheduleMethodNotAllowed)
	mux.HandleFunc("GET /api/v1/control/schedules", a.handleGetControlSchedules)
	mux.HandleFunc("/api/v1/control/schedules", a.handleControlScheduleCollectionMethodNotAllowed)
	mux.HandleFunc("/api/v1/control/schedules/", a.handleControlInvalidPath)
	mux.HandleFunc("GET /api/v1/control/agents/{agent_id}", a.handleGetControlAgent)
	mux.HandleFunc("/api/v1/control/agents/{agent_id}", a.handleControlAgentMethodNotAllowed)
	mux.HandleFunc("GET /api/v1/control/agents", a.handleGetControlAgents)
	mux.HandleFunc("/api/v1/control/agents", a.handleControlAgentCollectionMethodNotAllowed)
	mux.HandleFunc("/api/v1/control/agents/", a.handleControlInvalidPath)
	a.webRoutes(mux, a.staticAssets)
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
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
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
		if errors.Is(err, storage.ErrAgentRevoked) {
			writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
			return
		}
		if errors.Is(err, storage.ErrAgentDisabled) {
			writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
			return
		}
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
	received := a.now()
	owner := a.traffic.capture(authenticatedID, received)
	// Retired slots can be evicted; pending removal is authoritative even then.
	// Optional cache lookup errors only skip chart retention, never report ACKs.
	if _, removing, lookupErr := a.store.GetAgentRemovalOperation(r.Context(), authenticatedID); removing || lookupErr != nil {
		owner = trafficOwner{}
	}
	state, accepted, reason, err := a.store.ProcessReport(r.Context(), authenticatedID, report, received)
	if err != nil {
		if errors.Is(err, storage.ErrAgentRevoked) {
			writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
			return
		}
		if errors.Is(err, storage.ErrAgentDisabled) {
			writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
			return
		}
		if errors.Is(err, storage.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "agent ID does not match token")
			return
		}
		a.logger.Error("process report", "agent_id", authenticatedID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not process report")
		return
	}
	if accepted {
		a.publishReportedState(state, owner, received)
	}
	response := protocol.ReportResponse{Accepted: accepted, Reason: reason, Capabilities: protocol.ReportCapabilities{AgentVersionReport: true, LinuxMetricsReport: true, CountryCodeReport: true, AgentUpgrade: true, InteractiveControl: true, GoogleStatus: true, Security: true, ManagementReport: true, NetworkCountersReport: true}}
	response.Capabilities.NetworkQuality = true
	writeJSON(w, http.StatusOK, a.qualityReportResponse(r, response, authenticatedID, report))
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
	googleCapable := false
	currentAgent, snapshotErr := a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
	if snapshotErr != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read agent")
		return
	}
	if currentAgent.State != nil && (currentAgent.State.Epoch != request.AgentEpoch || currentAgent.State.SessionID != request.SessionID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for _, probeType := range request.SupportedProbeTypes {
		if probeType == protocol.ProbeTypeGoogleStatus {
			googleCapable = true
			break
		}
	}
	negotiated, err := a.store.NegotiateGoogleStatusCapability(r.Context(), agentID, googleCapable, request.AgentEpoch, request.SessionID, a.now())
	if err != nil {
		a.logger.Error("record Google Status capability", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not claim job")
		return
	}
	if negotiated && googleCapable && currentAgent.Online {
		created, err := a.store.EnsureGoogleStatusJob(r.Context(), agentID, a.now())
		if err != nil && !errors.Is(err, storage.ErrGoogleStatusPending) && !errors.Is(err, storage.ErrOutstandingJobsFull) && !errors.Is(err, storage.ErrGoogleStatusUnsupported) {
			a.logger.Error("schedule Google Status", "agent_id", agentID, "error", err)
			writeJobError(w, http.StatusInternalServerError, "internal_error", "could not claim job")
			return
		}
		if created {
			_ = a.publishAgentDetail(r.Context(), agentID)
		}
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
	if job.ProbeType == protocol.ProbeTypeGoogleStatus && !ack.Duplicate {
		if err := a.publishAgentDetail(r.Context(), agentID); err != nil {
			a.logger.Warn("publish Google Status Web event", "agent_id", agentID, "error", err)
		}
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
		if errors.Is(err, storage.ErrAgentRevoked) {
			writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
			return "", false
		}
		if errors.Is(err, storage.ErrAgentDisabled) {
			writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
			return "", false
		}
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
	case errors.Is(err, storage.ErrAgentRevoked):
		writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
	case errors.Is(err, storage.ErrAgentDisabled):
		writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
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
	case errors.Is(err, storage.ErrAgentRevoked):
		writeJobError(w, http.StatusUnauthorized, "agent_revoked", "agent credential has been revoked")
	case errors.Is(err, storage.ErrAgentDisabled):
		writeJobError(w, http.StatusLocked, "agent_disabled", "agent has been disabled")
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
	return a.publishReportedState(state, trafficOwner{}, time.Time{})
}

func (a *App) publishReportedState(state storage.State, owner trafficOwner, received time.Time) bool {
	a.mu.Lock()
	current, exists := a.states[state.AgentID]
	if exists && !stateAfter(state, current) {
		a.mu.Unlock()
		return false
	}
	a.states[state.AgentID] = state
	if a.traffic != nil {
		a.traffic.append(owner, state, received, a.now())
	}
	payload, _ := json.Marshal(newWebAgentEventView(state))
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

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	if _, err := parseControlQuery(r); err != nil {
		writeControlCollectionError(w, err)
		return
	}
	session := webSessionFromContext(r.Context())
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	setWebNoStore(w)
	w.Header().Set("Connection", "keep-alive")
	ch, ok := a.hub.subscribe()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "too many event subscribers")
		return
	}
	defer a.hub.unsubscribe(ch)
	if err := writeSSEFrame(w, controller, []byte("retry: 3000\n\n"), a.now(), sseFrameWriteTimeout); err != nil {
		return
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	var sessionTimer *time.Timer
	var sessionExpiry <-chan time.Time
	if session != nil && a.webAuth != nil {
		deadline := a.webAuth.sessionDeadline(session)
		delay := deadline.Sub(a.now())
		if delay < 0 {
			delay = 0
		}
		sessionTimer = time.NewTimer(delay)
		sessionExpiry = sessionTimer.C
		defer sessionTimer.Stop()
	}
	for {
		select {
		case <-a.shutdown.Done():
			return
		case <-r.Context().Done():
			return
		case <-sessionExpiry:
			deadline, active := a.webAuth.sessionStillActive(session, a.now())
			if !active {
				return
			}
			delay := deadline.Sub(a.now())
			if delay < 0 {
				delay = 0
			}
			sessionTimer.Reset(delay)
		case <-sessionDone(session):
			return
		case message := <-ch:
			if session != nil && !a.webAuth.sessionOriginAllowed(r.Context(), session) {
				a.webAuth.revokeSession(session)
				return
			}
			frame := fmt.Appendf(nil, "event: agent\ndata: %s\n\n", message)
			if err := writeSSEFrame(w, controller, frame, a.now(), sseFrameWriteTimeout); err != nil {
				return
			}
		case <-keepalive.C:
			if session != nil && !a.webAuth.sessionOriginAllowed(r.Context(), session) {
				a.webAuth.revokeSession(session)
				return
			}
			if err := writeSSEFrame(w, controller, []byte(": keepalive\n\n"), a.now(), sseFrameWriteTimeout); err != nil {
				return
			}
		}
	}
}

func writeSSEFrame(w http.ResponseWriter, controller *http.ResponseController, frame []byte, now time.Time, timeout time.Duration) error {
	if err := controller.SetWriteDeadline(now.Add(timeout)); err != nil {
		return fmt.Errorf("set SSE write deadline: %w", err)
	}
	written, err := w.Write(frame)
	if err != nil {
		return fmt.Errorf("write SSE frame: %w", err)
	}
	if written != len(frame) {
		return io.ErrShortWrite
	}
	if err := controller.Flush(); err != nil {
		return fmt.Errorf("flush SSE frame: %w", err)
	}
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear SSE write deadline: %w", err)
	}
	return nil
}

func sessionDone(session *webSession) <-chan struct{} {
	if session == nil {
		return nil
	}
	return session.done
}

func (a *App) Shutdown() {
	if a.webAuth != nil {
		a.webAuth.shutdown()
	}
	a.cancel()
}

func (a *App) CleanupLoop() {
	if a.removalReceiptCleanupInterval <= 0 {
		a.removalReceiptCleanupInterval = 5 * time.Minute
	}
	if a.walHealthInterval <= 0 {
		a.walHealthInterval = sqliteWALHealthInterval
	}
	cleanupTicker := time.NewTicker(a.removalReceiptCleanupInterval)
	historyTicker := time.NewTicker(time.Hour)
	walHealthTicker := time.NewTicker(a.walHealthInterval)
	qualityTicker := time.NewTicker(5 * time.Minute)
	defer qualityTicker.Stop()
	defer cleanupTicker.Stop()
	defer historyTicker.Stop()
	defer walHealthTicker.Stop()
	a.cleanupExpiredRemovalReceipts()
	a.cleanupPlanRenewalRequests()
	for {
		select {
		case <-a.shutdown.Done():
			return
		case <-cleanupTicker.C:
			a.cleanupExpiredRemovalReceipts()
			a.cleanupPlanRenewalRequests()
		case <-historyTicker.C:
			a.cleanupScheduledProbeJobs()
			if err := a.store.CleanupHistory(a.shutdown, a.now().Add(-30*24*time.Hour)); err != nil && a.shutdown.Err() == nil {
				a.logger.Error("clean history", "error", err)
			}
			if err := a.store.CleanupSecurityHistory(a.shutdown, a.now().Add(-storage.SecurityRetention)); err != nil && a.shutdown.Err() == nil {
				a.logger.Error("clean security history", "error", err)
			}
		case <-walHealthTicker.C:
			a.checkWALHealth()
		case <-qualityTicker.C:
			a.cleanupQuality()
		}
	}
}

func (a *App) cleanupExpiredRemovalReceipts() {
	if _, err := a.store.PruneExpiredAgentRemovalReceipts(a.shutdown, a.now()); err != nil && a.shutdown.Err() == nil {
		a.logger.Error("clean expired Agent removal receipts", "error", err)
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
