package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const webAgentC = "cccccccccccccccccccccccccccccccc"

func newWebAgentTestApp(t *testing.T) (*App, *storage.Store, time.Time) {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	addControlAgent(t, store, controlAgentA, "alpha", time.Unix(300, 0))
	addControlAgent(t, store, controlAgentB, "beta", time.Unix(200, 0))
	addControlAgent(t, store, webAgentC, "gamma", time.Unix(100, 0))
	app, err := NewApp(store, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{
		PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test",
	}))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	now := time.Unix(400, 0)
	app.now = func() time.Time { return now }
	return app, store, now
}

func webAgentResponse(t *testing.T, app *App, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	addTestWebSession(t, app, request)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func getWebAgentCollection(t *testing.T, app *App, path string) webAgentCollectionView {
	t.Helper()
	response := webAgentResponse(t, app, http.MethodGet, path)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("path=%q status=%d cache=%q body=%s", path, response.Code,
			response.Header().Get("Cache-Control"), response.Body.String())
	}
	var collection webAgentCollectionView
	if err := json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	return collection
}

func TestWebAgentsPaginationFilteringAndCursorBinding(t *testing.T) {
	app, store, now := newWebAgentTestApp(t)
	defer store.Close()
	processControlAgentReport(t, store, controlAgentA, now.Add(-10*time.Second))
	if _, err := store.RevokeAgent(context.Background(), controlAgentB, now.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}

	first := getWebAgentCollection(t, app, "/api/v1/web/agents?limit=1")
	if len(first.Items) != 1 || first.Items[0].AgentID != controlAgentA || first.NextCursor == nil {
		t.Fatalf("first page=%+v", first)
	}
	second := getWebAgentCollection(t, app, "/api/v1/web/agents?limit=1&cursor="+*first.NextCursor)
	if len(second.Items) != 1 || second.Items[0].AgentID != webAgentC || second.NextCursor != nil {
		t.Fatalf("second page=%+v", second)
	}

	online := getWebAgentCollection(t, app, "/api/v1/web/agents?status=online")
	if len(online.Items) != 1 || online.Items[0].AgentID != controlAgentA || !online.Items[0].Online || online.Items[0].LastSeen == nil {
		t.Fatalf("online=%+v", online)
	}
	revoked := getWebAgentCollection(t, app, "/api/v1/web/agents?status=revoked")
	if len(revoked.Items) != 1 || revoked.Items[0].AgentID != controlAgentB || !revoked.Items[0].Revoked || revoked.Items[0].Online {
		t.Fatalf("revoked=%+v", revoked)
	}
	offline := getWebAgentCollection(t, app, "/api/v1/web/agents?status=offline")
	if len(offline.Items) != 1 || offline.Items[0].AgentID != webAgentC {
		t.Fatalf("offline=%+v", offline)
	}

	response := webAgentResponse(t, app, http.MethodGet,
		"/api/v1/web/agents?status=online&cursor="+*first.NextCursor)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_cursor" {
		t.Fatalf("filter-bound cursor status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAgentDetailShowsManagementCapabilitiesAndLegacyUpgradeGate(t *testing.T) {
	app, store, now := newWebAgentTestApp(t)
	defer store.Close()
	processControlAgentReport(t, store, controlAgentA, now)

	legacy := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/agents/"+controlAgentA)
	if legacy.Code != http.StatusOK {
		t.Fatalf("legacy detail status=%d body=%s", legacy.Code, legacy.Body.String())
	}
	var detail webAgentDetailView
	if err := json.Unmarshal(legacy.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Management.RemoteRemoval || detail.Management.CountryCodeLookup {
		t.Fatalf("legacy Agent must not advertise new management features: %+v", detail.Management)
	}

	report := protocol.Report{
		AgentID: controlAgentA, Epoch: 1, SessionID: "session", Sequence: 2, CollectedAt: now.Add(time.Second).UnixMilli(),
		Hostname: "host", OS: "linux", Arch: "amd64", BootID: "boot", Uptime: 11,
		CPUPercent: 5, Load1: 1, Load5: 2, Load15: 3, RAMUsed: 10, RAMTotal: 20, RAMPercent: 50,
		SwapUsed: 1, SwapTotal: 2, SwapPercent: 50, DiskUsed: 30, DiskTotal: 60, DiskPercent: 50,
		Management: &protocol.AgentManagementCapabilities{RemoteRemoval: true, CountryCodeLookup: true}, RXBytes: 110, TXBytes: 220,
	}
	if _, accepted, reason, err := store.ProcessReport(context.Background(), controlAgentA, report, now.Add(time.Second)); err != nil || !accepted {
		t.Fatalf("capable report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
	current := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/agents/"+controlAgentA)
	if current.Code != http.StatusOK {
		t.Fatalf("current detail status=%d body=%s", current.Code, current.Body.String())
	}
	if err := json.Unmarshal(current.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if !detail.Management.RemoteRemoval || !detail.Management.CountryCodeLookup {
		t.Fatalf("negotiated capabilities missing from detail: %+v", detail.Management)
	}
}

func TestWebAgentDetailHistoryWhitelistAndRouting(t *testing.T) {
	app, store, now := newWebAgentTestApp(t)
	defer store.Close()
	processControlAgentReport(t, store, controlAgentA, now.Add(-10*time.Second))

	detailPath := webAgentPathPrefix + controlAgentA
	response := webAgentResponse(t, app, http.MethodGet, detailPath)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("detail status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var detail webAgentDetailView
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.AgentID != controlAgentA || detail.State == nil || detail.State.Hostname != "host" || !detail.Online || detail.LastSeen == nil ||
		detail.State.CPUStealPercent == nil || *detail.State.CPUStealPercent != 3 || detail.State.DiskBusyPercent == nil || *detail.State.DiskBusyPercent != 72 || detail.CountryCode != "US" || detail.CountrySource != "automatic" {
		t.Fatalf("detail=%s", response.Body.String())
	}
	assertNoWebAgentSecrets(t, response.Body.String())

	historyPath := detailPath + "/history?hours=1"
	response = webAgentResponse(t, app, http.MethodGet, historyPath)
	var history webAgentHistoryView
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &history) != nil ||
		history.AgentID != controlAgentA || history.Hours != 1 || len(history.Points) != 1 {
		t.Fatalf("history status=%d body=%s", response.Code, response.Body.String())
	}
	assertNoWebAgentSecrets(t, response.Body.String())

	tests := []struct {
		method string
		path   string
		code   int
		error  string
	}{
		{http.MethodGet, detailPath + "?x=1", http.StatusBadRequest, "invalid_query"},
		{http.MethodGet, webAgentPathPrefix + "dddddddddddddddddddddddddddddddd", http.StatusNotFound, "agent_not_found"},
		{http.MethodGet, webAgentPathPrefix + "bad", http.StatusBadRequest, "invalid_request"},
		{http.MethodPost, detailPath, http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodGet, detailPath + "/history?hours=0", http.StatusBadRequest, "invalid_query"},
		{http.MethodGet, detailPath + "/history?hours=721", http.StatusBadRequest, "invalid_query"},
		{http.MethodGet, detailPath + "/history?x=1", http.StatusBadRequest, "invalid_query"},
		{http.MethodPost, detailPath + "/history", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodGet, "/api/v1/web/events?x=1", http.StatusBadRequest, "invalid_query"},
		{http.MethodPost, "/api/v1/web/events", http.StatusMethodNotAllowed, "method_not_allowed"},
	}
	for _, test := range tests {
		response = webAgentResponse(t, app, test.method, test.path)
		if response.Code != test.code || jobErrorCode(t, response) != test.error || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("method=%s path=%q status=%d cache=%q body=%s", test.method, test.path, response.Code,
				response.Header().Get("Cache-Control"), response.Body.String())
		}
	}
}

func TestWebAgentCountryOverrideWinsAndCanReturnToAutomatic(t *testing.T) {
	app, store, now := newWebAgentTestApp(t)
	defer store.Close()
	processControlAgentReport(t, store, controlAgentA, now.Add(-10*time.Second))

	if _, err := store.PutAgentPlan(context.Background(), storage.AgentPlan{AgentID: controlAgentA, CountryCodeOverride: "JP"}, nil, now); err != nil {
		t.Fatal(err)
	}
	response := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+controlAgentA)
	var detail webAgentDetailView
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &detail) != nil || detail.CountryCode != "JP" || detail.CountrySource != "manual" {
		t.Fatalf("manual detail status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := store.DeleteAgentPlan(context.Background(), controlAgentA); err != nil {
		t.Fatal(err)
	}
	response = webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+controlAgentA)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &detail) != nil || detail.CountryCode != "US" || detail.CountrySource != "automatic" {
		t.Fatalf("automatic detail status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAgentLegacyRoutesAndDashboardUseOnlyWebReadSurface(t *testing.T) {
	app, store, _ := newWebAgentTestApp(t)
	defer store.Close()
	for _, path := range []string{"/api/v1/agents", "/api/v1/agents/" + controlAgentA, "/api/v1/events", "/api/v1/events/"} {
		unauthenticated := webRequest(app, http.MethodGet, path, nil, nil)
		if unauthenticated.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated legacy path=%q status=%d", path, unauthenticated.Code)
		}
		authenticated := webAgentResponse(t, app, http.MethodGet, path)
		if authenticated.Code != http.StatusNotFound || authenticated.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("authenticated legacy path=%q status=%d cache=%q", path, authenticated.Code,
				authenticated.Header().Get("Cache-Control"))
		}
	}

	for _, path := range []string{"/", "/history.html", "/app.js", "/history.js", "/session.js"} {
		response := webAgentResponse(t, app, http.MethodGet, path)
		cache := "no-store"
		if strings.HasSuffix(path, ".js") {
			cache = "private, no-cache"
		}
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != cache {
			t.Fatalf("asset=%q status=%d cache=%q", path, response.Code, response.Header().Get("Cache-Control"))
		}
		body := response.Body.String()
		if strings.HasSuffix(path, ".js") && (strings.Contains(body, "/api/v1/control/") ||
			strings.Contains(body, "'/api/v1/agents") || strings.Contains(body, "'/api/v1/events")) {
			t.Fatalf("asset=%q bypasses Web read surface: %s", path, body)
		}
		if path == "/app.js" && (!strings.Contains(body, "/api/v1/web/agents") || !strings.Contains(body, "/api/v1/web/events")) {
			t.Fatalf("dashboard does not use Web Agent API: %s", body)
		}
		if (path == "/" || path == "/history.html") && (!strings.Contains(body, app.staticAssets.fingerprints["session.js"]) || !strings.Contains(body, "id=\"logout\"")) {
			t.Fatalf("page=%q missing logout foundation: %s", path, body)
		}
	}
}

func assertNoWebAgentSecrets(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{"token", "token_hash", "epoch", "session_id", "sequence", "boot_id", "raw_rx", "raw_tx", "lease_token", "result_hash"} {
		if strings.Contains(body, `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q leaked: %s", forbidden, body)
		}
	}
}
