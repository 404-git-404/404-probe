package server

import (
	"bytes"
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

const egressSession = "session" // the session reportFor puts the Agent in

func egressWebRequest(t *testing.T, app *App, path, contentType, body string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Origin", "https://probe.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if authenticated {
		session := addTestWebSession(t, app, request)
		request.Header.Set("X-CSRF-Token", session.csrfToken)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func egressSwitch(t *testing.T, app *App, agentID string, enabled bool) *httptest.ResponseRecorder {
	body := `{"enabled":false}`
	if enabled {
		body = `{"enabled":true}`
	}
	return egressWebRequest(t, app, "/api/v1/web/agents/"+agentID+"/public-egress", "application/json", body, true)
}

func egressCheck(t *testing.T, app *App, agentID string) *httptest.ResponseRecorder {
	return egressWebRequest(t, app, "/api/v1/web/agents/"+agentID+"/public-egress/check", "application/json", "{}", true)
}

func egressCheckView(t *testing.T, app *App, agentID string) webPublicEgressCheckView {
	t.Helper()
	view, err := app.webAgentDetail(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	return view.PublicEgressCheck
}

// egressClaim is a claim from an Agent that can run public_egress jobs.
func egressClaim(t *testing.T, app *App, token string) (*protocol.Job, *httptest.ResponseRecorder) {
	t.Helper()
	request := validClaimRequest(1, egressSession)
	request.SupportedProbeTypes = []protocol.ProbeType{protocol.ProbeTypePublicEgress}
	return claimHTTPJob(t, app, token, request)
}

func finishEgressHTTPJob(t *testing.T, app *App, token string, job *protocol.Job, outcome protocol.PublicEgressResult) {
	t.Helper()
	result := validHTTPJobResult(job)
	result.SessionID = egressSession
	result.ResolvedIP = ""
	result.Result = protocol.ProbeResult{PublicEgress: &outcome}
	if !outcome.Succeeded() {
		result.Success, result.ErrorCategory, result.ErrorMessage = false, "no_public_address", "no public address could be verified"
	}
	response := jobHTTPResponse(t, app, http.MethodPost, "/api/v1/agent/jobs/"+job.JobID+"/result", token, "application/json", result)
	if response.Code != http.StatusOK {
		t.Fatalf("result status=%d body=%s", response.Code, response.Body.String())
	}
}

func goodEgress() protocol.PublicEgressResult {
	return protocol.PublicEgressResult{
		IPv4: protocol.EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", Reason: "verified"},
		IPv6: protocol.EgressObservation{Family: "ipv6", Status: "unavailable", Reason: "family_unavailable"},
	}
}

func TestWebPublicEgressRequestValidation(t *testing.T) {
	app, store, agentID, _ := testApp(t)
	defer store.Close()
	at := time.Unix(100_000, 0)
	app.now = func() time.Time { return at }
	markSelectorAgentOnline(t, store, agentID, at)
	base := "/api/v1/web/agents/" + agentID + "/public-egress"
	// Not an administrator session: refused.
	for _, path := range []string{base, base + "/check"} {
		if response := egressWebRequest(t, app, path, "application/json", `{}`, false); response.Code < 400 || response.Code == http.StatusInternalServerError {
			t.Errorf("%s without a session status=%d", path, response.Code)
		}
	}
	// Wrong Content-Type, extra or odd bodies, queries and methods.
	for name, response := range map[string]*httptest.ResponseRecorder{
		"switch text/plain":    egressWebRequest(t, app, base, "text/plain", `{"enabled":true}`, true),
		"switch no type":       egressWebRequest(t, app, base, "", `{"enabled":true}`, true),
		"switch extra field":   egressWebRequest(t, app, base, "application/json", `{"enabled":true,"url":"http://x"}`, true),
		"switch wrong type":    egressWebRequest(t, app, base, "application/json", `{"enabled":"yes"}`, true),
		"switch empty":         egressWebRequest(t, app, base, "application/json", `{}`, true),
		"switch two documents": egressWebRequest(t, app, base, "application/json", `{"enabled":true}{"enabled":false}`, true),
		"switch oversize":      egressWebRequest(t, app, base, "application/json", `{"enabled":true,"x":"`+strings.Repeat("a", 200)+`"}`, true),
		"switch query":         egressWebRequest(t, app, base+"?x=1", "application/json", `{"enabled":true}`, true),
		"check text/plain":     egressWebRequest(t, app, base+"/check", "text/plain", `{}`, true),
		"check body":           egressWebRequest(t, app, base+"/check", "application/json", `{"agent":"x"}`, true),
		"check null":           egressWebRequest(t, app, base+"/check", "application/json", `null`, true),
		"check query":          egressWebRequest(t, app, base+"/check?x=1", "application/json", `{}`, true),
		"check oversize":       egressWebRequest(t, app, base+"/check", "application/json", `{"x":"`+strings.Repeat("a", 200)+`"}`, true),
	} {
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{base, base + "/check"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		addTestWebSession(t, app, request)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
			t.Errorf("GET %s status=%d allow=%q", path, response.Code, response.Header().Get("Allow"))
		}
	}
	if view := egressCheckView(t, app, agentID); view.Enabled {
		t.Fatalf("a rejected request changed the switch: %+v", view)
	}
	if response := egressSwitch(t, app, "ffffffffffffffffffffffffffffffff", true); response.Code != http.StatusNotFound {
		t.Fatalf("unknown Agent status=%d", response.Code)
	}
}

func TestWebPublicEgressSwitchManualCheckAndCooldown(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	at := time.Unix(110_000, 0)
	app.now = func() time.Time { return at }
	markSelectorAgentOnline(t, store, agentID, at)
	// Off by default: no job is ever handed out, and the button is refused.
	if view := egressCheckView(t, app, agentID); view.Enabled || view.Pending {
		t.Fatalf("default view=%+v", view)
	}
	if job, response := egressClaim(t, app, token); job != nil || response.Code != http.StatusNoContent {
		t.Fatalf("a job was dispatched while off: %d %s", response.Code, response.Body.String())
	}
	if response := egressCheck(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "public_egress_disabled" {
		t.Fatalf("manual while off status=%d body=%s", response.Code, response.Body.String())
	}
	// Switching on with a capable Agent is one immediate check; that counts as the manual check.
	if job, _ := egressClaim(t, app, token); job != nil {
		t.Fatal("job without the switch")
	}
	response := egressSwitch(t, app, agentID, true)
	if response.Code != http.StatusOK {
		t.Fatalf("switch on status=%d body=%s", response.Code, response.Body.String())
	}
	var switched struct {
		Enabled       bool   `json:"enabled"`
		Pending       bool   `json:"pending"`
		Created       bool   `json:"created"`
		CooldownUntil *int64 `json:"cooldown_until"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &switched); err != nil || !switched.Enabled || !switched.Pending || !switched.Created || switched.CooldownUntil == nil || *switched.CooldownUntil != at.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("switch response %s err=%v", response.Body.String(), err)
	}
	if response := egressCheck(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "public_egress_pending" {
		t.Fatalf("manual while pending status=%d body=%s", response.Code, response.Body.String())
	}
	job, response := egressClaim(t, app, token)
	if job == nil || job.ProbeType != protocol.ProbeTypePublicEgress || job.Config.PublicEgress == nil {
		t.Fatalf("claim=%d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"config":{}`) {
		t.Fatalf("the job carries parameters: %s", response.Body.String())
	}
	finishEgressHTTPJob(t, app, token, job, goodEgress())
	// Inside the cooldown: 429 with the code and the next allowed time; the switch round trip does not dispatch.
	response = egressCheck(t, app, agentID)
	var limited struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		NextAllowedAt int64 `json:"next_allowed_at"`
	}
	if response.Code != http.StatusTooManyRequests || json.Unmarshal(response.Body.Bytes(), &limited) != nil || limited.Error.Code != "cooldown" || limited.NextAllowedAt != at.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("cooldown status=%d body=%s", response.Code, response.Body.String())
	}
	egressSwitch(t, app, agentID, false)
	if response := egressSwitch(t, app, agentID, true); response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"created":true`) {
		t.Fatalf("re-enable in cooldown dispatched: %d %s", response.Code, response.Body.String())
	}
	if job, _ := egressClaim(t, app, token); job != nil {
		t.Fatal("a job was dispatched for the re-enable inside the cooldown")
	}
	view := egressCheckView(t, app, agentID)
	if !view.Enabled || view.Pending || view.CooldownUntil == nil || view.NextCheckAt == nil {
		t.Fatalf("view=%+v", view)
	}
	// 24 hours after the first manual check it is allowed again.
	at = at.Add(24 * time.Hour)
	markSelectorAgentOnlineAgain(t, store, agentID, at, 2)
	if response := egressCheck(t, app, agentID); response.Code != http.StatusAccepted {
		t.Fatalf("after the cooldown status=%d body=%s", response.Code, response.Body.String())
	}
}

func markSelectorAgentOnlineAgain(t *testing.T, store *storage.Store, agentID string, at time.Time, sequence uint64) {
	t.Helper()
	report := reportFor(agentID, sequence)
	report.CollectedAt = at.UnixMilli()
	if _, accepted, reason, err := store.ProcessReport(context.Background(), agentID, report, at); err != nil || !accepted {
		t.Fatalf("report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
}

func TestWebPublicEgressManualCheckErrors(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	at := time.Unix(120_000, 0)
	app.now = func() time.Time { return at }
	markSelectorAgentOnline(t, store, agentID, at)
	// A v1.0.3 Agent never offers the job type: the switch works but the button is unsupported.
	if response := egressSwitch(t, app, agentID, true); response.Code != http.StatusOK {
		t.Fatalf("switch status=%d", response.Code)
	}
	if response := egressCheck(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "public_egress_unsupported" {
		t.Fatalf("legacy Agent status=%d body=%s", response.Code, response.Body.String())
	}
	if view := egressCheckView(t, app, agentID); view.Supported || !view.Enabled {
		t.Fatalf("view=%+v", view)
	}
	// A current Agent negotiates through its claim.
	if job, _ := egressClaim(t, app, token); job == nil {
		t.Fatal("the enabled, capable Agent was not given its first check")
	}
	if response := egressCheck(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "public_egress_pending" {
		t.Fatalf("pending status=%d body=%s", response.Code, response.Body.String())
	}
	// Offline.
	at = at.Add(10 * time.Minute)
	if response := egressCheck(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "agent_offline" {
		t.Fatalf("offline status=%d body=%s", response.Code, response.Body.String())
	}
	// Paused and revoked.
	if changed, err := store.DisableAgent(context.Background(), agentID, at); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if response := egressCheck(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "agent_disabled" {
		t.Fatalf("paused status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := store.RevokeAgent(context.Background(), agentID, at); err != nil {
		t.Fatal(err)
	}
	if response := egressCheck(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "agent_revoked" {
		t.Fatalf("revoked status=%d body=%s", response.Code, response.Body.String())
	}
	if response := egressSwitch(t, app, agentID, false); response.Code != http.StatusConflict || jobErrorCode(t, response) != "agent_revoked" {
		t.Fatalf("switch on a revoked Agent status=%d body=%s", response.Code, response.Body.String())
	}
	if response := egressCheck(t, app, "ffffffffffffffffffffffffffffffff"); response.Code != http.StatusNotFound {
		t.Fatalf("unknown status=%d", response.Code)
	}
}

func TestWebPublicEgressQueueFull(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	at := time.Unix(130_000, 0)
	app.now = func() time.Time { return at }
	markSelectorAgentOnline(t, store, agentID, at)
	egressSwitch(t, app, agentID, true)
	job, _ := egressClaim(t, app, token)
	if job == nil {
		t.Fatal("no first check")
	}
	finishEgressHTTPJob(t, app, token, job, goodEgress())
	at = at.Add(25 * time.Hour)
	markSelectorAgentOnlineAgain(t, store, agentID, at, 2)
	for i := 0; i < storage.MaxOutstandingJobsPerAgent; i++ {
		createHTTPTestJob(t, store, "fill-"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26)), agentID, at, time.Hour)
	}
	if response := egressCheck(t, app, agentID); response.Code != http.StatusConflict || jobErrorCode(t, response) != "queue_full" {
		t.Fatalf("queue full status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPublicEgressCapabilityAndLegacyGate(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	at := time.Unix(140_000, 0)
	app.now = func() time.Time { return at }
	response := postReport(t, app, token, reportFor(agentID, 1))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"public_egress_job":true`) {
		t.Fatalf("report response %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"public_egress_report":true`) {
		t.Fatalf("legacy telemetry capability withdrawn: %s", response.Body.String())
	}
	// Telemetry of a v1.0.3 Agent: dropped while off, accepted once on.
	telemetry := &protocol.PublicEgress{Version: 1,
		IPv4: protocol.EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", CheckedAt: at.UnixMilli(), ValidUntil: at.Add(40 * time.Minute).UnixMilli(), Reason: "verified"},
		IPv6: protocol.UnknownEgress("ipv6", "not_checked")}
	report := reportFor(agentID, 2)
	report.CollectedAt = at.UnixMilli()
	report.PublicEgress = telemetry
	if got := postReport(t, app, token, report); got.Code != http.StatusOK {
		t.Fatal(got.Code, got.Body.String())
	}
	detail, err := app.webAgentDetail(context.Background(), agentID)
	if err != nil || detail.State == nil || detail.State.PublicEgress.IPv4.IP != "" || detail.State.PublicEgress.Supported {
		t.Fatalf("telemetry visible while off: %+v err=%v", detail.State, err)
	}
	if response := egressSwitch(t, app, agentID, true); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	report.Sequence = 3
	if got := postReport(t, app, token, report); got.Code != http.StatusOK {
		t.Fatal(got.Code, got.Body.String())
	}
	if detail, err = app.webAgentDetail(context.Background(), agentID); err != nil || detail.State.PublicEgress.IPv4.IP != "8.8.8.8" {
		t.Fatalf("telemetry rejected while on: %+v err=%v", detail.State, err)
	}
	// A legacy Agent cannot use the button.
	if view := detail.PublicEgressCheck; view.Supported {
		t.Fatalf("legacy Agent shown as supporting the job: %+v", view)
	}
	// Switching off hides it again.
	egressSwitch(t, app, agentID, false)
	if detail, err = app.webAgentDetail(context.Background(), agentID); err != nil || detail.State.PublicEgress.IPv4.IP != "" {
		t.Fatalf("result visible after switching off: %+v err=%v", detail.State, err)
	}
}

func TestPublicEgressSchedulerThroughClaims(t *testing.T) {
	app, store, agentID, token := testApp(t)
	defer store.Close()
	at := time.Unix(150_000, 0)
	app.now = func() time.Time { return at }
	markSelectorAgentOnline(t, store, agentID, at)
	// Enabled while the Agent is idle: the first claim that proves it is capable starts the check.
	if _, err := store.SetPublicEgressEnabled(context.Background(), agentID, true, at, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	job, _ := egressClaim(t, app, token)
	if job == nil {
		t.Fatal("no check after the capability was negotiated")
	}
	finishEgressHTTPJob(t, app, token, job, goodEgress())
	if again, _ := egressClaim(t, app, token); again != nil {
		t.Fatal("a second check right away")
	}
	// The weekly check.
	at = at.Add(7*24*time.Hour + time.Second)
	markSelectorAgentOnlineAgain(t, store, agentID, at, 2)
	if weekly, _ := egressClaim(t, app, token); weekly == nil {
		t.Fatal("no weekly check")
	}
}
