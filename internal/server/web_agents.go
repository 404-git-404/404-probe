package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	webAgentPathPrefix         = "/api/v1/web/agents/"
	webAgentCursorResource     = "web_agents"
	outboundSnapshotStaleAfter = 2 * time.Minute
)

type webAgentSummaryView struct {
	AgentID        string `json:"agent_id"`
	Name           string `json:"name"`
	Revoked        bool   `json:"revoked"`
	DisabledAt     *int64 `json:"disabled_at"`
	CreatedAt      int64  `json:"created_at"`
	Online         bool   `json:"online"`
	LastSeen       *int64 `json:"last_seen"`
	Version        string `json:"version"`
	UpgradeCapable bool   `json:"upgrade_capable"`
}

type webAgentDetailView struct {
	webAgentSummaryView
	CountryCode   string              `json:"country_code,omitempty"`
	CountrySource string              `json:"country_source,omitempty"`
	State         *webAgentStateView  `json:"state"`
	Outbounds     webOutboundsView    `json:"outbounds"`
	GoogleStatus  webGoogleStatusView `json:"google_status"`
	Security      webSecurityView     `json:"security"`
	Plan          *storage.AgentPlan  `json:"plan,omitempty"`
}

type webSecurityView struct {
	Supported           bool                    `json:"supported"`
	Status              protocol.SecurityStatus `json:"status"`
	Reason              string                  `json:"reason,omitempty"`
	Stale               bool                    `json:"stale"`
	UpdatedAt           *int64                  `json:"updated_at,omitempty"`
	Current             *protocol.SecurityBatch `json:"current,omitempty"`
	DeliveryGap         bool                    `json:"delivery_gap,omitempty"`
	PreviousCollectedAt int64                   `json:"previous_collected_at,omitempty"`
}

type webGoogleStatusView struct {
	Supported bool                         `json:"supported"`
	Pending   bool                         `json:"pending"`
	Stale     bool                         `json:"stale"`
	Result    *protocol.GoogleStatusResult `json:"result,omitempty"`
	CheckedAt *int64                       `json:"checked_at,omitempty"`
}

type webOutboundsView struct {
	Configured  bool                        `json:"configured"`
	Available   bool                        `json:"available"`
	Status      protocol.OutboundStatus     `json:"status"`
	Stale       bool                        `json:"stale"`
	OrderSource string                      `json:"order_source,omitempty"`
	Selectors   []protocol.OutboundSelector `json:"selectors"`
	CheckedAt   *int64                      `json:"checked_at"`
	UpdatedAt   *int64                      `json:"updated_at"`
}

type webAgentStateView struct {
	Stale           bool     `json:"stale"`
	Hostname        string   `json:"hostname"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Uptime          uint64   `json:"uptime"`
	CPUPercent      float64  `json:"cpu_percent"`
	CPUStealPercent *float64 `json:"cpu_steal_percent,omitempty"`
	CPUCores        uint32   `json:"cpu_cores,omitempty"`
	Load1           float64  `json:"load1"`
	Load5           float64  `json:"load5"`
	Load15          float64  `json:"load15"`
	RAMUsed         uint64   `json:"ram_used"`
	RAMTotal        uint64   `json:"ram_total"`
	RAMPercent      float64  `json:"ram_percent"`
	SwapUsed        uint64   `json:"swap_used"`
	SwapTotal       uint64   `json:"swap_total"`
	SwapPercent     float64  `json:"swap_percent"`
	DiskUsed        uint64   `json:"disk_used"`
	DiskTotal       uint64   `json:"disk_total"`
	DiskPercent     float64  `json:"disk_percent"`
	DiskReadRate    *float64 `json:"disk_read_rate,omitempty"`
	DiskWriteRate   *float64 `json:"disk_write_rate,omitempty"`
	DiskBusyPercent *float64 `json:"disk_busy_percent,omitempty"`
	RXRate          float64  `json:"rx_rate"`
	TXRate          float64  `json:"tx_rate"`
	RXTotal         uint64   `json:"rx_total"`
	TXTotal         uint64   `json:"tx_total"`
	CollectedAt     int64    `json:"collected_at"`
}

type webAgentCollectionView struct {
	Items      []webAgentSummaryView `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}

type webAgentHistoryView struct {
	AgentID  string                     `json:"agent_id"`
	Hours    int                        `json:"hours"`
	Points   []webAgentHistoryPointView `json:"points"`
	Security []webSecurityRecordView    `json:"security"`
}

