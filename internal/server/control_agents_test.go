package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	controlAgentA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	controlAgentB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestControlAgentsAuthenticationAndQueryValidation(t *testing.T) {
	disabled, disabledStore, _, _ := testApp(t)
	defer disabledStore.Close()
	response := controlHTTPResponse(t, disabled, http.MethodGet, "/api/v1/control/agents?unknown=x", "", "", nil)
	if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("disabled status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}

	app, store, _, agentToken, controlToken := newControlTestApp(t)
	defer store.Close()
	for _, token := range []string{"", "wrong", agentToken} {
		response = controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/agents?unknown=x", token, "", nil)
		if response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "unauthorized" ||
			response.Header().Get("WWW-Authenticate") != "Bearer" || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("token=%q status=%d auth=%q cache=%q body=%s", token, response.Code,
				response.Header().Get("WWW-Authenticate"), response.Header().Get("Cache-Control"), response.Body.String())
		}
	}

	tests := []string{
		"?unknown=x", "?status=", "?status=active", "?status=online&status=offline", "?limit=", "?limit=0",
		"?limit=101", "?limit=x", "?limit=1&limit=2", "?cursor=", "?cursor=x", "?cursor=x&cursor=y",
	}
	for _, suffix := range tests {
		response = controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/agents"+suffix, controlToken, "", nil)
		if response.Code != http.StatusBadRequest || (jobErrorCode(t, response) != "invalid_query" && jobErrorCode(t, response) != "invalid_cursor") {
			t.Fatalf("query=%q status=%d body=%s", suffix, response.Code, response.Body.String())
		}
	}
}

