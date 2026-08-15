package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

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
	app, err := NewApp(store, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	return app, store, id, token
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

func TestOnlineOfflineOnline(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	now := time.Unix(1000, 0)
	app.now = func() time.Time { return now }
	if response := postReport(t, app, token, reportFor(id, 1)); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	assertOnline(t, app, true)
	now = now.Add(31 * time.Second)
	assertOnline(t, app, false)
	if response := postReport(t, app, token, reportFor(id, 2)); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	assertOnline(t, app, true)
}

func assertOnline(t *testing.T, app *App, want bool) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	var agents []struct {
		Online bool `json:"online"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Online != want {
		t.Fatalf("agents=%s want online=%t", response.Body.String(), want)
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
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
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
		var view agentView
		if err := json.Unmarshal(payload, &view); err != nil || view.Sequence != newer.Sequence || view.RXTotal != newer.RXTotal {
			t.Fatalf("SSE payload=%s err=%v", payload, err)
		}
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
	response, err := server.Client().Get(server.URL + "/api/v1/events")
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
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
	first, err := server.Client().Get(server.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	if first.StatusCode != http.StatusOK {
		first.Body.Close()
		t.Fatalf("first SSE status=%d", first.StatusCode)
	}
	second, err := server.Client().Get(server.URL + "/api/v1/events")
	if err != nil {
		first.Body.Close()
		t.Fatal(err)
	}
	if second.StatusCode != http.StatusServiceUnavailable {
		second.Body.Close()
		first.Body.Close()
		t.Fatalf("second SSE status=%d", second.StatusCode)
	}
	second.Body.Close()
	first.Body.Close()
	waitForSubscribers(t, app.hub, 0)
	third, err := server.Client().Get(server.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
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
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("404-probe")) {
		t.Fatalf("embedded dashboard status=%d body=%q", response.Code, response.Body.String())
	}
}
