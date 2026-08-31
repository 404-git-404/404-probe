package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"404-probe/internal/agent"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func webSelectorSwitchResponse(t *testing.T, app *App, agentID, body string, configure func(*http.Request, *webSession)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, webAgentPathPrefix+agentID+"/outbounds/switch", strings.NewReader(body))
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

func TestWebSelectorSwitchCreatesAllowlistedIdempotentJob(t *testing.T) {
	app, store, agentID, _ := testApp(t)
	defer store.Close()
	now := time.Unix(3_000, 0)
	app.now = func() time.Time { return now }
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{
		{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}},
		{Name: "backup", Current: "direct", Choices: []string{"direct", "warp"}},
	}}, now); err != nil {
		t.Fatal(err)
	}
	body := `{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","selector":"proxy","choice":"jp"}`
	created := webSelectorSwitchResponse(t, app, agentID, body, nil)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"created":true`) {
		t.Fatalf("created status=%d body=%s", created.Code, created.Body.String())
	}
	replayed := webSelectorSwitchResponse(t, app, agentID, body, nil)
	if replayed.Code != http.StatusOK || !strings.Contains(replayed.Body.String(), `"created":false`) {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	pending := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"abababababababababababababababab","selector":"proxy","choice":"hk"}`, nil)
	if pending.Code != http.StatusConflict || jobErrorCode(t, pending) != "selector_switch_pending" {
		t.Fatalf("pending status=%d body=%s", pending.Code, pending.Body.String())
	}
	differentSelector := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"acacacacacacacacacacacacacacacac","selector":"backup","choice":"warp"}`, nil)
	if differentSelector.Code != http.StatusCreated {
		t.Fatalf("different selector status=%d body=%s", differentSelector.Code, differentSelector.Body.String())
	}
	job, _, err := store.GetProbeJobSnapshot(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", now)
	if err != nil || job.ProbeType != protocol.ProbeTypeSelectorSwitch || job.Config.SelectorSwitch == nil || job.Config.SelectorSwitch.Selector != "proxy" || job.Config.SelectorSwitch.Choice != "jp" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	outside := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","selector":"proxy","choice":"us"}`, nil)
	if outside.Code != http.StatusConflict || jobErrorCode(t, outside) != "selector_choice_not_allowed" {
		t.Fatalf("outside status=%d body=%s", outside.Code, outside.Body.String())
	}
	if _, _, err := store.GetProbeJobSnapshot(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", now); !errors.Is(err, storage.ErrJobNotFound) {
		t.Fatalf("outside choice created job: %v", err)
	}
}

