package server

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

type dashboardResources struct {
	Known       bool     `json:"known"`
	Stale       bool     `json:"stale"`
	CPU         *float64 `json:"cpu_percent"`
	RAM         *float64 `json:"ram_percent"`
	Disk        *float64 `json:"disk_percent"`
	CollectedAt int64    `json:"collected_at"`
	ReceivedAt  int64    `json:"received_at"`
	MaxAgeMS    int64    `json:"max_age_ms"`
}
type dashboardRenewal struct {
	Days     *int   `json:"days"`
	Type     string `json:"type"`
	Date     string `json:"date"`
	Field    string `json:"field"`
	Timezone string `json:"timezone"`
}
type dashboardTraffic struct {
	Known        bool   `json:"known"`
	Stale        bool   `json:"stale"`
	CycleCurrent bool   `json:"cycle_current"`
	QuotaState   string `json:"quota_state"`
}
type dashboardNetwork struct {
	Active    *bool `json:"active"`
	Known     bool  `json:"known"`
	Monitored bool  `json:"monitored"`
	CheckedAt int64 `json:"checked_at"`
	ExpiresAt int64 `json:"expires_at"`
}
type dashboardNetworkEntry struct {
	Value      dashboardNetwork
	Expires    time.Time
	Generation uint64
}
type dashboardSummary struct {
	webAgentSummaryView
	Lifecycle            string                           `json:"lifecycle"`
	Hostname             string                           `json:"hostname"`
	CountryCode          string                           `json:"country_code"`
	CountrySource        string                           `json:"country_source"`
	GroupID              *string                          `json:"group_id"`
	TagIDs               []string                         `json:"tag_ids"`
	OrganizationRevision int64                            `json:"organization_revision"`
	Resources            dashboardResources               `json:"resources"`
	State                *webAgentStateView               `json:"state"`
	Plan                 *storage.AgentPlan               `json:"plan"`
	Traffic              dashboardTraffic                 `json:"traffic"`
	Renewal              dashboardRenewal                 `json:"renewal"`
	Network              dashboardNetwork                 `json:"network"`
	GoogleStatus         webGoogleStatusView              `json:"google_status"`
	Outbounds            webOutboundsView                 `json:"outbounds"`
	AccumulatedTraffic   *storage.TrafficTotals           `json:"accumulated_traffic"`
	MaskedIPs            []string                         `json:"masked_ips"`
	MaskedEgress         map[string]dashboardMaskedEgress `json:"masked_egress"`
	SearchFields         []string                         `json:"search_fields"`
}

type dashboardMaskedEgress struct {
	IP          string                  `json:"ip"`
	Status      string                  `json:"status"`
	CheckedAt   int64                   `json:"checked_at"`
	ValidUntil  int64                   `json:"valid_until"`
	Reason      string                  `json:"reason"`
	LastSuccess *protocol.EgressSuccess `json:"last_success,omitempty"`
}
type dashboardResponse struct {
	Partial       bool                   `json:"partial"`
	Total         int                    `json:"total"`
	Expenses      expenseTotals          `json:"expenses"`
	ServerNow     int64                  `json:"server_now"`
	Revision      int64                  `json:"revision"`
	Groups        []storage.AgentGroup   `json:"groups"`
	Tags          []storage.AgentTag     `json:"tags"`
	ExchangeRates []storage.ExchangeRate `json:"exchange_rates"`
	Items         []dashboardSummary     `json:"items"`
}

