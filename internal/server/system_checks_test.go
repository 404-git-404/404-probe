package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

func TestSystemChecksReportSettingsAndDashboard(t *testing.T) {
	app, store, _, _ := testApp(t)
	t.Cleanup(func() { app.Shutdown(); store.Close() })
	now := app.now()
	app.debianStable.next = now.Add(24 * time.Hour)
	id, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := store.RegisterServerLocalAgent(t.Context(), id, "Local", hash, now); err != nil || got != id {
		t.Fatalf("register local Agent: id=%q err=%v", got, err)
	}
	base := "/api/v1/web/agents/" + id
	edit := `{"certificate_directories":["/opt/certs"],"watched_services":["nginx.service","nginx"]}`
	for _, test := range []struct {
		body string
		csrf bool
		code int
	}{
		{edit, false, 403},
		{`{"certificate_directories":["relative"],"watched_services":[]}`, true, 400},
		{edit, true, 200},
	} {
		w := organizationHTTP(t, app, http.MethodPut, base+"/system-checks", test.body, true, test.csrf)
		if w.Code != test.code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	send := func(report protocol.Report, supported bool) protocol.ReportResponse {
		raw, _ := json.Marshal(report)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if supported {
			req.Header.Set(protocol.SystemCheckHeader, "1")
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("report status %d %s", w.Code, w.Body.String())
		}
		return decodeReportResponse(t, w)
	}
	report := reportFor(id, 1)
	report.CollectedAt = now.UnixMilli()
	if response := send(report, false); response.SystemCheckSettings != nil {
		t.Fatal("legacy agent received a system setting")
	}
	report.Sequence++
	report.CollectedAt = now.Add(-6 * time.Second).UnixMilli()
	checks := &protocol.SystemChecks{
		Supported: true, DebianMajor: 13, RebootStatus: protocol.CheckOK, RebootRequired: true,
		RebootPackages: []string{"linux-image-6.12.x-amd64", "libc6"}, RebootPackagesTruncated: true,
		CertificateScan: protocol.CertificateScan{Certificates: []protocol.Certificate{
			{Domains: "node.example.test", Path: "/opt/certs/node.crt", Fingerprint: strings.Repeat("a", 64), ExpiresAt: now.Add(7 * 24 * time.Hour).UnixMilli()},
		}},
		Services: []protocol.WatchedService{{Name: "nginx", State: "inactive"}},
		Fail2ban: protocol.Fail2banCheck{Status: protocol.CheckOK, JailCount: 1, Jails: []protocol.Fail2banJail{{Name: "sshd", Total: 500, IPs: []string{"192.0.2.15"}}}},
	}
	report.SystemChecks = checks
	settings := send(report, true).SystemCheckSettings
	wantSettings := &protocol.SystemCheckSettings{CertificateDirectories: []string{"/opt/certs"}, WatchedServices: []string{"nginx"}}
	if !reflect.DeepEqual(settings, wantSettings) {
		t.Fatalf("settings = %+v", settings)
	}
	report.Sequence++
	report.SystemChecks = nil
	send(report, true)
	w := organizationHTTP(t, app, http.MethodGet, base, "", true, false)
	var detail webAgentDetailView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	want := systemAttention{Certificate: "warning", Service: true, Time: true}
	if !detail.ServerLocal || detail.SystemChecks == nil || !detail.SystemChecks.RebootRequired ||
		!reflect.DeepEqual(detail.SystemChecks.RebootPackages, checks.RebootPackages) || !detail.SystemChecks.RebootPackagesTruncated ||
		detail.SystemAttention != want || len(detail.SystemChecks.Fail2ban.Jails) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	report.Sequence++
	legacyChecks := *checks
	legacyChecks.RebootPackages, legacyChecks.RebootPackagesTruncated = nil, false
	report.SystemChecks = &legacyChecks
	send(report, true)
	w = organizationHTTP(t, app, http.MethodGet, base, "", true, false)
	detail = webAgentDetailView{}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.SystemChecks == nil ||
		!detail.SystemChecks.RebootRequired || strings.Contains(w.Body.String(), "reboot_packages") {
		t.Fatalf("legacy system checks: %d %s", w.Code, w.Body.String())
	}
	w = organizationHTTP(t, app, http.MethodGet, "/api/v1/web/dashboard?ids="+id, "", true, false)
	var dashboard dashboardResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &dashboard) != nil || len(dashboard.Items) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !dashboard.Items[0].ServerLocal || dashboard.Items[0].SystemAttention != want || strings.Contains(w.Body.String(), "192.0.2.15") {
		t.Fatal(w.Body.String())
	}
	state, err := store.ListStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range state {
		if s.SystemChecks != nil {
			t.Fatal("system checks were persisted")
		}
	}
}

func TestSystemAttentionBoundaries(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	checks := &protocol.SystemChecks{
		Supported: true, RebootRequired: true, Packages: protocol.PackageCheck{Upgradable: 99, Security: 99},
		NTPStatus: protocol.CheckOK, NTPSynchronized: true, TimeOffsetMS: 5000,
		CertificateScan: protocol.CertificateScan{Certificates: []protocol.Certificate{{ExpiresAt: now.Add(7*24*time.Hour + time.Millisecond).UnixMilli()}}},
	}
	if got := checkAttention(checks, now); got != (systemAttention{}) {
		t.Fatal(got)
	}
	checks.Certificates[0].ExpiresAt = now.UnixMilli()
	checks.TimeOffsetMS = 0
	checks.NTPSynchronized = false
	checks.FailedUnits = []string{"fixture.timer"}
	if got := checkAttention(checks, now); got != (systemAttention{Certificate: "danger", Time: true, Service: true}) {
		t.Fatal(got)
	}
}

func TestSystemRestartAttentionLevels(t *testing.T) {
	for _, tc := range []struct {
		count uint64
		want  string
	}{{0, ""}, {1, "warning"}, {2, "warning"}, {3, "danger"}} {
		checks := protocol.SystemChecks{Supported: true}
		if tc.count > 0 {
			checks.ServiceRestarts = []protocol.ServiceRestart{{Unit: "unwatched.service", Count: tc.count}}
		}
		got := checkAttention(&checks, time.Now())
		if got.Service || got.ServiceRestart != tc.want {
			t.Fatalf("count=%d attention=%+v", tc.count, got)
		}
	}
}
