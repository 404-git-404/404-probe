package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func webAgentStateResponse(t *testing.T, app *App, agentID, action string, configure func(*http.Request, *webSession)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, webAgentPathPrefix+agentID+"/"+action, strings.NewReader(`{}`))
	session := addTestWebSession(t, app, request)
	request.Header.Set("Origin", "https://probe.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", session.csrfToken)
	request.Header.Set("Content-Type", "application/json")
	if configure != nil {
		configure(request, session)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestWebAgentDisableEnableLifecycleAndBoundary(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Unix(2_000, 0)
	app.now = func() time.Time { return now }
	if response := postReport(t, app, token, reportFor(agentID, 1)); response.Code != http.StatusOK {
		t.Fatalf("initial report status=%d body=%s", response.Code, response.Body.String())
	}

	for _, action := range []string{"disable", "enable"} {
		response := webAgentStateResponse(t, app, agentID, action, func(r *http.Request, _ *webSession) {
			r.Header.Del("X-CSRF-Token")
		})
		if response.Code != http.StatusForbidden || jobErrorCode(t, response) != "forbidden" {
			t.Fatalf("%s without csrf status=%d body=%s", action, response.Code, response.Body.String())
		}
	}

	disable := webAgentStateResponse(t, app, agentID, "disable", nil)
	var disabled webAgentRevokeView
	if disable.Code != http.StatusOK || json.Unmarshal(disable.Body.Bytes(), &disabled) != nil ||
		disabled.Agent.DisabledAt == nil || disabled.Agent.Online || disabled.Agent.Revoked {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	if *disabled.Agent.DisabledAt != now.UnixMilli() {
		t.Fatalf("disabled_at=%d want=%d", *disabled.Agent.DisabledAt, now.UnixMilli())
	}
	pausedDetailResponse := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+agentID)
	var pausedDetail webAgentDetailView
	if pausedDetailResponse.Code != http.StatusOK || json.Unmarshal(pausedDetailResponse.Body.Bytes(), &pausedDetail) != nil ||
		pausedDetail.State == nil || !pausedDetail.State.Stale || pausedDetail.State.RXRate != 0 || pausedDetail.State.TXRate != 0 {
		t.Fatalf("paused detail status=%d body=%s", pausedDetailResponse.Code, pausedDetailResponse.Body.String())
	}
	if response := postReport(t, app, token, reportFor(agentID, 2)); response.Code != http.StatusLocked || jobErrorCode(t, response) != "agent_disabled" {
		t.Fatalf("disabled report status=%d body=%s", response.Code, response.Body.String())
	}
	active := getWebAgentCollection(t, app, "/api/v1/web/agents?limit=100")
	if len(active.Items) != 1 || active.Items[0].DisabledAt == nil {
		t.Fatalf("active fleet excludes disabled agent: %+v", active.Items)
	}
	paused := getWebAgentCollection(t, app, "/api/v1/web/agents?status=disabled")
	if len(paused.Items) != 1 || paused.Items[0].AgentID != agentID {
		t.Fatalf("disabled agents=%+v", paused.Items)
	}

	if replay := webAgentStateResponse(t, app, agentID, "disable", nil); replay.Code != http.StatusOK {
		t.Fatalf("idempotent disable status=%d body=%s", replay.Code, replay.Body.String())
	}
	now = now.Add(time.Minute)
	enable := webAgentStateResponse(t, app, agentID, "enable", nil)
	var enabled webAgentRevokeView
	if enable.Code != http.StatusOK || json.Unmarshal(enable.Body.Bytes(), &enabled) != nil || enabled.Agent.DisabledAt != nil {
		t.Fatalf("enable status=%d body=%s", enable.Code, enable.Body.String())
	}
	awaitingDetailResponse := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+agentID)
	var awaitingDetail webAgentDetailView
	if awaitingDetailResponse.Code != http.StatusOK || json.Unmarshal(awaitingDetailResponse.Body.Bytes(), &awaitingDetail) != nil ||
		awaitingDetail.State == nil || !awaitingDetail.State.Stale || awaitingDetail.State.RXRate != 0 || awaitingDetail.State.TXRate != 0 {
		t.Fatalf("resumed detail before fresh sample status=%d body=%s", awaitingDetailResponse.Code, awaitingDetailResponse.Body.String())
	}
	if response := postReport(t, app, token, reportFor(agentID, 2)); response.Code != http.StatusOK {
		t.Fatalf("resumed report status=%d body=%s", response.Code, response.Body.String())
	}
	freshDetailResponse := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+agentID)
	var freshDetail webAgentDetailView
	if freshDetailResponse.Code != http.StatusOK || json.Unmarshal(freshDetailResponse.Body.Bytes(), &freshDetail) != nil ||
		freshDetail.State == nil || freshDetail.State.Stale {
		t.Fatalf("resumed detail after fresh sample status=%d body=%s", freshDetailResponse.Code, freshDetailResponse.Body.String())
	}
}

func TestDisabledAgentCanBeRevokedButNeverEnabled(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	if response := webAgentStateResponse(t, app, agentID, "disable", nil); response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	if response := webAgentRevokeResponse(t, app, agentID, `{}`, nil); response.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", response.Code, response.Body.String())
	}
	response := webAgentStateResponse(t, app, agentID, "enable", nil)
	if response.Code != http.StatusConflict || jobErrorCode(t, response) != "agent_revoked" {
		t.Fatalf("enable revoked status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := store.Authenticate(context.Background(), token); err == nil {
		t.Fatal("revoked credential authenticated")
	}
}