// Opaque equality token only: internal epoch/session/sequence never leave the
// Server, and clients must still pass AgentState's existing arrival barriers.
func dashboardVersion(agent storage.Agent, state *storage.State) string {
	var epoch, sequence uint64
	var session string
	var seen int64
	if state != nil {
		epoch, sequence, session, seen = state.Epoch, state.Sequence, state.SessionID, state.LastSeen
	}
	value, _ := json.Marshal(struct {
		ID, Name        string
		Revoked         bool
		DisabledAt      *int64
		Epoch, Sequence uint64
		Session         string
		Seen            int64
	}{agent.ID, agent.Name, agent.Revoked, agent.DisabledAt, epoch, sequence, session, seen})
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func renewalSummary(plan *storage.AgentPlan, now time.Time) dashboardRenewal {
	out := dashboardRenewal{Type: "missing", Timezone: "UTC"}
	if plan == nil {
		return out
	}
	if plan.Timezone != "" {
		out.Timezone = plan.Timezone
	}
	out.Date, out.Field = plan.RenewalDate, "renewal_date"
	if out.Date == "" {
		out.Date, out.Field = plan.ExpiryDate, "expiry_date"
	}
	if out.Date == "" {
		out.Field = ""
		return out
	}
	if out.Timezone == "Local" {
		out.Type = "invalid"
		return out
	}
	d, err := time.Parse("2006-01-02", out.Date)
	if err != nil || d.Year() < 1 || d.Year() > 9999 {
		out.Type = "invalid"
		return out
	}
	loc, err := time.LoadLocation(out.Timezone)
	if err != nil {
		out.Type = "invalid"
		return out
	}
	today := now.In(loc)
	midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	// Unix-day subtraction avoids DST and time.Duration's ~290-year overflow.
	days := int(d.Unix()/86400 - midnight.Unix()/86400)
	out.Days = &days
	switch {
	case days < 0:
		out.Type = "overdue"
	case days <= 7:
		out.Type = "due_soon"
	default:
		out.Type = "upcoming"
	}
	return out
}

func maskDashboardIP(text string) string {
	ip, err := netip.ParseAddr(text)
	if err != nil || ip.Zone() != "" {
		return ""
	}
	if ip.Is4() {
		v := ip.As4()
		return fmt.Sprintf("%d.%d.*.*", v[0], v[1])
	}
	v := ip.As16()
	return fmt.Sprintf("%x:%x:…", uint16(v[0])<<8|uint16(v[1]), uint16(v[2])<<8|uint16(v[3]))
}

func (a *App) invalidateDashboardNetwork(id string) {
	a.dashboardMu.Lock()
	defer a.dashboardMu.Unlock()
	if a.dashboardNetwork == nil {
		a.dashboardNetwork = map[string]dashboardNetworkEntry{}
	}
	entry := a.dashboardNetwork[id]
	a.dashboardGeneration++
	entry.Generation = a.dashboardGeneration
	entry.Expires = time.Time{}
	a.dashboardNetwork[id] = entry
}
func (a *App) cachedDashboardNetwork(r *http.Request, id string, now time.Time) dashboardNetwork {
	return a.dashboardNetworkValue(r.Context(), id, now, true)
}

func (a *App) dashboardNetworkValue(ctx context.Context, id string, now time.Time, cacheFailure bool) dashboardNetwork {
	a.dashboardMu.Lock()
	entry := a.dashboardNetwork[id]
	if now.Before(entry.Expires) {
		a.dashboardMu.Unlock()
		return entry.Value
	}
	generation := entry.Generation
	cacheReset := a.dashboardResets
	a.dashboardMu.Unlock()
	out := dashboardNetwork{CheckedAt: now.UnixMilli(), ExpiresAt: now.Add(5 * time.Second).UnixMilli()}
	gate, err := a.store.QualityAlertGate(ctx, id)
	if err == nil {
		out.Monitored = gate
		active := false
		if gate {
			active, err = a.store.QualityAlertActive(ctx, id, now)
		}
		if err == nil {
			out.Active = &active
			out.Known = true
		}
	}
	// A failed background prewarm must not seed a five-second unknown entry.
	if !out.Known && !cacheFailure {
		return out
	}
	a.dashboardMu.Lock()
	defer a.dashboardMu.Unlock()
	if a.dashboardNetwork == nil {
		a.dashboardNetwork = map[string]dashboardNetworkEntry{}
	}
	if len(a.dashboardNetwork) > 2048 {
		a.dashboardNetwork = map[string]dashboardNetworkEntry{}
		a.dashboardResets++
	}
	// Unrelated device invalidations must not invalidate this computation. A map
	// reset has its own epoch so an old computation cannot repopulate a new map.
	if a.dashboardResets != cacheReset || a.dashboardNetwork[id].Generation != generation {
		return dashboardNetwork{CheckedAt: now.UnixMilli()}
	}
	a.dashboardNetwork[id] = dashboardNetworkEntry{Value: out, Expires: now.Add(5 * time.Second), Generation: generation}
	return out
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	values, err := parseControlQuery(r, "ids", "include_revoked")
	if err != nil || r.URL.EscapedPath() != "/api/v1/web/dashboard" {
		writeJobError(w, 400, "invalid_request", "invalid dashboard query")
		return
	}
	includeRevoked := values.Get("include_revoked") == "true"
	if v := values.Get("include_revoked"); v != "" && v != "true" && v != "false" {
		writeJobError(w, 400, "invalid_request", "invalid revoked filter")
		return
	}
	var selected map[string]bool
	if raw, ok := values["ids"]; ok {
		selected = map[string]bool{}
		if len(raw) != 1 || raw[0] == "" {
			writeJobError(w, 400, "invalid_request", "ids must be a nonempty list")
			return
		}
		for _, id := range strings.Split(raw[0], ",") {
			if !validWebAgentID(id) || selected[id] || len(selected) >= 200 {
				writeJobError(w, 400, "invalid_request", "invalid dashboard ids")
				return
			}
			selected[id] = true
		}
	}
	now := a.now()
	data, err := a.store.ReadDashboardData(r.Context(), now, a.offlineTimeout, includeRevoked)
	if err != nil {
		a.organizationError(w, err)
		return
	}
	out := dashboardResponse{Partial: selected != nil, Total: len(data.Records), Expenses: estimateExpenses(data, nil).expenseTotals, ServerNow: now.UnixMilli(), Revision: data.Catalog.Revision, Groups: data.Catalog.Groups, Tags: data.Catalog.Tags, ExchangeRates: data.Catalog.Rates, Items: []dashboardSummary{}}
	for _, raw := range data.Records {
		id := raw.Snapshot.Agent.ID
		if selected != nil && !selected[id] {
			continue
		}
		delete(selected, id)
		record := a.withUpdaterHealth(raw.Snapshot)
		v := dashboardSummary{webAgentSummaryView: newWebAgentSummaryView(record), Lifecycle: "unknown", CountryCode: raw.CountryCode, CountrySource: raw.CountrySource, TagIDs: []string{}, OrganizationRevision: data.Catalog.Revision, Plan: raw.Plan, MaskedIPs: []string{}, SearchFields: []string{record.Agent.Name}, Renewal: renewalSummary(raw.Plan, now), Traffic: dashboardTraffic{QuotaState: "unconfigured", Stale: true}}
		switch {
		case record.Agent.Revoked:
			v.Lifecycle = "revoked"
		case record.Agent.DisabledAt != nil:
			v.Lifecycle = "paused"
		case record.Online:
			v.Lifecycle = "online"
		case record.State != nil:
			v.Lifecycle = "offline"
		}
		v.Resources = dashboardResources{Stale: true, MaxAgeMS: a.offlineTimeout.Milliseconds()}
		v.AccumulatedTraffic = raw.TrafficTotals
		v.Outbounds = webOutboundsView{Selectors: []protocol.OutboundSelector{}}
		if o := raw.Outbound; o != nil {
			checked := o.CheckedAt
			v.Outbounds = webOutboundsView{Configured: true, Available: o.Available, Status: o.Status, Stale: now.Sub(time.UnixMilli(o.CheckedAt)) > outboundSnapshotStaleAfter, OrderSource: o.OrderSource, Selectors: o.Selectors, CheckedAt: &checked, UpdatedAt: o.UpdatedAt}
		}
		v.GoogleStatus = webGoogleStatusView{Supported: raw.GoogleSupported, Pending: raw.GooglePending, Stale: !record.Online || record.Agent.DisabledAt != nil || record.Agent.Revoked}
		if g := raw.Google; g != nil {
			copy := *g
			v.GoogleStatus.Result, v.GoogleStatus.CheckedAt, v.GoogleStatus.NextDueAt = &copy.Result, &copy.CheckedAt, &copy.NextDueAt
			v.GoogleStatus.UnknownStreak = copy.UnknownStreak
			v.GoogleStatus.Stale = v.GoogleStatus.Stale || now.UnixMilli() >= copy.NextDueAt
			if yt := copy.Result.YouTube; yt.Status != protocol.YouTubeUnknown && !protocol.ValidCountryCode(yt.Region) {
				copy.Result.YouTube = protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: "invalid_region"}}
			}
		}
		if state := record.State; state != nil {
			v.Hostname = state.Hostname
			v.SearchFields = append(v.SearchFields, state.Hostname)
			stale := record.StateStale || state.CollectedAt <= 0 || state.CollectedAt < now.Add(-a.offlineTimeout).UnixMilli() || state.CollectedAt > now.Add(time.Minute).UnixMilli()
			v.Resources = dashboardResources{Known: true, Stale: stale, CPU: &state.CPUPercent, RAM: &state.RAMPercent, Disk: &state.DiskPercent, CollectedAt: state.CollectedAt, ReceivedAt: state.LastSeen, MaxAgeMS: a.offlineTimeout.Milliseconds()}
			copy := *state
			copy.PublicEgress = storage.EgressState{}
			v.State = newWebAgentStateView(copy, false, now)
			v.State.Stale = stale
			v.MaskedEgress = map[string]dashboardMaskedEgress{}
			for _, o := range []protocol.EgressObservation{state.PublicEgress.IPv4, state.PublicEgress.IPv6} {
				if mask := maskDashboardIP(o.IP); mask != "" && o.Status == "available" && o.CheckedAt > 0 && o.ValidUntil > now.UnixMilli() && !stale {
					entry := dashboardMaskedEgress{IP: mask, Status: o.Status, CheckedAt: o.CheckedAt, ValidUntil: o.ValidUntil, Reason: o.Reason}
					if o.LastSuccess != nil {
						last := *o.LastSuccess
						last.IP = maskDashboardIP(last.IP)
						if last.IP != "" {
							entry.LastSuccess = &last
						}
					}
					v.MaskedEgress[o.Family] = entry
					v.MaskedIPs = append(v.MaskedIPs, mask)
					v.SearchFields = append(v.SearchFields, mask)
				}
			}
		}
		if assignment, ok := data.Catalog.Assignments[id]; ok {
			v.GroupID, v.TagIDs = assignment.GroupID, assignment.TagIDs
		}
		if p := raw.Plan; p != nil {
			v.Traffic.Known = true
			v.Traffic.Stale = v.Resources.Stale
			v.Traffic.CycleCurrent = p.CycleStart != nil && p.CycleEnd != nil && *p.CycleEnd > *p.CycleStart && *p.CycleStart <= now.UnixMilli() && now.UnixMilli() < *p.CycleEnd
			switch {
			case p.TrafficMode == "":
				v.Traffic.QuotaState = "unconfigured"
			case p.QuotaBytes == nil:
				v.Traffic.QuotaState = "unlimited"
			case *p.QuotaBytes == 0:
				v.Traffic.QuotaState = "zero"
			case !v.Traffic.CycleCurrent:
				v.Traffic.QuotaState = "expired"
			default:
				v.Traffic.QuotaState = "current"
			}
		}
		if record.Agent.Revoked || record.Agent.DisabledAt != nil {
			active := false
			v.Network = dashboardNetwork{Active: &active, Known: true, CheckedAt: now.UnixMilli(), ExpiresAt: now.Add(5 * time.Second).UnixMilli()}
		} else {
			v.Network = a.cachedDashboardNetwork(r, id, now)
		}
		out.Items = append(out.Items, v)
	}
	if len(selected) != 0 {
		writeJobError(w, 404, "not_found", "one or more dashboard devices are unavailable")
		return
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if acceptsDashboardGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		writer, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer writer.Close()
		_ = json.NewEncoder(writer).Encode(out)
		return
	}
	writeJSON(w, 200, out)
}

func acceptsDashboardGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		for _, parameter := range fields[1:] {
			if strings.HasPrefix(strings.TrimSpace(parameter), "q=") {
				q, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(parameter), "q=")), 64)
				return err == nil && q > 0 && q <= 1
			}
		}
		return true
	}
	return false
}

// Metadata changes are published only after the storage mutation commits. They
// carry no runtime sample, and therefore cannot bypass report sequence fencing.
func (a *App) publishDashboardChange(id, kind string, revoked bool) {
	a.invalidateDashboardNetwork(id)
	payload, _ := json.Marshal(map[string]any{"event": "dashboard", "agent_id": id, "kind": kind, "revoked": revoked})
	a.hub.publish(payload)
}
