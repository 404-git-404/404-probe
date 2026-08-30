package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func webAgentRevokeResponse(t *testing.T, app *App, agentID, body string, configure func(*http.Request, *webSession)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, webAgentPathPrefix+agentID+"/revoke", strings.NewReader(body))
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

func TestWebAgentRevokeMutationBoundary(t *testing.T) {
	app, store, agentID, _ := testApp(t)
	defer store.Close()
	path := webAgentPathPrefix + agentID + "/revoke"

	unauthenticated := webRequest(app, http.MethodPost, path, strings.NewReader(`{"unexpected":true}`), nil)
	if unauthenticated.Code != http.StatusUnauthorized || jobErrorCode(t, unauthenticated) != "unauthorized" {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	invalidSession := webAgentRevokeResponse(t, app, agentID, `{}`, func(r *http.Request, _ *webSession) {
		r.Header.Set("Cookie", app.webAuth.sessionCookieName()+"=invalid")
	})
	if invalidSession.Code != http.StatusUnauthorized || jobErrorCode(t, invalidSession) != "unauthorized" {
		t.Fatalf("invalid session status=%d body=%s", invalidSession.Code, invalidSession.Body.String())
	}

	tests := []struct {
		name      string
		agentID   string
		body      string
		configure func(*http.Request, *webSession)
		status    int
		code      string
	}{
		{"missing csrf", agentID, `{}`, func(r *http.Request, _ *webSession) { r.Header.Del("X-CSRF-Token") }, http.StatusForbidden, "forbidden"},
		{"wrong origin", agentID, `{}`, func(r *http.Request, _ *webSession) { r.Header.Set("Origin", "https://attacker.test") }, http.StatusForbidden, "forbidden"},
		{"missing fetch site", agentID, `{}`, func(r *http.Request, _ *webSession) { r.Header.Del("Sec-Fetch-Site") }, http.StatusForbidden, "forbidden"},
		{"wrong content type", agentID, `{}`, func(r *http.Request, _ *webSession) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType, "invalid_content_type"},
		{"query", agentID, `{}`, func(r *http.Request, _ *webSession) { r.URL.RawQuery = "x=1" }, http.StatusBadRequest, "invalid_query"},
		{"malformed", agentID, `{`, nil, http.StatusBadRequest, "invalid_request"},
		{"null", agentID, `null`, nil, http.StatusBadRequest, "invalid_request"},
		{"array", agentID, `[]`, nil, http.StatusBadRequest, "invalid_request"},
		{"unknown field", agentID, `{"delete":true}`, nil, http.StatusBadRequest, "invalid_request"},
		{"multiple values", agentID, `{}{}`, nil, http.StatusBadRequest, "invalid_request"},
		{"oversized", agentID, strings.Repeat("x", maxWebAgentRevokeBodyBytes+1), nil, http.StatusRequestEntityTooLarge, "request_too_large"},
		{"bad agent id", "bad", `{}`, nil, http.StatusBadRequest, "invalid_request"},
		{"missing agent", strings.Repeat("d", 32), `{}`, nil, http.StatusNotFound, "agent_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := webAgentRevokeResponse(t, app, test.agentID, test.body, test.configure)
			if response.Code != test.status || jobErrorCode(t, response) != test.code || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}

	method := webAgentResponse(t, app, http.MethodGet, path)
	if method.Code != http.StatusMethodNotAllowed || jobErrorCode(t, method) != "method_not_allowed" || method.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("method status=%d allow=%q body=%s", method.Code, method.Header().Get("Allow"), method.Body.String())
	}
}

func TestWebAgentRevokePreservesHistorySchedulesJobsAndResults(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	ctx := context.Background()
	now := time.Unix(2_000, 0)
	app.now = func() time.Time { return now }

	report := reportFor(agentID, 1)
	report.CollectedAt = now.Add(-10 * time.Second).UnixMilli()
	if response := postReport(t, app, token, report); response.Code != http.StatusOK {
		t.Fatalf("report status=%d body=%s", response.Code, response.Body.String())
	}
	config := protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}}
	if _, _, err := store.PutProbeSchedule(ctx, storage.PutScheduleParams{
		ID: "schedule", AgentID: agentID, Name: "retained", ProbeType: protocol.ProbeTypeTCPConnect,
		Config: config, TimeoutMS: 1_000, IntervalSeconds: 60, Enabled: true, Now: now.Add(-time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateOneShotJob(ctx, storage.CreateOneShotJobParams{
		ID: "job", AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect, Config: config,
		TimeoutMS: 1_000, CreatedAt: now.Add(-9 * time.Second).UnixMilli(),
		NotBefore: now.Add(-9 * time.Second).UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimJob(ctx, agentID, protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: 1, SessionID: "session",
		SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
	}, now.Add(-8*time.Second), time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim job=%+v err=%v", job, err)
	}
	if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, protocol.JobResult{
		ProtocolVersion: protocol.JobProtocolVersion, LeaseToken: job.LeaseToken, Attempt: job.Attempt,
		AgentEpoch: 1, SessionID: "session", StartedAt: now.Add(-7 * time.Second).UnixMilli(),
		FinishedAt: now.Add(-6 * time.Second).UnixMilli(), DurationMS: 1_000, Success: true,
		Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{ConnectMS: 10}},
	}, now.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}

	response := webAgentRevokeResponse(t, app, agentID, `{}`, nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("revoke status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var revoked webAgentRevokeView
	if err := json.Unmarshal(response.Body.Bytes(), &revoked); err != nil {
		t.Fatal(err)
	}
	if revoked.Agent.AgentID != agentID || !revoked.Agent.Revoked || revoked.Agent.Online || revoked.Agent.LastSeen == nil {
		t.Fatalf("revoked=%+v", revoked)
	}
	if _, err := store.Authenticate(ctx, token); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatalf("revoked credential error=%v", err)
	}
	if response := postReport(t, app, token, reportFor(agentID, 2)); response.Code != http.StatusUnauthorized ||
		jobErrorCode(t, response) != "agent_revoked" {
		t.Fatalf("revoked report status=%d body=%s", response.Code, response.Body.String())
	}

	active := getWebAgentCollection(t, app, "/api/v1/web/agents?limit=100")
	if len(active.Items) != 0 {
		t.Fatalf("active agents=%+v", active.Items)
	}
	revokedList := getWebAgentCollection(t, app, "/api/v1/web/agents?status=revoked")
	if len(revokedList.Items) != 1 || revokedList.Items[0].AgentID != agentID {
		t.Fatalf("revoked agents=%+v", revokedList.Items)
	}
	if detail := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+agentID); detail.Code != http.StatusOK {
		t.Fatalf("retained detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	if history := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+agentID+"/history?hours=1"); history.Code != http.StatusOK {
		t.Fatalf("retained history status=%d body=%s", history.Code, history.Body.String())
	}
	if schedules, err := store.ListProbeSchedules(ctx, agentID); err != nil || len(schedules) != 1 || !schedules[0].Enabled {
		t.Fatalf("retained schedules=%+v err=%v", schedules, err)
	}
	if storedJob, err := store.GetProbeJob(ctx, "job"); err != nil || storedJob.Status != storage.JobStatusFinished {
		t.Fatalf("retained job=%+v err=%v", storedJob, err)
	}
	if result, err := store.GetProbeResult(ctx, "job"); err != nil || result.JobID != "job" {
		t.Fatalf("retained result=%+v err=%v", result, err)
	}

	replay := webAgentRevokeResponse(t, app, agentID, `{}`, nil)
	if replay.Code != http.StatusOK {
		t.Fatalf("idempotent revoke status=%d body=%s", replay.Code, replay.Body.String())
	}
}
