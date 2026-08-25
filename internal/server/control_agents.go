package server

import (
	"errors"
	"net/http"

	"404-probe/internal/storage"
)

const (
	controlAgentPathPrefix     = "/api/v1/control/agents/"
	controlAgentCursorResource = "agents"
)

type controlAgentSummaryView struct {
	AgentID   string `json:"agent_id"`
	Name      string `json:"name"`
	Revoked   bool   `json:"revoked"`
	CreatedAt int64  `json:"created_at"`
	Online    bool   `json:"online"`
	LastSeen  *int64 `json:"last_seen"`
}

type controlAgentDetailView struct {
	controlAgentSummaryView
	State *controlAgentStateView `json:"state"`
}

type controlAgentStateView struct {
	Hostname    string  `json:"hostname"`
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	Uptime      uint64  `json:"uptime"`
	CPUPercent  float64 `json:"cpu_percent"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	RAMUsed     uint64  `json:"ram_used"`
	RAMTotal    uint64  `json:"ram_total"`
	RAMPercent  float64 `json:"ram_percent"`
	SwapUsed    uint64  `json:"swap_used"`
	SwapTotal   uint64  `json:"swap_total"`
	SwapPercent float64 `json:"swap_percent"`
	DiskUsed    uint64  `json:"disk_used"`
	DiskTotal   uint64  `json:"disk_total"`
	DiskPercent float64 `json:"disk_percent"`
	RXRate      float64 `json:"rx_rate"`
	TXRate      float64 `json:"tx_rate"`
	RXTotal     uint64  `json:"rx_total"`
	TXTotal     uint64  `json:"tx_total"`
	CollectedAt int64   `json:"collected_at"`
}

type controlAgentCollectionView struct {
	Items      []controlAgentSummaryView `json:"items"`
	NextCursor *string                   `json:"next_cursor"`
}

func (a *App) handleGetControlAgents(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	values, err := parseControlQuery(r, "status", "limit", "cursor")
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	status := storage.AgentQueryStatus(values.Get("status"))
	switch status {
	case "", storage.AgentQueryStatusOnline, storage.AgentQueryStatusOffline, storage.AgentQueryStatusRevoked:
	default:
		writeControlCollectionError(w, errInvalidControlQuery)
		return
	}
	filters := controlFilterFingerprint(values, "status")
	limit, after, err := parseControlCollectionPage(values, controlAgentCursorResource, filters, validControlAgentID)
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	records, next, err := a.store.QueryAgents(r.Context(), storage.AgentQuery{
		Status: status, After: after, Limit: limit, Now: a.now(), OfflineTimeout: a.offlineTimeout,
	})
	if err != nil {
		a.logger.Error("query control agents", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query agents")
		return
	}
	items := make([]controlAgentSummaryView, 0, len(records))
	for _, record := range records {
		items = append(items, newControlAgentSummaryView(record))
	}
	nextCursor, err := encodeControlCursor(controlAgentCursorResource, filters, next)
	if err != nil {
		a.logger.Error("encode control agents cursor", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query agents")
		return
	}
	writeJSON(w, http.StatusOK, controlAgentCollectionView{Items: items, NextCursor: nextCursor})
}

func (a *App) handleGetControlAgent(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	agentID := r.PathValue("agent_id")
	if !validControlAgentRequestPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	if _, err := parseControlQuery(r); err != nil {
		writeControlCollectionError(w, err)
		return
	}
	record, err := a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout)
	if err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
			return
		}
		a.logger.Error("read control agent", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read agent")
		return
	}
	view := controlAgentDetailView{controlAgentSummaryView: newControlAgentSummaryView(record)}
	if record.State != nil {
		view.State = newControlAgentStateView(*record.State)
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) handleControlAgentMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	if !validControlAgentRequestPath(r, r.PathValue("agent_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func (a *App) handleControlAgentCollectionMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setControlNoStore(w)
	if !a.authenticateControlRequest(w, r) {
		return
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func validControlAgentRequestPath(r *http.Request, agentID string) bool {
	return validControlAgentID(agentID) && r.URL.EscapedPath() == controlAgentPathPrefix+agentID
}

func validControlAgentID(value string) bool {
	return validLowerHexID(value, 32)
}

func newControlAgentSummaryView(record storage.AgentSnapshot) controlAgentSummaryView {
	view := controlAgentSummaryView{
		AgentID: record.Agent.ID, Name: record.Agent.Name, Revoked: record.Agent.Revoked,
		CreatedAt: record.Agent.CreatedAt, Online: record.Online,
	}
	if record.State != nil {
		lastSeen := record.State.LastSeen
		view.LastSeen = &lastSeen
	}
	return view
}

func newControlAgentStateView(state storage.State) *controlAgentStateView {
	return &controlAgentStateView{
		Hostname: state.Hostname, OS: state.OS, Arch: state.Arch, Uptime: state.Uptime,
		CPUPercent: state.CPUPercent, Load1: state.Load1, Load5: state.Load5, Load15: state.Load15,
		RAMUsed: state.RAMUsed, RAMTotal: state.RAMTotal, RAMPercent: state.RAMPercent,
		SwapUsed: state.SwapUsed, SwapTotal: state.SwapTotal, SwapPercent: state.SwapPercent,
		DiskUsed: state.DiskUsed, DiskTotal: state.DiskTotal, DiskPercent: state.DiskPercent,
		RXRate: state.RXRate, TXRate: state.TXRate, RXTotal: state.RXTotal, TXTotal: state.TXTotal,
		CollectedAt: state.CollectedAt,
	}
}
