package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func webAgentRemovalResponse(t *testing.T, app *App, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/web/agents/"+agentID+"/remove", strings.NewReader(body))
	session := addTestWebSession(t, app, request)
	request.Header.Set("Origin", "https://probe.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", session.csrfToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestWebAgentRemovalIdempotencyAndAgentReceiptProtocol(t *testing.T) {
	app, store, agentID, agentToken := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	report := reportFor(agentID, 1)
	report.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
	if _, accepted, reason, err := store.ProcessReport(context.Background(), agentID, report, app.now()); err != nil || !accepted {
		t.Fatalf("capability report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
	operationID := "abcdefabcdefabcdefabcdefabcdefab"
	created := webAgentRemovalResponse(t, app, agentID, `{"operation_id":"`+operationID+`"}`)
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var operation webAgentRemovalOperationView
	if err := json.Unmarshal(created.Body.Bytes(), &operation); err != nil || operation.OperationID != operationID || operation.Status != storage.AgentRemovalRequested {
		t.Fatalf("created operation=%+v err=%v body=%s", operation, err, created.Body.String())
	}
	replay := webAgentRemovalResponse(t, app, agentID, `{"operation_id":"`+operationID+`"}`)
	if replay.Code != http.StatusOK {
		t.Fatalf("idempotent replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	conflict := webAgentRemovalResponse(t, app, agentID, `{"operation_id":"11111111111111111111111111111111"}`)
	if conflict.Code != http.StatusConflict || jobErrorCode(t, conflict) != "removal_pending" {
		t.Fatalf("second operation status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	claimRequest := protocol.AgentRemovalClaimRequest{ProtocolVersion: protocol.AgentRemovalProtocolVersion, AgentEpoch: 1, SessionID: "session"}
	claim := postAgentUpgrade(t, app, agentToken, "/api/v1/agent/removals/claim", claimRequest)
	if claim.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claim.Code, claim.Body.String())
	}
	var delivery protocol.AgentRemovalDelivery
	if err := json.Unmarshal(claim.Body.Bytes(), &delivery); err != nil || delivery.OperationID != operationID || delivery.Status != protocol.AgentRemovalStatusDelivered || delivery.Validate() != nil {
		t.Fatalf("delivery=%+v err=%v body=%s", delivery, err, claim.Body.String())
	}
	receiptPath := "/api/v1/agent/removals/" + operationID + "/receipt"
	credentialUsedElsewhere := postReport(t, app, delivery.ReceiptToken, reportFor(agentID, 2))
	if credentialUsedElsewhere.Code != http.StatusUnauthorized {
		t.Fatalf("receipt credential authenticated normal API: status=%d body=%s", credentialUsedElsewhere.Code, credentialUsedElsewhere.Body.String())
	}
	premature := postAgentUpgrade(t, app, delivery.ReceiptToken, receiptPath, protocol.AgentRemovalReceiptRequest{Receipt: protocol.AgentRemovalReceiptKind})
	if premature.Code != http.StatusConflict || jobErrorCode(t, premature) != "invalid_transition" {
		t.Fatalf("premature receipt status=%d body=%s", premature.Code, premature.Body.String())
	}
	progress := postAgentUpgrade(t, app, agentToken, "/api/v1/agent/removals/"+operationID+"/status", protocol.AgentRemovalStatusRequest{
		ProtocolVersion: protocol.AgentRemovalProtocolVersion, AgentEpoch: 1, SessionID: "session", Status: protocol.AgentRemovalStatusUninstalling,
	})
	if progress.Code != http.StatusOK {
		t.Fatalf("progress status=%d body=%s", progress.Code, progress.Body.String())
	}
	completed := postAgentUpgrade(t, app, delivery.ReceiptToken, receiptPath, protocol.AgentRemovalReceiptRequest{Receipt: protocol.AgentRemovalReceiptKind})
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"completed":true`) || strings.Contains(completed.Body.String(), `"duplicate":true`) {
		t.Fatalf("receipt status=%d body=%s", completed.Code, completed.Body.String())
	}
	duplicate := postAgentUpgrade(t, app, delivery.ReceiptToken, receiptPath, protocol.AgentRemovalReceiptRequest{Receipt: protocol.AgentRemovalReceiptKind})
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate receipt status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
}

func TestWebAgentRemovalFailsClosedForLegacyAgent(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	makeUpgradeableAgent(t, app, store, agentID)
	response := webAgentRemovalResponse(t, app, agentID, `{"operation_id":"22222222222222222222222222222222"}`)
	if response.Code != http.StatusUpgradeRequired || jobErrorCode(t, response) != "agent_upgrade_required" {
		t.Fatalf("legacy removal status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCleanupLoopPrunesExpiredRemovalReceiptsWithoutFurtherRequests(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cleanup.db")
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agentID, _ := auth.NewID()
	_, tokenHash, _ := auth.NewToken()
	if err := store.AddAgent(ctx, agentID, "cleanup test", tokenHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	report := reportFor(agentID, 1)
	report.CollectedAt = at.UnixMilli()
	report.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, at); err != nil || !accepted {
		t.Fatalf("report accepted=%t err=%v", accepted, err)
	}
	const operationID = "99999999999999999999999999999999"
	if _, _, err := store.CreateAgentRemovalOperation(ctx, agentID, operationID, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.ClaimAgentRemoval(ctx, agentID, 1, report.SessionID, at.Add(2*time.Second))
	if err != nil || delivery == nil {
		t.Fatalf("claim=%+v err=%v", delivery, err)
	}
	if _, err := store.MarkAgentRemovalUninstalling(ctx, agentID, operationID, 1, report.SessionID, at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operationID, delivery.ReceiptToken, "agent_uninstalled", at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(store, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	app.removalReceiptCleanupInterval = 10 * time.Millisecond
	app.now = func() time.Time { return at.Add(storage.AgentRemovalReceiptReplayTTL + 5*time.Second) }
	done := make(chan struct{})
	go func() {
		app.CleanupLoop()
		close(done)
	}()
	defer func() {
		app.Shutdown()
		<-done
	}()

	queryDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer queryDB.Close()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		if err := queryDB.QueryRow(`SELECT count(*) FROM agent_removal_receipts WHERE operation_id=?`, operationID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("periodic cleanup retained an expired receipt without a follow-up removal request")
		case <-ticker.C:
		}
	}
}
