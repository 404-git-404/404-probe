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

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
	"404-probe/web"
)

type App struct {
	store          *storage.Store
	offlineTimeout time.Duration
	logger         *slog.Logger
	now            func() time.Time
	shutdown       context.Context
	cancel         context.CancelFunc
	mu             sync.RWMutex
	states         map[string]storage.State
	hub            *hub
	handler        http.Handler
}

const maxSSESubscribers = 64

type agentView struct {
	storage.State
	Online bool `json:"online"`
}

func NewApp(store *storage.Store, offlineTimeout time.Duration, logger *slog.Logger) (*App, error) {
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
	mux.HandleFunc("GET /api/v1/agents", a.handleAgents)
	mux.HandleFunc("GET /api/v1/agents/{id}/history", a.handleHistory)
	mux.HandleFunc("GET /api/v1/events", a.handleEvents)
	static, err := fs.Sub(web.Files, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(static)))
	return securityHeaders(mux)
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
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
