package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	controlScheduleA = "11111111111111111111111111111111"
	controlScheduleB = "22222222222222222222222222222222"
	controlScheduleC = "33333333333333333333333333333333"
	controlAgentC    = "cccccccccccccccccccccccccccccccc"
)

func TestControlScheduleCollectionAuthenticationAndValidation(t *testing.T) {
	disabled, disabledStore, _, _ := testApp(t)
	defer disabledStore.Close()
	response := controlHTTPResponse(t, disabled, http.MethodGet, "/api/v1/control/schedules?unknown=x", "", "", nil)
	if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("disabled status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}

	app, store, _, agentToken, controlToken := newControlTestApp(t)
	defer store.Close()
	for _, token := range []string{"", "wrong", agentToken} {
		response = controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/schedules?unknown=x", token, "", nil)
		if response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "unauthorized" ||
			response.Header().Get("WWW-Authenticate") != "Bearer" || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("token=%q status=%d headers=%v body=%s", token, response.Code, response.Header(), response.Body.String())
		}
	}

	for _, suffix := range []string{
		"?unknown=x", "?agent_id=", "?agent_id=bad", "?agent_id=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"?agent_id=" + controlAgentC + "&agent_id=" + controlAgentC,
		"?enabled=", "?enabled=1", "?enabled=TRUE", "?enabled=true&enabled=false",
		"?probe_type=", "?probe_type=dns", "?probe_type=http&probe_type=tcp_connect",
		"?limit=0", "?limit=101", "?cursor=x",
	} {
		response = controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/schedules"+suffix, controlToken, "", nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query=%q status=%d body=%s", suffix, response.Code, response.Body.String())
		}
		code := jobErrorCode(t, response)
		if code != "invalid_query" && code != "invalid_cursor" {
			t.Fatalf("query=%q code=%q body=%s", suffix, code, response.Body.String())
		}
	}
}

func TestControlScheduleCollectionPaginationAndFilters(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	addControlAgent(t, store, controlAgentC, "other", time.Unix(50, 0))
	putControlSchedule(t, store, controlScheduleA, agentID, "web", protocol.ProbeTypeHTTP,
		protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://secret.example/health", Method: "HEAD"}}, true, time.Unix(300, 0))
	putControlSchedule(t, store, controlScheduleB, agentID, "tcp", protocol.ProbeTypeTCPConnect,
		protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "private.example", Port: 443}}, false, time.Unix(200, 0))
	putControlSchedule(t, store, controlScheduleC, controlAgentC, "ping", protocol.ProbeTypeICMPPing,
		protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "192.0.2.1", Count: 2}}, true, time.Unix(100, 0))

	first := getControlScheduleCollection(t, app, "/api/v1/control/schedules?limit=1", controlToken)
	if len(first.Items) != 1 || first.Items[0].ScheduleID != controlScheduleA || first.NextCursor == nil ||
		!strings.Contains(string(first.Items[0].Config), "secret.example") {
		t.Fatalf("first=%+v", first)
	}
	second := getControlScheduleCollection(t, app, "/api/v1/control/schedules?limit=1&cursor="+*first.NextCursor, controlToken)
	if len(second.Items) != 1 || second.Items[0].ScheduleID != controlScheduleB || second.NextCursor == nil {
		t.Fatalf("second=%+v", second)
	}
	third := getControlScheduleCollection(t, app, "/api/v1/control/schedules?limit=2&cursor="+*second.NextCursor, controlToken)
	if len(third.Items) != 1 || third.Items[0].ScheduleID != controlScheduleC || third.NextCursor != nil {
		t.Fatalf("third=%+v", third)
	}

	byAgent := getControlScheduleCollection(t, app, "/api/v1/control/schedules?agent_id="+agentID, controlToken)
	if len(byAgent.Items) != 2 || byAgent.Items[0].ScheduleID != controlScheduleA || byAgent.Items[1].ScheduleID != controlScheduleB {
		t.Fatalf("by agent=%+v", byAgent)
	}
	disabled := getControlScheduleCollection(t, app, "/api/v1/control/schedules?enabled=false", controlToken)
	if len(disabled.Items) != 1 || disabled.Items[0].ScheduleID != controlScheduleB {
		t.Fatalf("disabled=%+v", disabled)
	}
	httpOnly := getControlScheduleCollection(t, app, "/api/v1/control/schedules?probe_type=http", controlToken)
	if len(httpOnly.Items) != 1 || httpOnly.Items[0].ScheduleID != controlScheduleA {
		t.Fatalf("http=%+v", httpOnly)
	}
	combined := getControlScheduleCollection(t, app,
		"/api/v1/control/schedules?enabled=true&agent_id="+controlAgentC+"&probe_type=icmp_ping", controlToken)
	if len(combined.Items) != 1 || combined.Items[0].ScheduleID != controlScheduleC {
		t.Fatalf("combined=%+v", combined)
	}

	response := controlHTTPResponse(t, app, http.MethodGet,
		"/api/v1/control/schedules?enabled=true&cursor="+*first.NextCursor, controlToken, "", nil)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_cursor" {
		t.Fatalf("filter cursor status=%d body=%s", response.Code, response.Body.String())
	}
	agentCursor, err := encodeControlCursor(controlAgentCursorResource, "", &storage.CollectionPageKey{
		CreatedAt: time.Unix(300, 0).UnixMilli(), ID: controlScheduleA,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/schedules?cursor="+*agentCursor, controlToken, "", nil)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_cursor" {
		t.Fatalf("resource cursor status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestControlScheduleCollectionMethodAndDetailQuery(t *testing.T) {
	app, store, agentID, _, controlToken := newControlTestApp(t)
	defer store.Close()
	putControlSchedule(t, store, controlScheduleA, agentID, "web", protocol.ProbeTypeHTTP,
		protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com", Method: "GET"}}, true, time.Unix(300, 0))

	response := controlHTTPResponse(t, app, http.MethodPost, "/api/v1/control/schedules", controlToken, "", nil)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet ||
		response.Header().Get("Cache-Control") != "no-store" || jobErrorCode(t, response) != "method_not_allowed" {
		t.Fatalf("collection method status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	response = controlHTTPResponse(t, app, http.MethodGet, controlSchedulePathPrefix+controlScheduleA+"?unknown=x", controlToken, "", nil)
	if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" || jobErrorCode(t, response) != "invalid_query" {
		t.Fatalf("detail query status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func putControlSchedule(t *testing.T, store *storage.Store, id, agentID, name string, probeType protocol.ProbeType,
	config protocol.ProbeConfig, enabled bool, now time.Time) {
	t.Helper()
	if _, created, err := store.PutProbeSchedule(context.Background(), storage.PutScheduleParams{
		ID: id, AgentID: agentID, Name: name, ProbeType: probeType, Config: config,
		TimeoutMS: 5000, IntervalSeconds: 60, Enabled: enabled, Now: now.UnixMilli(),
	}); err != nil || !created {
		t.Fatalf("put schedule created=%t error=%v", created, err)
	}
}

func getControlScheduleCollection(t *testing.T, app *App, path, token string) controlScheduleCollectionView {
	t.Helper()
	response := controlHTTPResponse(t, app, http.MethodGet, path, token, "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("path=%q status=%d cache=%q body=%s", path, response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var collection controlScheduleCollectionView
	if err := json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	return collection
}
