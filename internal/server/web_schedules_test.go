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

func TestWebScheduleAuthenticationAndQueryValidation(t *testing.T) {
	app, store, _ := newWebAgentTestApp(t)
	defer store.Close()
	unauthenticated := webRequest(app, http.MethodGet, "/api/v1/web/schedules?unknown=x", nil, nil)
	if unauthenticated.Code != http.StatusUnauthorized || jobErrorCode(t, unauthenticated) != "unauthorized" {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	for _, suffix := range []string{
		"?unknown=x", "?agent_id=", "?agent_id=bad", "?agent_id=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"?enabled=", "?enabled=1", "?enabled=TRUE", "?enabled=true&enabled=false",
		"?probe_type=", "?probe_type=dns", "?probe_type=http&probe_type=tcp_connect",
		"?limit=0", "?limit=101", "?cursor=x",
	} {
		response := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/schedules"+suffix)
		if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("query=%q status=%d cache=%q body=%s", suffix, response.Code,
				response.Header().Get("Cache-Control"), response.Body.String())
		}
		code := jobErrorCode(t, response)
		if code != "invalid_query" && code != "invalid_cursor" {
			t.Fatalf("query=%q code=%q body=%s", suffix, code, response.Body.String())
		}
	}
}

func TestWebSchedulePaginationFiltersAndSummaryMinimization(t *testing.T) {
	app, store, _ := newWebAgentTestApp(t)
	defer store.Close()
	expected := 204
	putControlSchedule(t, store, controlScheduleA, controlAgentA, "web", protocol.ProbeTypeHTTP,
		protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://secret.example/health", Method: "HEAD", ExpectedStatus: &expected}}, true, time.Unix(300, 0))
	putControlSchedule(t, store, controlScheduleB, controlAgentA, "tcp", protocol.ProbeTypeTCPConnect,
		protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "private.example", Port: 443}}, false, time.Unix(200, 0))
	putControlSchedule(t, store, controlScheduleC, webAgentC, "ping", protocol.ProbeTypeICMPPing,
		protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "192.0.2.1", Count: 2}}, true, time.Unix(100, 0))

	firstResponse := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/schedules?limit=1")
	var first webScheduleCollectionView
	decodeWebScheduleCollection(t, firstResponse, &first)
	if len(first.Items) != 1 || first.Items[0].ScheduleID != controlScheduleA || first.NextCursor == nil {
		t.Fatalf("first=%+v", first)
	}
	if strings.Contains(firstResponse.Body.String(), `"config"`) || strings.Contains(firstResponse.Body.String(), "secret.example") {
		t.Fatalf("collection exposed detail config: %s", firstResponse.Body.String())
	}
	secondResponse := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/schedules?limit=1&cursor="+*first.NextCursor)
	var second webScheduleCollectionView
	decodeWebScheduleCollection(t, secondResponse, &second)
	if len(second.Items) != 1 || second.Items[0].ScheduleID != controlScheduleB || second.NextCursor == nil {
		t.Fatalf("second=%+v", second)
	}
	thirdResponse := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/schedules?limit=2&cursor="+*second.NextCursor)
	var third webScheduleCollectionView
	decodeWebScheduleCollection(t, thirdResponse, &third)
	if len(third.Items) != 1 || third.Items[0].ScheduleID != controlScheduleC || third.NextCursor != nil {
		t.Fatalf("third=%+v", third)
	}

	assertWebScheduleIDs(t, app, "/api/v1/web/schedules?agent_id="+controlAgentA, controlScheduleA, controlScheduleB)
	assertWebScheduleIDs(t, app, "/api/v1/web/schedules?enabled=false", controlScheduleB)
	assertWebScheduleIDs(t, app, "/api/v1/web/schedules?probe_type=http", controlScheduleA)
	assertWebScheduleIDs(t, app, "/api/v1/web/schedules?enabled=true&agent_id="+webAgentC+"&probe_type=icmp_ping", controlScheduleC)

	bound := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/schedules?enabled=true&cursor="+*first.NextCursor)
	if bound.Code != http.StatusBadRequest || jobErrorCode(t, bound) != "invalid_cursor" {
		t.Fatalf("filter cursor status=%d body=%s", bound.Code, bound.Body.String())
	}
	agentCursor, err := encodeControlCursor(webAgentCursorResource, "", &storage.CollectionPageKey{
		CreatedAt: time.Unix(300, 0).UnixMilli(), ID: controlScheduleA,
	})
	if err != nil {
		t.Fatal(err)
	}
	crossResource := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/schedules?cursor="+*agentCursor)
	if crossResource.Code != http.StatusBadRequest || jobErrorCode(t, crossResource) != "invalid_cursor" {
		t.Fatalf("cross-resource cursor status=%d body=%s", crossResource.Code, crossResource.Body.String())
	}
}

