package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if len(second.Items) != 1 || second.Items[0].AgentID != controlAgentB || second.NextCursor == nil {
		t.Fatalf("second page=%+v", second)
	}
	third := getWebAgentCollection(t, app, "/api/v1/web/agents?limit=2&cursor="+*second.NextCursor)
	if len(third.Items) != 1 || third.Items[0].AgentID != webAgentC || third.NextCursor != nil {
		t.Fatalf("third page=%+v", third)
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
	if detail.AgentID != controlAgentA || detail.State == nil || detail.State.Hostname != "host" || !detail.Online || detail.LastSeen == nil {
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
		{http.MethodPost, "/api/v1/web/agents", http.StatusMethodNotAllowed, "method_not_allowed"},
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
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
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
		if (path == "/" || path == "/history.html") && (!strings.Contains(body, "/session.js") || !strings.Contains(body, "id=\"logout\"")) {
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
