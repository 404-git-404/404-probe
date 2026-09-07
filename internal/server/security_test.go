package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func serverSecurityBatch(now time.Time) protocol.SecurityBatch {
	start := now.Add(-time.Hour).UnixMilli()
	end := now.Add(-time.Minute).UnixMilli()
	return protocol.SecurityBatch{BatchID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WindowStart: start, WindowEnd: end, CollectedAt: now.UnixMilli(), Status: protocol.SecurityStatusComplete,
		TotalEvents: 5, TrackedSources: 1, ScannedLines: 6, ScannedBytes: 512,
		Sources: []protocol.SecuritySource{{IP: "192.0.2.4", Count: 5, FirstSeen: start, LastSeen: end, DurationMS: end - start, Classifications: []protocol.SecurityClassification{protocol.SecurityRepeated}}}}
}

func postSecurityRequest(app *App, token string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/security", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestSecurityEndpointNegotiationStrictDecodeFenceAndWebRedaction(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Now().Truncate(time.Second)
	app.now = func() time.Time { return now }
	report := reportFor(agentID, 1)
	report.CollectedAt = now.UnixMilli()
	reportResponse := postReport(t, app, token, report)
	if reportResponse.Code != http.StatusOK || !strings.Contains(reportResponse.Body.String(), `"security":true`) {
		t.Fatalf("report status=%d body=%s", reportResponse.Code, reportResponse.Body.String())
	}
	batch := serverSecurityBatch(now)
	submission := protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: 1, SessionID: "session", Status: batch.Status, Batch: &batch}
	body, _ := json.Marshal(submission)
	if response := postSecurityRequest(app, token, body); response.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postSecurityRequest(app, token, body); response.Code != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postSecurityRequest(app, "wrong", body); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d", response.Code)
	}
	if response := postSecurityRequest(app, token, append(body, []byte(` {}`)...)); response.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status=%d", response.Code)
	}
	stale := submission
	stale.AgentEpoch = 2
	staleBody, _ := json.Marshal(stale)
	if response := postSecurityRequest(app, token, staleBody); response.Code != http.StatusConflict || jobErrorCode(t, response) != "stale_session" {
		t.Fatalf("stale status=%d body=%s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/web/agents/"+agentID, nil)
	addTestWebSession(t, app, request)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"security":{"supported":true`) {
		t.Fatalf("web status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"cursor", "MESSAGE", "session_id", "token"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("Web DTO exposed %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestSecurityEndpointBodyLimitAndMethod(t *testing.T) {
	app, store, _, token := testApp(t)
	defer store.Close()
	if response := postSecurityRequest(app, token, bytes.Repeat([]byte{'x'}, protocol.MaxSecurityBodyBytes+1)); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d", response.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/security", nil)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("method status=%d", response.Code)
	}
}