type webSecurityRecordView struct {
	protocol.SecurityBatch
	DeliveryGap         bool  `json:"delivery_gap,omitempty"`
	PreviousCollectedAt int64 `json:"previous_collected_at,omitempty"`
}

type webAgentHistoryPointView struct {
	Timestamp   int64   `json:"timestamp"`
	CPU         float64 `json:"cpu"`
	RAMPercent  float64 `json:"ram_percent"`
	SwapPercent float64 `json:"swap_percent"`
	DiskPercent float64 `json:"disk_percent"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	RXRate      float64 `json:"rx_rate"`
	TXRate      float64 `json:"tx_rate"`
	RXTotal     uint64  `json:"rx_total"`
	TXTotal     uint64  `json:"tx_total"`
}

type webAgentEventView struct {
	AgentID  string             `json:"agent_id"`
	Name     string             `json:"name"`
	Online   bool               `json:"online"`
	LastSeen int64              `json:"last_seen"`
	State    *webAgentStateView `json:"state"`
}

func (a *App) handleGetWebAgents(w http.ResponseWriter, r *http.Request) {
	values, err := parseControlQuery(r, "status", "limit", "cursor")
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	status := storage.AgentQueryStatus(values.Get("status"))
	switch status {
	case "", storage.AgentQueryStatusOnline, storage.AgentQueryStatusOffline, storage.AgentQueryStatusDisabled, storage.AgentQueryStatusRevoked:
	default:
		writeControlCollectionError(w, errInvalidControlQuery)
		return
	}
	filters := controlFilterFingerprint(values, "status")
	queryStatus := status
	if queryStatus == "" {
		queryStatus = storage.AgentQueryStatusActive
	}
	limit, after, err := parseControlCollectionPage(values, webAgentCursorResource, filters, validWebAgentID)
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	records, next, err := a.store.QueryAgents(r.Context(), storage.AgentQuery{
		Status: queryStatus, After: after, Limit: limit, Now: a.now(), OfflineTimeout: a.offlineTimeout,
	})
	if err != nil {
		a.logger.Error("query Web agents", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query agents")
		return
	}
	items := make([]webAgentSummaryView, 0, len(records))
	for _, record := range records {
		items = append(items, newWebAgentSummaryView(record))
	}
	nextCursor, err := encodeControlCursor(webAgentCursorResource, filters, next)
	if err != nil {
		a.logger.Error("encode Web agents cursor", "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not query agents")
		return
	}
	writeJSON(w, http.StatusOK, webAgentCollectionView{Items: items, NextCursor: nextCursor})
}

func (a *App) handleGetWebAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentRequestPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	if _, err := parseControlQuery(r); err != nil {
		writeControlCollectionError(w, err)
		return
	}
	view, err := a.webAgentDetail(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
			return
		}
		a.logger.Error("read Web agent", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read agent")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) webAgentDetail(ctx context.Context, agentID string) (webAgentDetailView, error) {
	record, err := a.store.GetAgentSnapshot(ctx, agentID, a.now(), a.offlineTimeout)
	if err != nil {
		return webAgentDetailView{}, err
	}
	view := webAgentDetailView{webAgentSummaryView: newWebAgentSummaryView(record), Outbounds: webOutboundsView{Selectors: []protocol.OutboundSelector{}}}
	if record.State != nil {
		view.State = newWebAgentStateView(*record.State, record.StateStale)
	}
	plan, planExists, err := a.store.GetAgentPlan(ctx, agentID)
	if err != nil {
		return webAgentDetailView{}, err
	}
	if planExists {
		plan, err = storage.AgentPlanAt(plan, a.now())
		if err != nil {
			return webAgentDetailView{}, err
		}
		view.Plan = &plan
	}
	if plan.CountryCodeOverride != "" {
		view.CountryCode, view.CountrySource = plan.CountryCodeOverride, "manual"
	} else if record.State != nil && record.State.CountryCode != "" {
		view.CountryCode, view.CountrySource = record.State.CountryCode, "automatic"
	}
	snapshot, configured, err := a.store.GetOutboundSnapshot(ctx, agentID)
	if err != nil {
		return webAgentDetailView{}, err
	}
	if configured {
		checkedAt := snapshot.CheckedAt
		stale := a.now().Sub(time.UnixMilli(snapshot.CheckedAt)) > outboundSnapshotStaleAfter
		view.Outbounds = webOutboundsView{Configured: true, Available: snapshot.Available, Status: snapshot.Status, Stale: stale, OrderSource: snapshot.OrderSource, Selectors: snapshot.Selectors, CheckedAt: &checkedAt, UpdatedAt: snapshot.UpdatedAt}
	}
	supported, err := a.store.GoogleStatusCapability(ctx, agentID)
	if err != nil {
		return webAgentDetailView{}, err
	}
	pending, err := a.store.GoogleStatusPending(ctx, agentID, a.now())
	if err != nil {
		return webAgentDetailView{}, err
	}
	view.GoogleStatus = webGoogleStatusView{Supported: supported, Pending: pending, Stale: record.Agent.Revoked || record.Agent.DisabledAt != nil || !record.Online}
	google, exists, err := a.store.GetGoogleStatus(ctx, agentID)
	if err != nil {
		return webAgentDetailView{}, err
	}
	if exists {
		checkedAt := google.CheckedAt
		view.GoogleStatus.Result = &google.Result
		view.GoogleStatus.CheckedAt = &checkedAt
	}
	capability, supported, err := a.store.SecurityCapability(ctx, agentID)
	if err != nil {
		return webAgentDetailView{}, err
	}
	view.Security = webSecurityView{Supported: supported && capability.Supported, Status: protocol.SecurityStatusUnavailable,
		Stale: record.Agent.Revoked || record.Agent.DisabledAt != nil || !record.Online}
	if view.Security.Supported && record.State != nil && capability.Epoch == record.State.Epoch && capability.SessionID == record.State.SessionID {
		updated := capability.UpdatedAt
		view.Security.Status, view.Security.Reason, view.Security.UpdatedAt = capability.Status, capability.Reason, &updated
		current, err := a.store.CurrentSecurity(ctx, agentID, capability, a.now())
		if err != nil {
			return webAgentDetailView{}, err
		}
		if current != nil {
			batch := current.Batch
			view.Security.Current = &batch
			view.Security.DeliveryGap = current.DeliveryGap
			view.Security.PreviousCollectedAt = current.PreviousCollectedAt
			view.Security.Stale = view.Security.Stale || protocol.SecurityStale(batch.CollectedAt, a.now())
		}
	}
	return view, nil
}

func (a *App) publishAgentDetail(ctx context.Context, agentID string) error {
	view, err := a.webAgentDetail(ctx, agentID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(view)
	if err != nil {
		return err
	}
	a.hub.publish(payload)
	return nil
}

func (a *App) handleGetWebAgentHistory(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if !validWebAgentHistoryRequestPath(r, agentID) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	values, err := parseControlQuery(r, "hours")
	if err != nil {
		writeControlCollectionError(w, err)
		return
	}
	hours := 24
	if value := values.Get("hours"); value != "" {
		hours, err = strconv.Atoi(value)
		if err != nil || hours < 1 || hours > 24*30 {
			writeJobError(w, http.StatusBadRequest, "invalid_query", "hours must be between 1 and 720")
			return
		}
	}
	if _, err := a.store.GetAgentSnapshot(r.Context(), agentID, a.now(), a.offlineTimeout); err != nil {
		if errors.Is(err, storage.ErrAgentNotFound) {
			writeJobError(w, http.StatusNotFound, "agent_not_found", "agent not found")
			return
		}
		a.logger.Error("read Web agent for history", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read agent history")
		return
	}
	points, err := a.store.History(r.Context(), agentID, a.now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		a.logger.Error("read Web agent history", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read agent history")
		return
	}
	views := make([]webAgentHistoryPointView, 0, len(points))
	for _, point := range points {
		views = append(views, newWebAgentHistoryPointView(point))
	}
	securityRecords, err := a.store.SecurityHistory(r.Context(), agentID, a.now().Add(-storage.SecurityRetention), 31, a.now())
	if err != nil {
		a.logger.Error("read Web agent security history", "agent_id", agentID, "error", err)
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not read agent history")
		return
	}
	security := make([]webSecurityRecordView, 0, len(securityRecords))
	for _, record := range securityRecords {
		security = append(security, webSecurityRecordView{SecurityBatch: record.Batch, DeliveryGap: record.DeliveryGap, PreviousCollectedAt: record.PreviousCollectedAt})
	}
	writeJSON(w, http.StatusOK, webAgentHistoryView{AgentID: agentID, Hours: hours, Points: views, Security: security})
}

func (a *App) handleWebAgentCollectionMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func (a *App) handleWebAgentMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebAgentRequestPath(r, r.PathValue("agent_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func (a *App) handleWebAgentHistoryMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if !validWebAgentHistoryRequestPath(r, r.PathValue("agent_id")) {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid agent ID")
		return
	}
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func (a *App) handleWebEventsMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	writeJobError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
}

func validWebAgentRequestPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID
}

func validWebAgentHistoryRequestPath(r *http.Request, agentID string) bool {
	return validWebAgentID(agentID) && r.URL.EscapedPath() == webAgentPathPrefix+agentID+"/history"
}

func validWebAgentID(value string) bool { return validLowerHexID(value, 32) }

func newWebAgentSummaryView(record storage.AgentSnapshot) webAgentSummaryView {
	view := webAgentSummaryView{
		AgentID: record.Agent.ID, Name: record.Agent.Name, Revoked: record.Agent.Revoked,
		DisabledAt: record.Agent.DisabledAt, CreatedAt: record.Agent.CreatedAt, Online: record.Online, Version: "unknown",
	}
	if record.State != nil {
		lastSeen := record.State.LastSeen
		view.LastSeen = &lastSeen
		view.Version = record.State.AgentVersion
		view.UpgradeCapable = record.State.AgentUpgradeCapable
	}
	return view
}

type webVersionView struct {
	Version         string `json:"version"`
	Commit          string `json:"commit,omitempty"`
	Dirty           bool   `json:"dirty"`
	UpgradeEligible bool   `json:"upgrade_eligible"`
}

func (a *App) handleGetWebVersion(w http.ResponseWriter, _ *http.Request) {
	info := a.buildInfo
	writeJSON(w, http.StatusOK, webVersionView{Version: info.Version, Commit: info.Commit, Dirty: info.Dirty, UpgradeEligible: info.UpgradeEligible()})
}

func newWebAgentStateView(state storage.State, stale bool) *webAgentStateView {
	view := &webAgentStateView{
		Stale:    stale,
		Hostname: state.Hostname, OS: state.OS, Arch: state.Arch, Uptime: state.Uptime,
		CPUPercent: state.CPUPercent, CPUStealPercent: state.CPUStealPercent, CPUCores: state.CPUCores, Load1: state.Load1, Load5: state.Load5, Load15: state.Load15,
		RAMUsed: state.RAMUsed, RAMTotal: state.RAMTotal, RAMPercent: state.RAMPercent,
		SwapUsed: state.SwapUsed, SwapTotal: state.SwapTotal, SwapPercent: state.SwapPercent,
		DiskUsed: state.DiskUsed, DiskTotal: state.DiskTotal, DiskPercent: state.DiskPercent,
		DiskReadRate: state.DiskReadRate, DiskWriteRate: state.DiskWriteRate, DiskBusyPercent: state.DiskBusyPercent,
		RXRate: state.RXRate, TXRate: state.TXRate, RXTotal: state.RXTotal, TXTotal: state.TXTotal,
		CollectedAt: state.CollectedAt,
	}
	if stale {
		view.RXRate = 0
		view.TXRate = 0
		view.CPUStealPercent = nil
		view.DiskReadRate = nil
		view.DiskWriteRate = nil
		view.DiskBusyPercent = nil
	}
	return view
}

func newWebAgentEventView(state storage.State) webAgentEventView {
	return webAgentEventView{
		AgentID: state.AgentID, Name: state.Name, Online: true, LastSeen: state.LastSeen,
		State: newWebAgentStateView(state, false),
	}
}

func newWebAgentHistoryPointView(point storage.HistoryPoint) webAgentHistoryPointView {
	return webAgentHistoryPointView{
		Timestamp: point.Timestamp, CPU: point.CPU, RAMPercent: point.RAMPercent,
		SwapPercent: point.SwapPercent, DiskPercent: point.DiskPercent,
		Load1: point.Load1, Load5: point.Load5, Load15: point.Load15,
		RXRate: point.RXRate, TXRate: point.TXRate, RXTotal: point.RXTotal, TXTotal: point.TXTotal,
	}
}