func TestWebSelectorSwitchTrustBoundaryAndAgentStates(t *testing.T) {
	app, store, agentID, _ := testApp(t)
	defer store.Close()
	now := time.Unix(3_100, 0)
	app.now = func() time.Time { return now }
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{
		{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}},
	}}, now); err != nil {
		t.Fatal(err)
	}
	body := `{"request_id":"cccccccccccccccccccccccccccccccc","selector":"proxy","choice":"jp"}`
	tests := []struct {
		name      string
		configure func(*http.Request, *webSession)
	}{
		{"missing csrf", func(r *http.Request, _ *webSession) { r.Header.Del("X-CSRF-Token") }},
		{"wrong origin", func(r *http.Request, _ *webSession) { r.Header.Set("Origin", "https://attacker.test") }},
		{"cross site", func(r *http.Request, _ *webSession) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
	}
	for _, test := range tests {
		response := webSelectorSwitchResponse(t, app, agentID, body, test.configure)
		if response.Code != http.StatusForbidden || jobErrorCode(t, response) != "forbidden" {
			t.Fatalf("%s status=%d body=%s", test.name, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, webAgentPathPrefix+agentID+"/outbounds/switch", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	unauthenticated := httptest.NewRecorder()
	app.Handler().ServeHTTP(unauthenticated, request)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.Code)
	}
	if changed, err := store.DisableAgent(context.Background(), agentID, now); err != nil || !changed {
		t.Fatalf("disable=%t err=%v", changed, err)
	}
	disabled := webSelectorSwitchResponse(t, app, agentID, body, nil)
	if disabled.Code != http.StatusLocked || jobErrorCode(t, disabled) != "agent_disabled" {
		t.Fatalf("disabled status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	if changed, err := store.EnableAgent(context.Background(), agentID, now); err != nil || !changed {
		t.Fatalf("enable=%t err=%v", changed, err)
	}
	if changed, err := store.RevokeAgent(context.Background(), agentID, now); err != nil || !changed {
		t.Fatalf("revoke=%t err=%v", changed, err)
	}
	revoked := webSelectorSwitchResponse(t, app, agentID, body, nil)
	if revoked.Code != http.StatusConflict || jobErrorCode(t, revoked) != "agent_revoked" {
		t.Fatalf("revoked status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestPauseExpiresQueuedSelectorSwitch(t *testing.T) {
	app, store, agentID, agentToken := testApp(t)
	defer store.Close()
	now := time.Now()
	app.now = func() time.Time { return now }
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{
		{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}},
	}}, now); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(webSelectorSwitchRequest{RequestID: "dddddddddddddddddddddddddddddddd", Selector: "proxy", Choice: "jp"})
	if response := webSelectorSwitchResponse(t, app, agentID, string(body), nil); response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if changed, err := store.DisableAgent(context.Background(), agentID, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("disable=%t err=%v", changed, err)
	}
	job, _, err := store.GetProbeJobSnapshot(context.Background(), "dddddddddddddddddddddddddddddddd", now.Add(time.Second))
	if err != nil || job.Status != storage.JobStatusExpired {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if changed, err := store.EnableAgent(context.Background(), agentID, now.Add(2*time.Second)); err != nil || !changed {
		t.Fatalf("enable=%t err=%v", changed, err)
	}
	app.now = func() time.Time { return now.Add(2 * time.Second) }
	resumed := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"dededededededededededededededede","selector":"proxy","choice":"jp"}`, nil)
	if resumed.Code != http.StatusCreated {
		t.Fatalf("resumed status=%d body=%s", resumed.Code, resumed.Body.String())
	}
	current, puts := "hk", 0
	var clashMu sync.Mutex
	clash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clashMu.Lock()
		defer clashMu.Unlock()
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"proxies": map[string]any{
				"proxy": map[string]any{"type": "Selector", "name": "proxy", "now": current, "all": []string{"hk", "jp"}},
			}})
		case http.MethodPut:
			puts++
			current = "jp"
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer clash.Close()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	runner, err := agent.New(agent.Config{
		ServerURL: httpServer.URL, AgentID: agentID, Token: agentToken,
		Interval: time.Hour, JobInterval: 10 * time.Millisecond, Timeout: time.Second,
		AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "pause-resume.state"),
		ClashAPIURL: clash.URL, OutboundInterval: time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runner.Run(ctx) }()
	result := waitForSelectorSwitchResult(t, ctx, store, "dededededededededededededededede")
	clashMu.Lock()
	gotPuts := puts
	clashMu.Unlock()
	if !result.Result.Success || result.Result.Result.SelectorSwitch == nil || result.Result.Result.SelectorSwitch.Current != "jp" || gotPuts != 1 {
		t.Fatalf("resumed result=%+v puts=%d", result.Result, gotPuts)
	}
	cancel()
	if err := <-runnerDone; err != nil {
		t.Fatal(err)
	}
}

func TestRevokeExpiresQueuedSelectorSwitch(t *testing.T) {
	app, store, agentID, _ := testApp(t)
	defer store.Close()
	now := time.Unix(3_300, 0)
	app.now = func() time.Time { return now }
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{
		{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}},
	}}, now); err != nil {
		t.Fatal(err)
	}
	jobID := "dadadadadadadadadadadadadadadada"
	if response := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"`+jobID+`","selector":"proxy","choice":"jp"}`, nil); response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if changed, err := store.RevokeAgent(context.Background(), agentID, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("revoke=%t err=%v", changed, err)
	}
	job, _, err := store.GetProbeJobSnapshot(context.Background(), jobID, now.Add(time.Second))
	if err != nil || job.Status != storage.JobStatusExpired {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

func TestRevokePreventsRunningSelectorSwitchFromExecutingAgain(t *testing.T) {
	app, store, agentID, agentToken := testApp(t)
	defer store.Close()
	now := time.Unix(3_400, 0)
	app.now = func() time.Time { return now }
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{
		{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}},
	}}, now); err != nil {
		t.Fatal(err)
	}
	jobID := "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	if response := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"`+jobID+`","selector":"proxy","choice":"jp"}`, nil); response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	claim := protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: 1, SessionID: "running",
		SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeSelectorSwitch},
	}
	job, response := claimHTTPJob(t, app, agentToken, claim)
	if response.Code != http.StatusOK || job == nil || job.JobID != jobID {
		t.Fatalf("claim status=%d body=%s job=%+v", response.Code, response.Body.String(), job)
	}
	if changed, err := store.RevokeAgent(context.Background(), agentID, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("revoke=%t err=%v", changed, err)
	}
	running, err := store.GetProbeJob(context.Background(), jobID)
	if err != nil || running.Status != storage.JobStatusLeased {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	claimAfterRevoke := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/claim", agentToken, "application/json", claim)
	if claimAfterRevoke.Code != http.StatusUnauthorized || jobErrorCode(t, claimAfterRevoke) != "agent_revoked" {
		t.Fatalf("revoke claim status=%d body=%s", claimAfterRevoke.Code, claimAfterRevoke.Body.String())
	}
	expired, _, err := store.GetProbeJobSnapshot(context.Background(), jobID, now.Add(selectorSwitchTTL+time.Second))
	if err != nil || expired.Status != storage.JobStatusExpired {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
}

func TestWebSelectorSwitchEndToEndAndStaleLocalChoice(t *testing.T) {
	app, store, agentID, agentToken := testApp(t)
	defer store.Close()

	var clashMu sync.Mutex
	current := "hk"
	choices := []string{"hk", "jp"}
	applySwitch := true
	puts := 0
	clash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		clashMu.Lock()
		defer clashMu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			writeJSON(w, http.StatusOK, map[string]any{"proxies": map[string]any{
				"proxy": map[string]any{"type": "Selector", "name": "proxy", "now": current, "all": choices},
			}})
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/proxy":
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}
			puts++
			if applySwitch {
				current = body.Name
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer clash.Close()

	now := time.Now()
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{
		{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}},
	}}, now); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	runner, err := agent.New(agent.Config{
		ServerURL: httpServer.URL, AgentID: agentID, Token: agentToken,
		Interval: time.Hour, JobInterval: 10 * time.Millisecond, Timeout: 2 * time.Second,
		AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "agent.state"),
		ClashAPIURL: clash.URL, ClashAPISecret: "local-secret", OutboundInterval: time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runner.Run(ctx) }()

	firstID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	first := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"`+firstID+`","selector":"proxy","choice":"jp"}`, nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("create success path status=%d body=%s", first.Code, first.Body.String())
	}
	firstResult := waitForSelectorSwitchResult(t, ctx, store, firstID)
	if !firstResult.Result.Success || firstResult.Result.Result.SelectorSwitch == nil || firstResult.Result.Result.SelectorSwitch.Current != "jp" {
		t.Fatalf("success result=%+v", firstResult.Result)
	}
	snapshot, configured, err := store.GetOutboundSnapshot(context.Background(), agentID)
	if err != nil || !configured || len(snapshot.Selectors) != 1 || snapshot.Selectors[0].Current != "jp" {
		t.Fatalf("post-switch snapshot=%+v configured=%t err=%v", snapshot, configured, err)
	}
	noOpID := "11111111111111111111111111111111"
	noOp := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"`+noOpID+`","selector":"proxy","choice":"jp"}`, nil)
	if noOp.Code != http.StatusCreated {
		t.Fatalf("create no-op path status=%d body=%s", noOp.Code, noOp.Body.String())
	}
	noOpResult := waitForSelectorSwitchResult(t, ctx, store, noOpID)
	if !noOpResult.Result.Success || noOpResult.Result.Result.SelectorSwitch == nil || noOpResult.Result.Result.SelectorSwitch.Changed {
		t.Fatalf("no-op result=%+v", noOpResult.Result)
	}
	clashMu.Lock()
	if puts != 1 {
		t.Fatalf("PUT count=%d, no-op must not mutate", puts)
	}
	current = "hk"
	choices = []string{"hk", "jp"}
	applySwitch = false
	clashMu.Unlock()
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{
		{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}},
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	mismatchID := "22222222222222222222222222222222"
	mismatch := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"`+mismatchID+`","selector":"proxy","choice":"jp"}`, nil)
	if mismatch.Code != http.StatusCreated {
		t.Fatalf("create mismatch path status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	mismatchResult := waitForSelectorSwitchResult(t, ctx, store, mismatchID)
	if mismatchResult.Result.Success || mismatchResult.Result.ErrorCategory != "switch_verification_failed" {
		t.Fatalf("mismatch result=%+v", mismatchResult.Result)
	}
	snapshot, _, err = store.GetOutboundSnapshot(context.Background(), agentID)
	if err != nil || snapshot.Selectors[0].Current != "hk" {
		t.Fatalf("verification failure changed snapshot=%+v err=%v", snapshot, err)
	}

	clashMu.Lock()
	choices = []string{"hk"}
	applySwitch = true
	clashMu.Unlock()
	if err := store.SaveOutboundSnapshot(context.Background(), agentID, protocol.OutboundSnapshot{Available: true, Selectors: []protocol.OutboundSelector{
		{Name: "proxy", Current: "hk", Choices: []string{"hk", "jp"}},
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	secondID := "ffffffffffffffffffffffffffffffff"
	second := webSelectorSwitchResponse(t, app, agentID, `{"request_id":"`+secondID+`","selector":"proxy","choice":"jp"}`, nil)
	if second.Code != http.StatusCreated {
		t.Fatalf("create stale path status=%d body=%s", second.Code, second.Body.String())
	}
	secondResult := waitForSelectorSwitchResult(t, ctx, store, secondID)
	if secondResult.Result.Success || secondResult.Result.ErrorCategory != "choice_not_found" {
		t.Fatalf("stale result=%+v", secondResult.Result)
	}
	clashMu.Lock()
	gotPuts := puts
	clashMu.Unlock()
	if gotPuts != 2 {
		t.Fatalf("PUT count=%d, stale choice must not mutate", gotPuts)
	}
	request := httptest.NewRequest(http.MethodGet, webJobPathPrefix+secondID, nil)
	addTestWebSession(t, app, request)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"error_category":"choice_not_found"`) {
		t.Fatalf("Web stale result status=%d body=%s", response.Code, response.Body.String())
	}

	cancel()
	if err := <-runnerDone; err != nil {
		t.Fatal(err)
	}
}

func waitForSelectorSwitchResult(t *testing.T, ctx context.Context, store *storage.Store, jobID string) *storage.ProbeResultRecord {
	t.Helper()
	for ctx.Err() == nil {
		job, result, err := store.GetProbeJobSnapshot(context.Background(), jobID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == storage.JobStatusFinished && result != nil {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(ctx.Err())
	return nil
}
