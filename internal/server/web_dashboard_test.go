package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestDashboardBatchLifecycleFreshnessOrganizationAndNoIPOracle(t *testing.T) {
	a, s, id, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	now := time.Date(2026, 10, 29, 0, 30, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	report := reportFor(id, 1)
	report.CollectedAt = now.UnixMilli()
	report.CPUPercent = 94
	report.AgentVersion = "v1.0.9"
	report.Hostname = "visible-host"
	if w := postReport(t, a, token, report); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := s.SetPublicEgressEnabled(t.Context(), id, true, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	report.Sequence++
	report.PublicEgress = &protocol.PublicEgress{Version: 1, IPv4: protocol.EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", CheckedAt: now.UnixMilli(), ValidUntil: now.Add(protocol.EgressSuccessTTL).UnixMilli(), Reason: "verified"}, IPv6: protocol.EgressObservation{Family: "ipv6", Status: "unavailable", CheckedAt: now.UnixMilli(), ValidUntil: now.Add(protocol.EgressFailureTTL).UnixMilli(), Reason: "no_route"}}
	if w := postReport(t, a, token, report); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for i, other := range []string{strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)} {
		_, hash, _ := auth.NewToken()
		if err := s.AddAgent(t.Context(), other, "other", hash, now); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			s.DisableAgent(t.Context(), other, now)
		}
		if i == 2 {
			s.RevokeAgent(t.Context(), other, now)
		}
	}
	zero := uint64(0)
	if _, err := s.PutAgentPlan(t.Context(), storage.AgentPlan{AgentID: id, Currency: "USD", RenewalPrice: "0", RenewalPricePeriod: "monthly", RenewalDate: "2026-10-28", ExpiryDate: "2026-10-20", Timezone: "America/Los_Angeles", CountryCodeOverride: "US", TrafficMode: "sum", QuotaBytes: &zero, QuotaValue: "0", QuotaUnit: "GB", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2026-10-01"}, nil, now); err != nil {
		t.Fatal(err)
	}
	c, _ := s.OrganizationCatalog(t.Context())
	group, err := s.ChangeOrganization(t.Context(), storage.OrganizationChange{ExpectedRevision: c.Revision, Kind: "create_group", Name: "Main"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeOrganization(t.Context(), storage.OrganizationChange{ExpectedRevision: group.Revision, Kind: "assign", AgentID: id, GroupID: &group.ID, NewTags: []string{"Tagged"}}, now); err != nil {
		t.Fatal(err)
	}
	w := organizationHTTP(t, a, "GET", "/api/v1/web/dashboard", "", true, false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out dashboardResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 3 {
		t.Fatal(len(out.Items))
	}
	items := map[string]dashboardSummary{}
	for _, v := range out.Items {
		items[v.AgentID] = v
	}
	v := items[id]
	if v.Lifecycle != "online" || v.Resources.CPU == nil || *v.Resources.CPU != 94 || v.Resources.Stale || v.CountryCode != "US" || v.CountrySource != "manual" || v.GroupID == nil || len(v.TagIDs) != 1 {
		t.Fatal(v)
	}
	if v.Renewal.Days == nil || *v.Renewal.Days != 0 || v.Renewal.Type != "due_soon" || v.Renewal.Field != "renewal_date" || v.Traffic.QuotaState != "zero" {
		t.Fatal(v.Renewal, v.Traffic)
	}
	if strings.Contains(w.Body.String(), "8.8.8.8") || len(v.MaskedIPs) != 1 || v.MaskedIPs[0] != "8.8.*.*" {
		t.Fatal("unmasked IP in summary", v.MaskedIPs)
	}
	if unknown := items[strings.Repeat("b", 32)]; unknown.Lifecycle != "unknown" || unknown.Resources.Known || unknown.Resources.CPU != nil || unknown.State != nil {
		t.Fatal(unknown)
	}
	if items[strings.Repeat("c", 32)].Lifecycle != "paused" {
		t.Fatal("paused counted offline")
	}
	w = organizationHTTP(t, a, "GET", "/api/v1/web/dashboard?include_revoked=true", "", true, false)
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Items) != 4 {
		t.Fatal(w.Body.String())
	}
	for _, query := range []string{"?search=8.8.8.8", "?ids=8.8.8.8", "?ids="} {
		if w := organizationHTTP(t, a, "GET", "/api/v1/web/dashboard"+query, "", true, false); w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	w = organizationHTTP(t, a, "GET", "/api/v1/web/dashboard?ids="+id, "", true, false)
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out.Items) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	now = now.Add(31 * time.Second)
	w = organizationHTTP(t, a, "GET", "/api/v1/web/dashboard?ids="+id, "", true, false)
	json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Items[0].Resources.Stale || out.Items[0].Lifecycle != "offline" {
		t.Fatal(out.Items[0])
	}
}

func TestDashboardRenewalCalendarBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 29, 0, 30, 0, 0, time.UTC)
	for _, row := range []struct {
		date, zone, kind string
		days             int
	}{{"2026-10-28", "America/Los_Angeles", "due_soon", 0}, {"2026-10-28", "UTC", "overdue", -1}, {"2026-11-05", "UTC", "due_soon", 7}, {"2026-11-06", "UTC", "upcoming", 8}, {"9999-12-31", "UTC", "upcoming", 2912140}} {
		got := renewalSummary(&storage.AgentPlan{RenewalDate: row.date, Timezone: row.zone}, now)
		if got.Days == nil || got.Type != row.kind || row.date != "9999-12-31" && *got.Days != row.days {
			t.Fatal(row, got)
		}
		if row.date == "9999-12-31" && *got.Days < 2_000_000 {
			t.Fatal("duration overflow", got)
		}
	}
	for _, p := range []*storage.AgentPlan{nil, {}, {RenewalDate: "2026-02-30"}, {RenewalDate: "2026-10-28", Timezone: "Bad/Zone"}} {
		r := renewalSummary(p, now)
		if r.Days != nil || r.Type != "missing" && r.Type != "invalid" {
			t.Fatal(r)
		}
	}
}

func TestDashboardMaskMatchesVisibleFormsAndVersionIsStable(t *testing.T) {
	for ip, want := range map[string]string{"203.0.113.9": "203.0.*.*", "2001:db8::abcd": "2001:db8:…", "::ffff:192.0.2.1": "0:0:…", "203.00.1.2": "", "bad": "", "fe80::1%eth0": ""} {
		if got := maskDashboardIP(ip); got != want {
			t.Fatal(ip, got, want)
		}
	}
	value := int64(123)
	other := value
	a := storage.Agent{ID: "id", Name: "name", DisabledAt: &value}
	b := a
	b.DisabledAt = &other
	state := storage.State{Epoch: 1, SessionID: "secret-session", Sequence: 1}
	if dashboardVersion(a, &state) != dashboardVersion(b, &state) {
		t.Fatal("token includes pointer address")
	}
	before := dashboardVersion(a, &state)
	state.Sequence++
	if before == dashboardVersion(a, &state) || strings.Contains(dashboardVersion(a, &state), state.SessionID) {
		t.Fatal("version is stale or exposes session")
	}
}

func TestDashboardHubNeverSilentlyDropsCriticalChanges(t *testing.T) {
	h := newHub(2)
	ch, _ := h.subscribe()
	for i := 0; i < 200; i++ {
		h.publish([]byte(`{"event":"organization","revision":2}`))
	}
	for i := 0; i < 200; i++ {
		message, open := <-ch
		if !open {
			t.Fatal("normal burst disconnected")
		}
		h.consumed(ch, len(message))
	}
	h.mu.Lock()
	queued := h.queuedBytes[ch]
	h.mu.Unlock()
	if queued != 0 {
		t.Fatal("queue memory accounting", queued)
	}
	for i := 0; i < 257; i++ {
		h.publish([]byte(`{"agent_id":"x"}`))
	}
	for range ch {
	} // closed stream, never an apparently healthy stream with gaps
	if _, ok := h.subscribe(); !ok {
		t.Fatal("slow client slot was not released")
	}
}

func TestDashboardNetworkCacheExpiresAndConfigurationInvalidates(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	r := httptest.NewRequest("GET", "/", nil)
	now := time.Now()
	first := a.cachedDashboardNetwork(r, id, now)
	if !first.Known || first.Active == nil || *first.Active || first.Monitored {
		t.Fatal(first)
	}
	second := a.cachedDashboardNetwork(r, id, now.Add(time.Second))
	if second.CheckedAt != first.CheckedAt {
		t.Fatal("cache did not coalesce")
	}
	a.invalidateDashboardNetwork(id)
	third := a.cachedDashboardNetwork(r, id, now.Add(2*time.Second))
	if third.CheckedAt == first.CheckedAt {
		t.Fatal("configuration did not invalidate")
	}
	fourth := a.cachedDashboardNetwork(r, id, now.Add(8*time.Second))
	if fourth.CheckedAt == third.CheckedAt {
		t.Fatal("cache freshness unbounded")
	}
}
