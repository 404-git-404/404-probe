package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func postOutbounds(t *testing.T, app *App, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/outbounds", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestAgentOutboundEndpointAuthBoundsAndWebReadOnlyView(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	app.now = func() time.Time { return time.Unix(300, 0) }
	payload, _ := json.Marshal(protocol.OutboundSnapshot{Available: true, Status: protocol.OutboundStatusConnected, Selectors: []protocol.OutboundSelector{{Name: "select", Current: "b", Choices: []string{"a", "b"}}}})
	if response := postOutbounds(t, app, token, payload); response.Code != http.StatusOK {
		t.Fatalf("active status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postOutbounds(t, app, "wrong", payload); response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "unauthorized" {
		t.Fatalf("unknown status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postOutbounds(t, app, token, []byte(`{"available":false,"selectors":[],"extra":true}`)); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postOutbounds(t, app, token, bytes.Repeat([]byte(" "), protocol.MaxOutboundSnapshotBytes+1)); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large status=%d body=%s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/web/agents/"+agentID, nil)
	addTestWebSession(t, app, request)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"configured":true`) || !strings.Contains(response.Body.String(), `"status":"connected"`) || !strings.Contains(response.Body.String(), `"name":"select"`) {
		t.Fatalf("web status=%d body=%s", response.Code, response.Body.String())
	}
	app.now = func() time.Time { return time.Unix(300, 0).Add(outboundSnapshotStaleAfter + time.Second) }
	staleRequest := httptest.NewRequest(http.MethodGet, "/api/v1/web/agents/"+agentID, nil)
	addTestWebSession(t, app, staleRequest)
	staleResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusOK || !strings.Contains(staleResponse.Body.String(), `"stale":true`) {
		t.Fatalf("stale status=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
	if response := webRequest(app, http.MethodPost, "/api/v1/web/agents/"+agentID, strings.NewReader(`{}`), nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated Web mutation status=%d", response.Code)
	}
	if changed, err := store.DisableAgent(context.Background(), agentID, time.Now()); err != nil || !changed {
		t.Fatalf("disable=%t err=%v", changed, err)
	}
	if response := postOutbounds(t, app, token, payload); response.Code != http.StatusLocked || jobErrorCode(t, response) != "agent_disabled" {
		t.Fatalf("disabled status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentOutboundEndpointRevokedSignal(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	if changed, err := store.RevokeAgent(context.Background(), agentID, time.Now()); err != nil || !changed {
		t.Fatalf("revoke=%t err=%v", changed, err)
	}
	response := postOutbounds(t, app, token, []byte(`{"available":false,"selectors":[]}`))
	if response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "agent_revoked" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
