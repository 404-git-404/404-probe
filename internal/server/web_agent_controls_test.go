package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestWebAgentCheckSwitchesStopDispatchAndPreserveManualRegion(t *testing.T) {
	app, store, id, token := testApp(t)
	t.Cleanup(func() { app.Shutdown(); store.Close() })
	now := app.now()
	report := reportFor(id, 1)
	report.Management = &protocol.AgentManagementCapabilities{CountryCodeLookup: true}
	if w := postReport(t, app, token, report); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	base := "/api/v1/web/agents/" + id
	if w := organizationHTTP(t, app, http.MethodPut, base+"/youtube-region", `{"region":"AL"}`, true, true); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := store.CreateAgentCountryCodeLookup(t.Context(), id, now); err != nil {
		t.Fatal(err)
	}
	claim := protocol.ClaimRequest{ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: report.Epoch, SessionID: report.SessionID, SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeGoogleStatus}}
	first, w := claimHTTPJob(t, app, token, claim)
	if w.Code != http.StatusOK || first.ProbeType != protocol.ProbeTypeGoogleStatus {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, check := range []string{"country", "youtube"} {
		path := base + "/checks/" + check
		if w := organizationHTTP(t, app, http.MethodPut, path, `{"enabled":false}`, true, false); w.Code != http.StatusForbidden {
			t.Fatal(w.Code, w.Body.String())
		}
		if w := organizationHTTP(t, app, http.MethodPut, path, `{"enabled":false}`, true, true); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for suffix, want := range map[string]string{"/country-code/refresh": "lookup_disabled", "/google-status": "google_status_disabled"} {
		w := organizationHTTP(t, app, http.MethodPost, base+suffix, `{}`, true, true)
		if w.Code != http.StatusConflict || jobErrorCode(t, w) != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, w := claimHTTPJob(t, app, token, claim); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if lookup, err := store.ClaimAgentCountryCodeLookup(t.Context(), id, report.Epoch, report.SessionID, now); err != nil || lookup != nil {
		t.Fatalf("disabled country lookup claim = %+v, %v", lookup, err)
	}
	var detail webAgentDetailView
	if w := organizationHTTP(t, app, http.MethodGet, base, "", true, false); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if detail.CountryLookupEnabled || detail.YouTubeCheckEnabled || detail.GoogleStatus.Pending || detail.CountryLookup != nil {
		t.Fatalf("disabled detection projection: %+v", detail)
	}
	if snapshot, exists, err := store.GetGoogleStatusAt(t.Context(), id, now.Add(time.Hour)); err != nil || exists {
		t.Fatalf("cancelled lease must not become a failed detection: %+v %v %v", snapshot, exists, err)
	}
	for _, check := range []string{"country", "youtube"} {
		if w := organizationHTTP(t, app, http.MethodPut, base+"/checks/"+check, `{"enabled":true}`, true, true); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, err := store.CreateAgentCountryCodeLookup(t.Context(), id, now); err != nil {
		t.Fatal(err)
	}
	next, w := claimHTTPJob(t, app, token, claim)
	if w.Code != http.StatusOK || next.JobID == first.JobID {
		t.Fatal(w.Code, w.Body.String())
	}
	var dashboard dashboardResponse
	w = organizationHTTP(t, app, http.MethodGet, "/api/v1/web/dashboard?ids="+id, "", true, false)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &dashboard) != nil || len(dashboard.Items) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	row := dashboard.Items[0]
	if !row.CountryLookupEnabled || !row.YouTubeCheckEnabled || row.GoogleStatus.ManualRegion != "AL" {
		t.Fatal(row.AgentControls, row.GoogleStatus)
	}
}

func TestWebDiskFaultAcknowledgementIsScopedToOneRecovery(t *testing.T) {
	app, store, id, token := testApp(t)
	t.Cleanup(func() { app.Shutdown(); store.Close() })
	now := time.Now().Truncate(time.Millisecond)
	app.now = func() time.Time { return now }
	base := "/api/v1/web/agents/" + id
	var seq uint64
	reportDisk := func(status protocol.DiskHealthStatus, reasons ...protocol.DiskHealthReason) int64 {
		t.Helper()
		now = now.Add(time.Second)
		seq++
		report := reportFor(id, seq)
		report.CollectedAt = now.UnixMilli()
		report.HostHealth = &protocol.HostHealth{Disk: protocol.DiskHealth{Status: status, CheckedAt: report.CollectedAt, Reasons: reasons}}
		if status == protocol.DiskError {
			report.HostHealth.Disk.StartedAt = report.CollectedAt
		}
		if w := postReport(t, app, token, report); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		return report.CollectedAt
	}
	ack := func(at int64, code int) {
		t.Helper()
		w := organizationHTTP(t, app, http.MethodPut, base+"/disk-fault-acknowledgement", fmt.Sprintf(`{"fault_at":%d}`, at), true, true)
		if w.Code != code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	first := reportDisk(protocol.DiskError, protocol.DiskWriteEIO)
	ack(first, http.StatusConflict)
	reportDisk(protocol.DiskOK)
	ack(first, http.StatusOK)
	ack(first, http.StatusOK)
	second := reportDisk(protocol.DiskError, protocol.DiskReadEIO)
	reportDisk(protocol.DiskOK)
	ack(first, http.StatusConflict)
	var detail webAgentDetailView
	w := organizationHTTP(t, app, http.MethodGet, base, "", true, false)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if detail.DiskFaultAcknowledgedAt != first || detail.State.HostHealth.LastFaultAt != second {
		t.Fatal(detail.DiskFaultAcknowledgedAt, detail.State.HostHealth)
	}
	ack(second, http.StatusOK)
}

func TestDashboardReportsUnlimitedCycleUsage(t *testing.T) {
	app, store, id, token := testApp(t)
	t.Cleanup(func() { app.Shutdown(); store.Close() })
	now := time.Date(2026, 11, 22, 1, 0, 0, 0, time.UTC)
	app.now = func() time.Time { return now }
	plan := storage.AgentPlan{AgentID: id, TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2026-11-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(t.Context(), plan, nil, now); err != nil {
		t.Fatal(err)
	}
	report := reportFor(id, 1)
	report.RXBytes, report.TXBytes = 0, 0
	for _, gb := range []uint64{0, 300, 1500, 2500} {
		report.RXBytes = gb * 1e9
		report.CollectedAt = now.UnixMilli()
		if w := postReport(t, app, token, report); w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		var out dashboardResponse
		w := organizationHTTP(t, app, http.MethodGet, "/api/v1/web/dashboard?ids="+id, "", true, false)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 1 {
			t.Fatal(w.Code, w.Body.String())
		}
		row := out.Items[0]
		if row.Plan == nil || row.Plan.QuotaBytes != nil || row.Plan.UsageBytes != gb*1e9 || row.Traffic.QuotaState != "unlimited" {
			t.Fatal(row.Plan, row.Traffic)
		}
		now = now.Add(time.Second)
		report.Sequence++
	}
}
