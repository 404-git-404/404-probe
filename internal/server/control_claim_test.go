package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestInteractiveControlClaimWakesForSelectorOnly(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	now := time.Now()
	app.now = time.Now
	markSelectorAgentOnline(t, store, agentID, now)
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{
		Available: true, Status: protocol.OutboundStatusConnected,
		Selectors: []protocol.OutboundSelector{{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}}},
	}, now); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(app.Handler())
	defer server.Close()
	claimBody, _ := json.Marshal(protocol.ControlClaimRequest{
		ProtocolVersion: protocol.ControlProtocolVersion, AgentEpoch: 9, SessionID: "interactive-test",
	})
	type claimResult struct {
		job     protocol.Job
		status  int
		err     error
		elapsed time.Duration
	}
	claimed := make(chan claimResult, 1)
	go func() {
		started := time.Now()
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/agent/control/claim", bytes.NewReader(claimBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(req)
		result := claimResult{err: err, elapsed: time.Since(started)}
		if err == nil {
			defer response.Body.Close()
			result.status = response.StatusCode
			result.err = json.NewDecoder(response.Body).Decode(&result.job)
		}
		claimed <- result
	}()

	deadline := time.Now().Add(time.Second)
	for {
		app.controlMu.Lock()
		waiting := len(app.controlWaiters[agentID])
		app.controlMu.Unlock()
		if waiting != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control claim did not enter long poll")
		}
		time.Sleep(time.Millisecond)
	}
	createdAt := time.Now()
	response := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"99999999999999999999999999999999","selector":"proxy","choice":"jp"}`, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case result := <-claimed:
		if result.err != nil || result.status != http.StatusOK || result.job.ProbeType != protocol.ProbeTypeSelectorSwitch {
			t.Fatalf("claim status=%d job=%+v err=%v", result.status, result.job, result.err)
		}
		if wakeLatency := time.Since(createdAt); wakeLatency > time.Second {
			t.Fatalf("control wake latency=%s", wakeLatency)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("selector control did not wake the long poll")
	}
}

func TestInteractiveControlClaimRejectsGenericSurface(t *testing.T) {
	app, store, _, token := testApp(t)
	defer store.Close()
	body := `{"protocol_version":1,"agent_epoch":1,"session_id":"s","supported_probe_types":["http"]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/control/claim", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_request" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInteractiveControlClaimTimesOutCleanlyAndReconnects(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	app.controlPollDuration = 15 * time.Millisecond
	body := protocol.ControlClaimRequest{ProtocolVersion: protocol.ControlProtocolVersion, AgentEpoch: 2, SessionID: "reconnect"}
	first := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/control/claim", token, "application/json", body)
	if first.Code != http.StatusNoContent || first.Body.Len() != 0 {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	now := time.Now()
	if err := store.CreateOneShotJob(context.Background(), storage.CreateOneShotJobParams{
		ID: "88888888888888888888888888888888", AgentID: agentID, ProbeType: protocol.ProbeTypeSelectorSwitch,
		Config:    protocol.ProbeConfig{SelectorSwitch: &protocol.SelectorSwitchConfig{Selector: "proxy", Choice: "jp"}},
		TimeoutMS: 1000, CreatedAt: now.UnixMilli(), NotBefore: now.UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	second := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/control/claim", token, "application/json", body)
	if second.Code != http.StatusOK || !bytes.Contains(second.Body.Bytes(), []byte(`"probe_type":"singbox_selector_switch"`)) {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
}
