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

func TestSecurityUnavailableForTheCollectorSandboxIsStoredAndShownWithItsReason(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	now := time.Now().Truncate(time.Second)
	app.now = func() time.Time { return now }
	report := reportFor(agentID, 1)
	report.CollectedAt = now.UnixMilli()
	if response := postReport(t, app, token, report); response.Code != http.StatusOK {
		t.Fatalf("report status=%d", response.Code)
	}
	for _, reason := range []string{"sandbox_unavailable", "collector_start_failed"} {
		submission := protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: 1, SessionID: "session", Status: protocol.SecurityStatusUnavailable, Reason: reason}
		body, _ := json.Marshal(submission)
		if response := postSecurityRequest(app, token, body); response.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", reason, response.Code, response.Body.String())
		}
		response := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/agents/"+agentID)
		var view struct {
			Security struct {
				Supported bool   `json:"supported"`
				Status    string `json:"status"`
				Reason    string `json:"reason"`
			} `json:"security"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if !view.Security.Supported || view.Security.Status != "unavailable" || view.Security.Reason != reason {
			t.Fatalf("%s: security view=%+v body=%s", reason, view.Security, response.Body.String())
		}
	}
	bad := protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: 1, SessionID: "session", Status: protocol.SecurityStatusUnavailable, Reason: "not_a_reason"}
	body, _ := json.Marshal(bad)
	if response := postSecurityRequest(app, token, body); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown reason status=%d", response.Code)
	}
}

func TestSecurityNoJournalEntriesIsPresentedAsANeutralObservation(t *testing.T) {
	for _, legacyBatch := range []bool{false, true} {
		app, store, agentID, token := testApp(t)
		now := time.Now().Truncate(time.Second)
		app.now = func() time.Time { return now }
		report := reportFor(agentID, 1)
		report.CollectedAt = now.UnixMilli()
		if response := postReport(t, app, token, report); response.Code != http.StatusOK {
			t.Fatalf("report status=%d", response.Code)
		}
		submission := protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: 1, SessionID: "session", Status: protocol.SecurityStatusFailed, Reason: "no_journal_entries"}
		if legacyBatch {
			start, end := now.Add(-time.Hour).UnixMilli(), now.Add(-time.Minute).UnixMilli()
			batch := protocol.SecurityBatch{BatchID: "cccccccccccccccccccccccccccccccc", WindowStart: start, WindowEnd: end, CollectedAt: now.UnixMilli(),
				Status: protocol.SecurityStatusFailed, Reason: "no_journal_entries", Sources: []protocol.SecuritySource{}}
			submission.Batch = &batch
		}
		body, _ := json.Marshal(submission)
		if response := postSecurityRequest(app, token, body); response.Code != http.StatusOK {
			t.Fatalf("batch=%v submit status=%d body=%s", legacyBatch, response.Code, response.Body.String())
		}
		var view struct {
			Security struct {
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Current *struct {
					Status string `json:"status"`
					Reason string `json:"reason"`
				} `json:"current"`
			} `json:"security"`
		}
		response := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/agents/"+agentID)
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Security.Status != "no_data" || view.Security.Reason != "no_sing_box_logs" || strings.Contains(response.Body.String(), "no_journal_entries") && !legacyBatch {
			t.Fatalf("batch=%v security=%+v body=%s", legacyBatch, view.Security, response.Body.String())
		}
		if legacyBatch && (view.Security.Current == nil || view.Security.Current.Status != "no_data" || view.Security.Current.Reason != "no_sing_box_logs") {
			t.Fatalf("current batch not normalised: %s", response.Body.String())
		}
		if strings.Contains(response.Body.String(), "no_journal_entries") {
			t.Fatalf("the failure wording leaked into the Web view: %s", response.Body.String())
		}
		history := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/agents/"+agentID+"/history?hours=24")
		if strings.Contains(history.Body.String(), "no_journal_entries") || legacyBatch && !strings.Contains(history.Body.String(), "no_sing_box_logs") {
			t.Fatalf("history not normalised: %s", history.Body.String())
		}
		store.Close()
	}
}

func TestOtherSecurityFailuresKeepTheirWebStatus(t *testing.T) {
	for _, reason := range []string{"journal_read_failed", "journal_timeout", "invalid_export"} {
		status, got := neutralSecurityOutcome(protocol.SecurityStatusFailed, reason)
		if status != protocol.SecurityStatusFailed || got != reason {
			t.Fatalf("%s became %s/%s", reason, status, got)
		}
	}
	if status, reason := neutralSecurityOutcome(protocol.SecurityStatusComplete, "no_journal_entries"); status != protocol.SecurityStatusComplete || reason != "no_journal_entries" {
		t.Fatalf("only failed reports are reinterpreted: %s/%s", status, reason)
	}
	if status, reason := neutralSecurityOutcome(protocol.SecurityStatusNoData, "awaiting_first_collection"); status != protocol.SecurityStatusNoData || reason != "awaiting_first_collection" {
		t.Fatalf("%s/%s", status, reason)
	}
}