func TestControlAgentsPaginationFilteringAndCursorBinding(t *testing.T) {
	app, store, _, _, controlToken := newControlTestApp(t)
	defer store.Close()
	addControlAgent(t, store, controlAgentA, "alpha", time.Unix(300, 0))
	addControlAgent(t, store, controlAgentB, "beta", time.Unix(200, 0))
	now := time.Unix(400, 0)
	app.now = func() time.Time { return now }
	processControlAgentReport(t, store, controlAgentA, now.Add(-10*time.Second))
	if _, err := store.RevokeAgent(context.Background(), controlAgentB, now.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}

	first := getControlAgentCollection(t, app, "/api/v1/control/agents?limit=1", controlToken)
	if len(first.Items) != 1 || first.Items[0].AgentID != controlAgentA || first.NextCursor == nil {
		t.Fatalf("first page=%+v", first)
	}
	second := getControlAgentCollection(t, app, "/api/v1/control/agents?limit=1&cursor="+*first.NextCursor, controlToken)
	if len(second.Items) != 1 || second.Items[0].AgentID != controlAgentB || second.NextCursor == nil {
		t.Fatalf("second page=%+v", second)
	}
	third := getControlAgentCollection(t, app, "/api/v1/control/agents?limit=2&cursor="+*second.NextCursor, controlToken)
	if len(third.Items) != 1 || third.Items[0].AgentID == controlAgentA || third.Items[0].AgentID == controlAgentB || third.NextCursor != nil {
		t.Fatalf("third page=%+v", third)
	}

	online := getControlAgentCollection(t, app, "/api/v1/control/agents?status=online", controlToken)
	if len(online.Items) != 1 || online.Items[0].AgentID != controlAgentA || !online.Items[0].Online || online.Items[0].LastSeen == nil {
		t.Fatalf("online=%+v", online)
	}
	revoked := getControlAgentCollection(t, app, "/api/v1/control/agents?status=revoked", controlToken)
	if len(revoked.Items) != 1 || revoked.Items[0].AgentID != controlAgentB || !revoked.Items[0].Revoked || revoked.Items[0].Online {
		t.Fatalf("revoked=%+v", revoked)
	}
	offline := getControlAgentCollection(t, app, "/api/v1/control/agents?status=offline", controlToken)
	if len(offline.Items) != 1 || offline.Items[0].AgentID == controlAgentA || offline.Items[0].AgentID == controlAgentB {
		t.Fatalf("offline=%+v", offline)
	}

	response := controlHTTPResponse(t, app, http.MethodGet,
		"/api/v1/control/agents?status=online&cursor="+*first.NextCursor, controlToken, "", nil)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_cursor" {
		t.Fatalf("filter-bound cursor status=%d body=%s", response.Code, response.Body.String())
	}
	badCursor := base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"resource":"agents","filters":"","created_at":1,"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	response = controlHTTPResponse(t, app, http.MethodGet, "/api/v1/control/agents?cursor="+badCursor, controlToken, "", nil)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_cursor" {
		t.Fatalf("version cursor status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestControlAgentDetailWhitelistAndRouting(t *testing.T) {
	app, store, _, _, controlToken := newControlTestApp(t)
	defer store.Close()
	now := time.Unix(400, 0)
	app.now = func() time.Time { return now }
	addControlAgent(t, store, controlAgentA, "alpha", time.Unix(300, 0))
	processControlAgentReport(t, store, controlAgentA, now.Add(-10*time.Second))

	path := controlAgentPathPrefix + controlAgentA
	response := controlHTTPResponse(t, app, http.MethodGet, path, controlToken, "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("detail status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	state, ok := detail["state"].(map[string]any)
	if !ok || state["hostname"] != "host" || state["country_code"] != "US" || detail["agent_id"] != controlAgentA || detail["online"] != true {
		t.Fatalf("detail=%s", response.Body.String())
	}
	for _, forbidden := range []string{"token", "token_hash", "epoch", "session_id", "sequence", "boot_id", "raw_rx", "raw_tx", "lease_token", "result_hash"} {
		if strings.Contains(response.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("forbidden field %q leaked: %s", forbidden, response.Body.String())
		}
	}

	for _, test := range []struct {
		method string
		path   string
		code   int
		error  string
	}{
		{http.MethodGet, path + "?x=1", http.StatusBadRequest, "invalid_query"},
		{http.MethodGet, controlAgentPathPrefix + "cccccccccccccccccccccccccccccccc", http.StatusNotFound, "agent_not_found"},
		{http.MethodGet, controlAgentPathPrefix + "bad", http.StatusBadRequest, "invalid_request"},
		{http.MethodPost, path, http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodPost, "/api/v1/control/agents", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodGet, "/api/v1/control/agents//" + controlAgentA, http.StatusBadRequest, "invalid_request"},
	} {
		response = controlHTTPResponse(t, app, test.method, test.path, controlToken, "", nil)
		if response.Code != test.code || jobErrorCode(t, response) != test.error || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("method=%s path=%q status=%d cache=%q body=%s", test.method, test.path, response.Code,
				response.Header().Get("Cache-Control"), response.Body.String())
		}
	}
}

func addControlAgent(t *testing.T, store *storage.Store, id, name string, createdAt time.Time) {
	t.Helper()
	if err := store.AddAgent(context.Background(), id, name, auth.Hash("agent-"+id), createdAt); err != nil {
		t.Fatal(err)
	}
}

func processControlAgentReport(t *testing.T, store *storage.Store, id string, receivedAt time.Time) {
	t.Helper()
	steal, diskRead, diskWrite, diskBusy := 3.0, 4096.0, 2048.0, 72.0
	report := protocol.Report{
		AgentID: id, Epoch: 1, SessionID: "session", Sequence: 1, CollectedAt: receivedAt.UnixMilli(),
		Hostname: "host", OS: "linux", Arch: "amd64", BootID: "boot", Uptime: 10,
		CPUPercent: 5, Load1: 1, Load5: 2, Load15: 3, RAMUsed: 10, RAMTotal: 20, RAMPercent: 50,
		SwapUsed: 1, SwapTotal: 2, SwapPercent: 50, DiskUsed: 30, DiskTotal: 60, DiskPercent: 50,
		CPUStealPercent: &steal, DiskReadRate: &diskRead, DiskWriteRate: &diskWrite, DiskBusyPercent: &diskBusy, CountryCode: "US",
		RXBytes: 100, TXBytes: 200,
	}
	if _, accepted, reason, err := store.ProcessReport(context.Background(), id, report, receivedAt); err != nil || !accepted {
		t.Fatalf("process report accepted=%t reason=%q error=%v", accepted, reason, err)
	}
}

func getControlAgentCollection(t *testing.T, app *App, path, token string) controlAgentCollectionView {
	t.Helper()
	response := controlHTTPResponse(t, app, http.MethodGet, path, token, "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("path=%q status=%d cache=%q body=%s", path, response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var collection controlAgentCollectionView
	if err := json.Unmarshal(response.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	return collection
}
