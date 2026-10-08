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

func webAgentCountryLookupResponse(t *testing.T, app *App, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/web/agents/"+agentID+"/country-code/refresh", strings.NewReader(body))
	session := addTestWebSession(t, app, request)
	request.Header.Set("Origin", "https://probe.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", session.csrfToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestWebAgentCountryCodeLookupIsSingleTriggerAndUpdatesWithoutLosingPriorValue(t *testing.T) {
	app, store, agentID, agentToken := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	at := app.now()
	report := reportFor(agentID, 1)
	report.Management = &protocol.AgentManagementCapabilities{CountryCodeLookup: true}
	if _, accepted, _, err := store.ProcessReport(context.Background(), agentID, report, at); err != nil || !accepted {
		t.Fatalf("capability report accepted=%t err=%v", accepted, err)
	}
	created := webAgentCountryLookupResponse(t, app, agentID, `{}`)
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var view webAgentCountryCodeLookupView
	if err := json.Unmarshal(created.Body.Bytes(), &view); err != nil || view.Status != string(storage.AgentCountryCodeLookupRequested) || view.OperationID == "" {
		t.Fatalf("created view=%+v err=%v", view, err)
	}
	duplicateTrigger := webAgentCountryLookupResponse(t, app, agentID, `{}`)
	if duplicateTrigger.Code != http.StatusConflict || jobErrorCode(t, duplicateTrigger) != "lookup_pending" {
		t.Fatalf("duplicate trigger status=%d body=%s", duplicateTrigger.Code, duplicateTrigger.Body.String())
	}

	claimRequest := protocol.AgentCountryCodeLookupClaimRequest{ProtocolVersion: protocol.CountryCodeLookupProtocolVersion, AgentEpoch: 1, SessionID: report.SessionID}
	claim := postAgentUpgrade(t, app, agentToken, "/api/v1/agent/country-code-lookups/claim", claimRequest)
	if claim.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claim.Code, claim.Body.String())
	}
	var delivery protocol.AgentCountryCodeLookupDelivery
	if err := json.Unmarshal(claim.Body.Bytes(), &delivery); err != nil || delivery.Validate() != nil || delivery.OperationID != view.OperationID {
		t.Fatalf("delivery=%+v err=%v body=%s", delivery, err, claim.Body.String())
	}
	claimAgain := postAgentUpgrade(t, app, agentToken, "/api/v1/agent/country-code-lookups/claim", claimRequest)
	if claimAgain.Code != http.StatusNoContent {
		t.Fatalf("one-shot duplicate claim status=%d body=%s", claimAgain.Code, claimAgain.Body.String())
	}

	resultPath := "/api/v1/agent/country-code-lookups/" + delivery.OperationID + "/result"
	resultRequest := protocol.AgentCountryCodeLookupResultRequest{ProtocolVersion: protocol.CountryCodeLookupProtocolVersion, AgentEpoch: 1, SessionID: report.SessionID, CountryCode: "US"}
	result := postAgentUpgrade(t, app, agentToken, resultPath, resultRequest)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("result status=%d body=%s", result.Code, result.Body.String())
	}
	replayed := postAgentUpgrade(t, app, agentToken, resultPath, resultRequest)
	if replayed.Code != http.StatusOK || !strings.Contains(replayed.Body.String(), `"duplicate":true`) {
		t.Fatalf("result replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	current, exists, err := store.GetAgentCountryCodeLookup(context.Background(), agentID)
	if err != nil || !exists || current.LastCode != "US" {
		t.Fatalf("stored country lookup=%+v exists=%t err=%v", current, exists, err)
	}

	failedTrigger := webAgentCountryLookupResponse(t, app, agentID, `{}`)
	if failedTrigger.Code != http.StatusAccepted {
		t.Fatalf("second create status=%d body=%s", failedTrigger.Code, failedTrigger.Body.String())
	}
	if err := json.Unmarshal(failedTrigger.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	claim = postAgentUpgrade(t, app, agentToken, "/api/v1/agent/country-code-lookups/claim", claimRequest)
	if claim.Code != http.StatusOK || json.Unmarshal(claim.Body.Bytes(), &delivery) != nil {
		t.Fatalf("second claim status=%d body=%s", claim.Code, claim.Body.String())
	}
	resultPath = "/api/v1/agent/country-code-lookups/" + delivery.OperationID + "/result"
	failedResult := protocol.AgentCountryCodeLookupResultRequest{ProtocolVersion: protocol.CountryCodeLookupProtocolVersion, AgentEpoch: 1, SessionID: report.SessionID, ErrorCode: protocol.CountryCodeLookupFailure}
	failed := postAgentUpgrade(t, app, agentToken, resultPath, failedResult)
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), `"status":"failed"`) {
		t.Fatalf("failed result status=%d body=%s", failed.Code, failed.Body.String())
	}
	current, exists, err = store.GetAgentCountryCodeLookup(context.Background(), agentID)
	if err != nil || !exists || current.Status != storage.AgentCountryCodeLookupFailed || current.LastCode != "US" {
		t.Fatalf("failed recheck did not retain prior code: %+v exists=%t err=%v", current, exists, err)
	}
}

func TestWebAgentCountryCodeLookupRequiresCurrentCapability(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	defer app.Shutdown()
	makeUpgradeableAgent(t, app, store, agentID)
	response := webAgentCountryLookupResponse(t, app, agentID, `{}`)
	if response.Code != http.StatusUpgradeRequired || jobErrorCode(t, response) != "agent_upgrade_required" {
		t.Fatalf("legacy Agent country lookup status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAgentCountryCodeLookupRejectsMalformedRequest(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	defer app.Shutdown()
	makeUpgradeableAgent(t, app, store, agentID)
	response := webAgentCountryLookupResponse(t, app, agentID, `{"unexpected":true}`)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_request" {
		t.Fatalf("malformed country lookup status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAgentCountryLookupOperationViewExpiresToUnknown(t *testing.T) {
	at := time.Now()
	operation := storage.AgentCountryCodeLookup{OperationID: "abcdefabcdefabcdefabcdefabcdefab", Status: storage.AgentCountryCodeLookupDelivered, CreatedAt: at.Add(-11 * time.Minute).UnixMilli(), UpdatedAt: at.Add(-11 * time.Minute).UnixMilli()}
	view := newWebAgentCountryCodeLookupView(operation, at)
	if view.Status != "unknown" {
		t.Fatalf("stale delivery status=%q", view.Status)
	}
}
