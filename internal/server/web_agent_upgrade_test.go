package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func webUpgradeResponse(t *testing.T, app *App, agentID string, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/web/agents/"+agentID+"/upgrade", strings.NewReader(body))
	session := addTestWebSession(t, app, request)
	request.Header.Set("Origin", "https://probe.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", session.csrfToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func makeUpgradeableAgent(t *testing.T, app *App, store *storage.Store, agentID string) {
	t.Helper()
	report := reportFor(agentID, 1)
	report.AgentVersion = "v0.8.0"
	report.AgentUpgradeCapable = true
	_, accepted, reason, err := store.ProcessReport(context.Background(), agentID, report, app.now())
	if err != nil || !accepted {
		t.Fatalf("report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
}

func TestWebAgentUpgradeCreatesOneTypedOperation(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	app.buildInfo = buildinfo.Info{Version: "v0.8.1", Commit: strings.Repeat("a", 40)}
	makeUpgradeableAgent(t, app, store, agentID)

	response := webUpgradeResponse(t, app, agentID, `{}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var created webUpgradeView
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.OperationID == "" || created.FromVersion != "v0.8.0" || created.TargetVersion != "v0.8.1" || created.Status != storage.UpgradeRequested {
		t.Fatalf("created=%+v", created)
	}
	duplicate := webUpgradeResponse(t, app, agentID, `{}`)
	if duplicate.Code != http.StatusConflict || jobErrorCode(t, duplicate) != "operation_conflict" {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
}

func TestWebAgentUpgradeSafetyGates(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*App, *storage.Store, string)
		code    string
	}{
		{"offline", func(_ *App, _ *storage.Store, _ string) {}, "agent_offline"},
		{"bootstrap required", func(app *App, store *storage.Store, id string) {
			report := reportFor(id, 1)
			report.AgentVersion = "v0.8.0"
			_, _, _, _ = store.ProcessReport(context.Background(), id, report, app.now())
		}, "bootstrap_required"},
		{"paused", func(app *App, store *storage.Store, id string) {
			makeUpgradeableAgent(t, app, store, id)
			_, _ = store.DisableAgent(context.Background(), id, app.now())
		}, "agent_paused"},
		{"revoked", func(app *App, store *storage.Store, id string) {
			makeUpgradeableAgent(t, app, store, id)
			_, _ = store.RevokeAgent(context.Background(), id, app.now())
		}, "agent_revoked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, store, agentID := newWebAuthenticationTestApp(t)
			defer store.Close()
			app.buildInfo = buildinfo.Info{Version: "v0.8.1", Commit: strings.Repeat("a", 40)}
			test.prepare(app, store, agentID)
			response := webUpgradeResponse(t, app, agentID, `{}`)
			if response.Code != http.StatusConflict || jobErrorCode(t, response) != test.code {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAgentUpgradeClaimAndStatusAreAuthenticatedAndOwned(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Unix(2_000_000_000, 0)
	app.now = func() time.Time { return now }
	operationID := "0123456789abcdef0123456789abcdef"
	if _, err := store.CreateUpgrade(context.Background(), storage.UpgradeOperation{OperationID: operationID, AgentID: agentID, FromVersion: "v0.8.0", TargetVersion: "v0.8.1", CreatedAt: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	claim := postAgentUpgrade(t, app, token, "/api/v1/agent/upgrades/claim", protocol.UpgradeClaimRequest{ProtocolVersion: protocol.UpgradeProtocolVersion})
	if claim.Code != http.StatusOK || !strings.Contains(claim.Body.String(), operationID) {
		t.Fatalf("claim status=%d body=%s", claim.Code, claim.Body.String())
	}
	status := postAgentUpgrade(t, app, token, "/api/v1/agent/upgrades/"+operationID+"/status", protocol.UpgradeStatusRequest{ProtocolVersion: protocol.UpgradeProtocolVersion, Status: string(storage.UpgradeDownloading)})
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), string(storage.UpgradeDownloading)) {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}
	unauthorized := postAgentUpgrade(t, app, "wrong", "/api/v1/agent/upgrades/claim", protocol.UpgradeClaimRequest{ProtocolVersion: protocol.UpgradeProtocolVersion})
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
}

func postAgentUpgrade(t *testing.T, app *App, token, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}