func TestWebScheduleTypedDetailAndReadOnlyBoundary(t *testing.T) {
	app, store, _ := newWebAgentTestApp(t)
	defer store.Close()
	expected := 204
	putControlSchedule(t, store, controlScheduleA, controlAgentA, "web", protocol.ProbeTypeHTTP,
		protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://secret.example/health", Method: "HEAD", ExpectedStatus: &expected}}, true, time.Unix(300, 0))
	putControlSchedule(t, store, controlScheduleB, controlAgentA, "tcp", protocol.ProbeTypeTCPConnect,
		protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "private.example", Port: 443}}, false, time.Unix(200, 0))
	putControlSchedule(t, store, controlScheduleC, webAgentC, "ping", protocol.ProbeTypeICMPPing,
		protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "192.0.2.1", Count: 2}}, true, time.Unix(100, 0))

	tests := []struct {
		id     string
		assert func(webScheduleConfigView) bool
	}{
		{controlScheduleA, func(config webScheduleConfigView) bool {
			return config.URL == "https://secret.example/health" && config.Method == "HEAD" && config.ExpectedStatus != nil && *config.ExpectedStatus == expected
		}},
		{controlScheduleB, func(config webScheduleConfigView) bool {
			return config.Host == "private.example" && config.Port == 443
		}},
		{controlScheduleC, func(config webScheduleConfigView) bool {
			return config.Target == "192.0.2.1" && config.Count == 2
		}},
	}
	for _, test := range tests {
		response := webAgentResponse(t, app, http.MethodGet, webSchedulePathPrefix+test.id)
		var detail webScheduleDetailView
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &detail) != nil || !test.assert(detail.Config) {
			t.Fatalf("schedule=%s status=%d body=%s", test.id, response.Code, response.Body.String())
		}
		assertNoWebScheduleSecrets(t, response.Body.String())
	}

	path := webSchedulePathPrefix + controlScheduleA
	for _, test := range []struct {
		method string
		path   string
		code   int
		error  string
	}{
		{http.MethodGet, path + "?x=1", http.StatusBadRequest, "invalid_query"},
		{http.MethodGet, webSchedulePathPrefix + "dddddddddddddddddddddddddddddddd", http.StatusNotFound, "schedule_not_found"},
		{http.MethodGet, webSchedulePathPrefix + "bad", http.StatusBadRequest, "invalid_request"},
		{http.MethodPost, "/api/v1/web/schedules", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodPut, path, http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodDelete, path, http.StatusMethodNotAllowed, "method_not_allowed"},
	} {
		response := webAgentResponse(t, app, test.method, test.path)
		if response.Code != test.code || jobErrorCode(t, response) != test.error || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("method=%s path=%q status=%d cache=%q body=%s", test.method, test.path, response.Code,
				response.Header().Get("Cache-Control"), response.Body.String())
		}
		if test.code == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("method=%s path=%q allow=%q", test.method, test.path, response.Header().Get("Allow"))
		}
	}
	if _, err := store.GetProbeSchedule(context.Background(), controlScheduleA); err != nil {
		t.Fatalf("read-only Web request changed schedule: %v", err)
	}
}

func TestSchedulePageUsesOnlyWebReadAPI(t *testing.T) {
	app, store, _ := newWebAgentTestApp(t)
	defer store.Close()
	page := webAgentResponse(t, app, http.MethodGet, "/schedules.html")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "/schedules.js") ||
		!strings.Contains(page.Body.String(), "/session.js") || !strings.Contains(page.Body.String(), "id=\"logout\"") {
		t.Fatalf("schedule page status=%d body=%s", page.Code, page.Body.String())
	}
	script := webAgentResponse(t, app, http.MethodGet, "/schedules.js")
	body := script.Body.String()
	if script.Code != http.StatusOK || !strings.Contains(body, "/api/v1/web/schedules") ||
		strings.Contains(body, "/api/v1/control/") || strings.Contains(body, "method: 'PUT'") ||
		strings.Contains(body, "method: 'POST'") || strings.Contains(body, "method: 'DELETE'") {
		t.Fatalf("schedule script status=%d body=%s", script.Code, body)
	}
	dashboard := webAgentResponse(t, app, http.MethodGet, "/")
	if dashboard.Code != http.StatusOK || !strings.Contains(dashboard.Body.String(), "/schedules.html") {
		t.Fatalf("dashboard status=%d body=%s", dashboard.Code, dashboard.Body.String())
	}
}

func decodeWebScheduleCollection(t *testing.T, response *httptest.ResponseRecorder, collection *webScheduleCollectionView) {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), collection); err != nil {
		t.Fatal(err)
	}
}

func assertWebScheduleIDs(t *testing.T, app *App, path string, want ...string) {
	t.Helper()
	response := webAgentResponse(t, app, http.MethodGet, path)
	var collection webScheduleCollectionView
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &collection) != nil || len(collection.Items) != len(want) {
		t.Fatalf("path=%q status=%d body=%s", path, response.Code, response.Body.String())
	}
	for index, id := range want {
		if collection.Items[index].ScheduleID != id {
			t.Fatalf("path=%q index=%d got=%s want=%s", path, index, collection.Items[index].ScheduleID, id)
		}
	}
}

func assertNoWebScheduleSecrets(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{"control_token", "token_hash", "lease_token", "result_hash", "agent_epoch", "session_id", "sequence", "boot_id"} {
		if strings.Contains(body, `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q leaked: %s", forbidden, body)
		}
	}
}
