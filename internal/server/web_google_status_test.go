package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func googleStatusWebResponse(t *testing.T, app *App, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/web/agents/"+agentID+"/google-status", bytes.NewBufferString("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://probe.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	session := addTestWebSession(t, app, request)
	request.Header.Set("X-CSRF-Token", session.csrfToken)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestWebGoogleStatusManualTriggerAndGates(t *testing.T) {
	app, store, agentID, _ := testApp(t)
	defer store.Close()
	at := time.Unix(40_000, 0)
	app.now = func() time.Time { return at }
	markSelectorAgentOnline(t, store, agentID, at)
	if response := googleStatusWebResponse(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "google_status_unsupported" {
		t.Fatalf("unsupported status=%d body=%s", response.Code, response.Body.String())
	}
	if ok, err := store.NegotiateGoogleStatusCapability(context.Background(), agentID, true, 1, "session", at); err != nil || !ok {
		t.Fatalf("negotiate=%t err=%v", ok, err)
	}
	if response := googleStatusWebResponse(t, app, agentID); response.Code != http.StatusAccepted {
		t.Fatalf("created status=%d body=%s", response.Code, response.Body.String())
	}
	if response := googleStatusWebResponse(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "google_status_pending" {
		t.Fatalf("pending status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebGoogleStatusOfflineAndPausedRejected(t *testing.T) {
	app, store, agentID, _ := testApp(t)
	defer store.Close()
	at := time.Unix(50_000, 0)
	app.now = func() time.Time { return at }
	seen := at.Add(-2 * time.Minute)
	markSelectorAgentOnline(t, store, agentID, seen)
	if ok, err := store.NegotiateGoogleStatusCapability(context.Background(), agentID, true, 1, "session", seen); err != nil || !ok {
		t.Fatalf("negotiate=%t err=%v", ok, err)
	}
	if response := googleStatusWebResponse(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "agent_offline" {
		t.Fatalf("offline status=%d body=%s", response.Code, response.Body.String())
	}
	if changed, err := store.DisableAgent(context.Background(), agentID, at); err != nil || !changed {
		t.Fatalf("disable=%t err=%v", changed, err)
	}
	if response := googleStatusWebResponse(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "agent_disabled" {
		t.Fatalf("paused status=%d body=%s", response.Code, response.Body.String())
	}
}
