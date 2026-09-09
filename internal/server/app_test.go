package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

var (
	testWebHashOnce sync.Once
	testWebHash     string
	testWebHashErr  error
)

func webTestPasswordHash(t *testing.T) string {
	t.Helper()
	testWebHashOnce.Do(func() {
		testWebHash, testWebHashErr = auth.HashPassword([]byte("test-password"))
	})
	if testWebHashErr != nil {
		t.Fatal(testWebHashErr)
	}
	return testWebHash
}

func testApp(t *testing.T) (*App, *storage.Store, string, string) {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := auth.NewID()
	token, hash, _ := auth.NewToken()
	if err := store.AddAgent(context.Background(), id, "test", hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(store, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{
		PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return app, store, id, token
}

func addTestWebSession(t *testing.T, app *App, request *http.Request) *webSession {
	t.Helper()
	request.Host = app.webAuth.publicOrigin.Host
	plain, session, err := app.webAuth.createSession(app.now())
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: app.webAuth.sessionCookieName(), Value: plain})
	return session
}

func getTestWeb(t *testing.T, app *App, client *http.Client, target string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	addTestWebSession(t, app, request)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func reportFor(id string, seq uint64) protocol.Report {
	return protocol.Report{AgentID: id, Epoch: 1, SessionID: "session", Sequence: seq, CollectedAt: time.Now().UnixMilli(), Hostname: "host", OS: "linux", Arch: "amd64", BootID: "boot", Uptime: 1, CPUPercent: 1, Load1: 1, Load5: 1, Load15: 1, RAMUsed: 1, RAMTotal: 2, RAMPercent: 50, SwapUsed: 0, SwapTotal: 0, SwapPercent: 0, DiskUsed: 1, DiskTotal: 2, DiskPercent: 50, RXBytes: 100 + seq, TXBytes: 200 + seq}
}

func postReport(t *testing.T, app *App, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if raw, ok := body.([]byte); ok {
		reader = bytes.NewReader(raw)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/report", reader)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestReportAuthentication(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	if response := postReport(t, app, token, reportFor(id, 1)); response.Code != http.StatusOK {
		t.Fatalf("correct token status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postReport(t, app, "wrong", reportFor(id, 2)); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d", response.Code)
	}
}

func TestReportRevocationHasStableMachineSignal(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	if revoked, err := store.RevokeAgent(context.Background(), id, time.Now()); err != nil || !revoked {
		t.Fatalf("revoke=%t err=%v", revoked, err)
	}
	response := postReport(t, app, token, reportFor(id, 1))
	if response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "agent_revoked" ||
		!strings.Contains(response.Body.String(), "agent credential has been revoked") {
		t.Fatalf("revoked status=%d body=%s", response.Code, response.Body.String())
	}
	unknown := postReport(t, app, "wrong", reportFor(id, 2))
	if unknown.Code != http.StatusUnauthorized || strings.Contains(unknown.Body.String(), "agent_revoked") {
		t.Fatalf("unknown status=%d body=%s", unknown.Code, unknown.Body.String())
	}
}

func TestReportDisabledHasStableDistinctMachineSignal(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	if disabled, err := store.DisableAgent(context.Background(), id, time.Now()); err != nil || !disabled {
		t.Fatalf("disable=%t err=%v", disabled, err)
	}
	response := postReport(t, app, token, reportFor(id, 1))
	if response.Code != http.StatusLocked || jobErrorCode(t, response) != "agent_disabled" ||
		strings.Contains(response.Body.String(), "agent_revoked") {
		t.Fatalf("disabled status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOnlineOfflineOnline(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	if response := postReport(t, app, token, reportFor(id, 1)); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	assertOnline(t, app, id, true)
	now = now.Add(31 * time.Second)
	assertOnline(t, app, id, false)
	if response := postReport(t, app, token, reportFor(id, 2)); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	assertOnline(t, app, id, true)
}

func assertOnline(t *testing.T, app *App, agentID string, want bool) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, webAgentPathPrefix+agentID, nil)
	addTestWebSession(t, app, request)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	var agent struct {
		Online bool `json:"online"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Online != want {
		t.Fatalf("agent=%s want online=%t", response.Body.String(), want)
	}
}

func TestMalformedReportDoesNotPanic(t *testing.T) {
	app, store, _, token := testApp(t)
	defer store.Close()
	for _, body := range [][]byte{[]byte(`{"broken":`), []byte(`{}`), append([]byte(`{"agent_id":"x"}`), []byte(` {}`)...)} {
		response := postReport(t, app, token, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%q status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/web/agents", nil)
	addTestWebSession(t, app, request)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("server unusable after malformed reports: %d", response.Code)
	}
}

func TestReportBodyLimitsAndUnknownFields(t *testing.T) {
	app, store, _, token := testApp(t)
	defer store.Close()
	if response := postReport(t, app, token, bytes.Repeat([]byte(" "), protocol.MaxReportBytes+1)); response.Code != http.StatusBadRequest {
		t.Fatalf("oversized report status=%d body=%s", response.Code, response.Body.String())
	}
	if response := postReport(t, app, token, []byte(`{"unknown":true}`)); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConcurrentPublicationCannotRegressMemoryOrSSE(t *testing.T) {
	app, store, id, _ := testApp(t)
	defer store.Close()
	ch, ok := app.hub.subscribe()
	if !ok {
		t.Fatal("subscribe failed")
	}
	defer app.hub.unsubscribe(ch)
	older := storage.State{AgentID: id, Epoch: 10, SessionID: "session-old", Sequence: 9, RXTotal: 90}
	newer := storage.State{AgentID: id, Epoch: 11, SessionID: "session-new", Sequence: 1, RXTotal: 100}
	newPublished := make(chan struct{})
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-newPublished
		results <- app.publishState(older)
	}()
	go func() {
		defer wg.Done()
		results <- app.publishState(newer)
		close(newPublished)
	}()
	wg.Wait()
	close(results)
	var applied int
	for result := range results {
		if result {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("applied publications=%d want 1", applied)
	}
	app.mu.RLock()
	current := app.states[id]
	app.mu.RUnlock()
	if current.Epoch != newer.Epoch || current.Sequence != newer.Sequence || current.RXTotal != newer.RXTotal {
		t.Fatalf("memory regressed to %+v", current)
	}
	select {
	case payload := <-ch:
		var view webAgentEventView
		if err := json.Unmarshal(payload, &view); err != nil || view.State == nil || view.State.RXTotal != newer.RXTotal {
			t.Fatalf("SSE payload=%s err=%v", payload, err)
		}
		assertNoWebAgentSecrets(t, string(payload))
	case <-time.After(time.Second):
		t.Fatal("missing SSE publication")
	}
	select {
	case payload := <-ch:
		t.Fatalf("stale SSE publication=%s", payload)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestActiveSSEExitsBeforeHTTPShutdown(t *testing.T) {
	app, store, _, _ := testApp(t)
	defer store.Close()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		app.CleanupLoop()
	}()
	server := httptest.NewServer(app.Handler())
	response := getTestWeb(t, app, server.Client(), server.URL+"/api/v1/web/events")
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		server.Close()
		t.Fatalf("SSE status=%d", response.StatusCode)
	}
	app.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Config.Shutdown(ctx); err != nil {
		response.Body.Close()
		server.Close()
		t.Fatalf("HTTP shutdown waited for SSE: %v", err)
	}
	response.Body.Close()
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		server.Close()
		t.Fatal("cleanup loop did not exit")
	}
	server.Close()
}

func TestSSESubscriberLimitAndRelease(t *testing.T) {
	app, store, _, _ := testApp(t)
	defer store.Close()
	app.hub = newHub(1)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	first := getTestWeb(t, app, server.Client(), server.URL+"/api/v1/web/events")
	if first.StatusCode != http.StatusOK {
		first.Body.Close()
		t.Fatalf("first SSE status=%d", first.StatusCode)
	}
	second := getTestWeb(t, app, server.Client(), server.URL+"/api/v1/web/events")
	if second.StatusCode != http.StatusServiceUnavailable {
		second.Body.Close()
		first.Body.Close()
		t.Fatalf("second SSE status=%d", second.StatusCode)
	}
	second.Body.Close()
	first.Body.Close()
	waitForSubscribers(t, app.hub, 0)
	third := getTestWeb(t, app, server.Client(), server.URL+"/api/v1/web/events")
	if third.StatusCode != http.StatusOK {
		third.Body.Close()
		t.Fatalf("SSE after release status=%d", third.StatusCode)
	}
	app.Shutdown()
	third.Body.Close()
}

func waitForSubscribers(t *testing.T, h *hub, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		h.mu.Lock()
		got := len(h.clients)
		h.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscribers=%d want %d", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDashboardIsEmbedded(t *testing.T) {
	app, store, _, _ := testApp(t)
	defer store.Close()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	addTestWebSession(t, app, request)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("404-probe")) {
		t.Fatalf("embedded dashboard status=%d body=%q", response.Code, response.Body.String())
	}
}
