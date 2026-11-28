package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
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
		NTPStatus: protocol.CheckOK, ClockMeasured: true, TimeOffsetMS: 6000,
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
	want.Items = []systemAttentionItem{
		{protocol.SystemCheckKey{Kind: "certificate", Item: "/opt/certs/node.crt"}, "warning", "/opt/certs/node.crt 将于 " + now.Add(7*24*time.Hour).UTC().Format(time.DateOnly) + " 到期"},
		{protocol.SystemCheckKey{Kind: "service", Item: "nginx.service"}, "danger", "nginx.service 处于 inactive 状态"},
		{protocol.SystemCheckKey{Kind: "time", Item: "time"}, "warning", "NTP 未同步，时间偏差 +6.0 秒"},
	}
	if !detail.ServerLocal || detail.SystemChecks == nil || !detail.SystemChecks.RebootRequired ||
		!reflect.DeepEqual(detail.SystemChecks.RebootPackages, checks.RebootPackages) || !detail.SystemChecks.RebootPackagesTruncated ||
		!reflect.DeepEqual(detail.SystemAttention, want) || len(detail.SystemChecks.Fail2ban.Jails) != 1 {
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
	if !dashboard.Items[0].ServerLocal || !reflect.DeepEqual(dashboard.Items[0].SystemAttention, want) || strings.Contains(w.Body.String(), "192.0.2.15") {
		t.Fatal(w.Body.String())
	}
	ignoreBody := `{"kind":"service","item":"nginx.service"}`
	if w := organizationHTTP(t, app, http.MethodPut, base+"/system-check-ignores", ignoreBody, true, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	ignored := want.Items[1]
	want.Service, want.Items, want.Ignored = false, []systemAttentionItem{want.Items[0], want.Items[2]}, []systemAttentionItem{ignored}
	events, ok := app.hub.subscribe()
	if !ok {
		t.Fatal("subscribe failed")
	}
	defer app.hub.unsubscribe(events)
	report.Sequence++
	report.SystemChecks = nil
	send(report, true)
	select {
	case payload := <-events:
		var event webAgentEventView
		if json.Unmarshal(payload, &event) != nil || !reflect.DeepEqual(event.SystemAttention, want) {
			t.Fatalf("telemetry event: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("missing telemetry event")
	}
	for _, path := range []string{base, "/api/v1/web/dashboard?ids=" + id, "/api/v1/web/agents"} {
		w = organizationHTTP(t, app, http.MethodGet, path, "", true, false)
		var wire struct {
			SystemAttention systemAttention       `json:"system_attention"`
			Items           []webAgentSummaryView `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &wire) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		got := wire.SystemAttention
		for _, item := range wire.Items {
			if item.AgentID == id {
				got = item.SystemAttention
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s attention = %+v, want %+v", path, got, want)
		}
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
	if got := checkAttention(checks, now, nil); !reflect.DeepEqual(got, systemAttention{}) {
		t.Fatal(got)
	}
	checks.Certificates[0].ExpiresAt = now.UnixMilli()
	checks.TimeOffsetMS = 0
	checks.NTPSynchronized = false
	checks.FailedUnits = []string{"fixture.timer"}
	if got := checkAttention(checks, now, nil); got.Certificate != "danger" || !got.Time || !got.Service || len(got.Items) != 3 {
		t.Fatal(got)
	}
}

func TestLocalDNSAttentionIgnoreAndLegacyAgent(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	now := time.Now()
	app.now = func() time.Time { return now }
	path := webAgentPathPrefix + id
	report := reportFor(id, 1)
	report.CollectedAt = now.UnixMilli()
	checks := &protocol.SystemChecks{Supported: true, ClockMeasured: true}
	send := func() {
		t.Helper()
		report.SystemChecks = checks
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/report", bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set(protocol.SystemCheckHeader, "1")
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		report.Sequence++
	}
	detail := func() webAgentDetailView {
		t.Helper()
		w := organizationHTTP(t, app, http.MethodGet, path, "", true, false)
		var view webAgentDetailView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.SystemChecks == nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return view
	}
	send()
	if got := detail(); got.SystemAttention.DNS || got.SystemChecks.LocalDNS != nil {
		t.Fatal("old Agent produced DNS attention", got.SystemAttention)
	}
	check := protocol.LocalDNSCheck{Status: protocol.CheckOK, Query: "probe.example.com", Servers: []protocol.LocalDNSObservation{
		{Address: "127.0.0.1", Result: protocol.DNSTimeout, CheckedAt: now.UnixMilli(), ConsecutiveFailures: 1},
		{Address: "::1", Result: protocol.CheckOK, CheckedAt: now.UnixMilli()},
	}}
	checks.LocalDNS = &check
	send()
	if got := detail().SystemAttention; got.DNS || len(got.Items) != 0 {
		t.Fatal("first failure alarmed", got)
	}
	check.Servers[0].ConsecutiveFailures = 2
	send()
	want := systemAttentionItem{protocol.SystemCheckKey{Kind: "dns", Item: "127.0.0.1"}, "danger", "127.0.0.1 解析 probe.example.com 失败：超时"}
	if got := detail().SystemAttention; !got.DNS || !reflect.DeepEqual(got.Items, []systemAttentionItem{want}) {
		t.Fatal("second failure did not alarm", got)
	}
	ignorePath := path + "/system-check-ignores"
	body := `{"kind":"dns","item":"127.0.0.1"}`
	if w := organizationHTTP(t, app, http.MethodPut, ignorePath, body, true, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := detail().SystemAttention; got.DNS || len(got.Items) != 0 || !reflect.DeepEqual(got.Ignored, []systemAttentionItem{want}) {
		t.Fatal("ignored DNS", got)
	}
	if w := organizationHTTP(t, app, http.MethodDelete, ignorePath, body, true, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := detail().SystemAttention; !got.DNS || len(got.Ignored) != 0 {
		t.Fatal("unignored DNS", got)
	}
	check.Servers[0].Result, check.Servers[0].ConsecutiveFailures = protocol.CheckOK, 0
	send()
	if got := detail().SystemAttention; got.DNS || len(got.Items) != 0 {
		t.Fatal("first success did not clear alarm", got)
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
		got := checkAttention(&checks, time.Now(), nil)
		if got.Service || got.ServiceRestart != tc.want {
			t.Fatalf("count=%d attention=%+v", tc.count, got)
		}
	}
}

func TestSystemAttentionIgnoreKeepsOtherItemsAndKinds(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	checks := &protocol.SystemChecks{
		Supported: true, NTPStatus: protocol.CheckOK, TimeOffsetMS: -7200,
		CertificateScan: protocol.CertificateScan{Certificates: []protocol.Certificate{
			{Path: "/expired", ExpiresAt: now.UnixMilli()},
			{Path: "/upcoming", ExpiresAt: now.Add(24 * time.Hour).UnixMilli()},
			{Path: "/healthy", ExpiresAt: now.Add(8 * 24 * time.Hour).UnixMilli()},
		}},
		FailedUnits:     []string{"exim4.service", "other.timer"},
		Services:        []protocol.WatchedService{{Name: "exim4", State: "failed"}},
		ServiceRestarts: []protocol.ServiceRestart{{Unit: "exim4.service", Count: 1}, {Unit: "busy.service", Count: 3}},
	}
	ignores := storage.SystemCheckIgnores{
		{Kind: "service", Item: "exim4.service"}:        true,
		{Kind: "service_restart", Item: "busy.service"}: true,
		{Kind: "certificate", Item: "/expired"}:         true,
		{Kind: "certificate", Item: "/healthy"}:         true,
		{Kind: "time", Item: "time"}:                    true,
	}
	got := checkAttention(checks, now, ignores)
	want := systemAttention{
		Certificate: "warning", Service: true, ServiceRestart: "warning",
		Items: []systemAttentionItem{
			{protocol.SystemCheckKey{Kind: "certificate", Item: "/upcoming"}, "warning", "/upcoming 将于 2026-11-27 到期"},
			{protocol.SystemCheckKey{Kind: "service", Item: "other.timer"}, "danger", "other.timer 处于 failed 状态"},
			{protocol.SystemCheckKey{Kind: "service_restart", Item: "exim4.service"}, "warning", "近 10 分钟重启 1 次"},
		},
		Ignored: []systemAttentionItem{
			{protocol.SystemCheckKey{Kind: "certificate", Item: "/expired"}, "danger", "/expired 已于 2026-11-26 到期"},
			{protocol.SystemCheckKey{Kind: "service", Item: "exim4.service"}, "danger", "exim4.service 处于 failed 状态"},
			{protocol.SystemCheckKey{Kind: "service_restart", Item: "busy.service"}, "danger", "近 10 分钟重启 3 次"},
			{protocol.SystemCheckKey{Kind: "time", Item: "time"}, "warning", "NTP 未同步，时间偏差 -7.2 秒"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention = %+v, want %+v", got, want)
	}
}

func TestSystemCheckIgnoreAPI(t *testing.T) {
	app, store, id, _ := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	path := "/api/v1/web/agents/" + id + "/system-check-ignores"
	const body = `{"kind":"service","item":"exim4.service"}`
	request := func(method, body string, authenticated, csrf bool, code int) *httptest.ResponseRecorder {
		t.Helper()
		w := organizationHTTP(t, app, method, path, body, authenticated, csrf)
		if w.Code != code {
			t.Fatalf("%s %s: got %d want %d: %s", method, body, w.Code, code, w.Body)
		}
		return w
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		request(method, body, false, false, 401)
		request(method, body, true, false, 403)
		for _, bad := range []string{`{"kind":"disk","item":"disk"}`, `{"kind":"service","item":"exim4"}`,
			`{"kind":"service_restart","item":"backup.timer"}`, `{"kind":"certificate","item":"relative.pem"}`,
			`{"kind":"time","item":"ntp"}`, `{"kind":"time","item":"time","unexpected":true}`,
			fmt.Sprintf(`{"kind":"certificate","item":%q}`, "/"+strings.Repeat("界", 171))} {
			request(method, bad, true, true, 400)
		}
	}
	request(http.MethodPut, body, true, true, 200)
	request(http.MethodPut, body, true, true, 200)
	for i := range 127 {
		request(http.MethodPut, fmt.Sprintf(`{"kind":"service","item":"other%d.service"}`, i), true, true, 200)
	}
	request(http.MethodPut, body, true, true, 200)
	if w := request(http.MethodPut, `{"kind":"time","item":"time"}`, true, true, 400); !strings.Contains(w.Body.String(), "limit_reached") {
		t.Fatal(w.Body.String())
	}
	request(http.MethodDelete, body, true, true, 200)
	request(http.MethodDelete, body, true, true, 200)
	request(http.MethodPut, `{"kind":"time","item":"time"}`, true, true, 200)
	ignores, err := store.SystemCheckIgnores(t.Context(), id)
	if err != nil || len(ignores) != 128 || !ignores[protocol.SystemCheckKey{Kind: "time", Item: "time"}] ||
		ignores[protocol.SystemCheckKey{Kind: "service", Item: "exim4.service"}] {
		t.Fatalf("persisted ignores = %v, err=%v", ignores, err)
	}
}
