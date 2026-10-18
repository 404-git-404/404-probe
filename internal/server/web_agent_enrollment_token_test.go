package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
)

func postEnrollmentAPI(t *testing.T, app *App, path, body string, configure func(*http.Request, *webSession)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
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

func agentEnrollCall(app *App, token, name string) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(map[string]string{"token": token, "name": name})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/enroll", strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

var oneClickCommandPattern = regexp.MustCompile(`PROBE_404_ENROLL_TOKEN=([A-Za-z0-9_-]{43}) `)

func createOneClick(t *testing.T, app *App, name string) (view webAgentEnrollmentTokenView, token string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name})
	response := postEnrollmentAPI(t, app, "/api/v1/web/agent-enrollments", string(body), nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	match := oneClickCommandPattern.FindStringSubmatch(view.Command)
	if match == nil {
		t.Fatalf("command has no token: %s", view.Command)
	}
	return view, match[1]
}

func oneClickApp(t *testing.T) (*App, func(name string) int) {
	app, store, _ := newWebAuthenticationTestApp(t)
	t.Cleanup(func() { store.Close() })
	app.buildInfo = buildinfo.Info{Version: "v1.0.4", Commit: strings.Repeat("a", 40)}
	agentsNamed := func(name string) int {
		agents, err := store.ListAgents(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, agent := range agents {
			if agent.Name == name {
				n++
			}
		}
		return n
	}
	return app, agentsNamed
}

func TestOneClickEnrollmentIsSingleUseAndBoundToTheName(t *testing.T) {
	app, agentsNamed := oneClickApp(t)
	view, token := createOneClick(t, app, "东京 web 1")
	want := "curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.4/install.sh | sudo env PROBE_404_ROLE=agent PROBE_404_SERVER=https://probe.test PROBE_404_ENROLL_TOKEN=" + token + " PROBE_404_AGENT_NAME='东京 web 1' bash"
	if view.Command != want {
		t.Fatalf("command=%s\nwant   =%s", view.Command, want)
	}
	if view.ExpiresAt-view.CreatedAt != int64(30*time.Minute/time.Millisecond) {
		t.Fatalf("lifetime=%dms", view.ExpiresAt-view.CreatedAt)
	}
	// A wrong name is refused and does not use the token up.
	wrong := agentEnrollCall(app, token, "other")
	if wrong.Code != http.StatusConflict || jobErrorCode(t, wrong) != "enrollment_name_mismatch" {
		t.Fatalf("wrong name status=%d body=%s", wrong.Code, wrong.Body.String())
	}
	ok := agentEnrollCall(app, token, "东京 web 1")
	if ok.Code != http.StatusOK {
		t.Fatalf("enroll status=%d body=%s", ok.Code, ok.Body.String())
	}
	var enrolled agentEnrollResponse
	if err := json.Unmarshal(ok.Body.Bytes(), &enrolled); err != nil || enrolled.AgentID == "" || len(enrolled.AgentToken) != 43 || enrolled.Name != "东京 web 1" {
		t.Fatalf("enroll body=%s err=%v", ok.Body.String(), err)
	}
	// The returned credential authenticates the new Agent.
	claim := httptest.NewRequest(http.MethodPost, "/api/v1/agent/jobs/claim", strings.NewReader("{}"))
	claim.Header.Set("Authorization", "Bearer "+enrolled.AgentToken)
	claimResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(claimResponse, claim)
	if claimResponse.Code == http.StatusUnauthorized {
		t.Fatalf("new credential rejected: %s", claimResponse.Body.String())
	}
	again := agentEnrollCall(app, token, "东京 web 1")
	if again.Code != http.StatusConflict || jobErrorCode(t, again) != "enrollment_used" {
		t.Fatalf("second use status=%d body=%s", again.Code, again.Body.String())
	}
	if n := agentsNamed("东京 web 1"); n != 1 {
		t.Fatalf("agents created=%d", n)
	}
}

func TestOneClickEnrollmentExpiresRevokesAndRejectsGarbage(t *testing.T) {
	app, _ := oneClickApp(t)
	now := time.Unix(1_800_000_000, 0)
	app.now = func() time.Time { return now }
	_, expiring := createOneClick(t, app, "a")
	view, revoked := createOneClick(t, app, "b")

	revoke := postEnrollmentAPI(t, app, "/api/v1/web/agent-enrollments/"+view.EnrollmentID+"/revoke", `{}`, nil)
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	if response := agentEnrollCall(app, revoked, "b"); response.Code != http.StatusGone || jobErrorCode(t, response) != "enrollment_revoked" {
		t.Fatalf("revoked status=%d body=%s", response.Code, response.Body.String())
	}
	now = now.Add(30 * time.Minute)
	if response := agentEnrollCall(app, expiring, "a"); response.Code != http.StatusGone || jobErrorCode(t, response) != "enrollment_expired" {
		t.Fatalf("expired status=%d body=%s", response.Code, response.Body.String())
	}
	if response := agentEnrollCall(app, strings.Repeat("A", 43), "a"); response.Code != http.StatusUnauthorized || jobErrorCode(t, response) != "enrollment_invalid" {
		t.Fatalf("unknown status=%d body=%s", response.Code, response.Body.String())
	}
	if response := agentEnrollCall(app, "short", "a"); response.Code != http.StatusBadRequest {
		t.Fatalf("garbage status=%d", response.Code)
	}
	missing := postEnrollmentAPI(t, app, "/api/v1/web/agent-enrollments/"+strings.Repeat("0", 32)+"/revoke", `{}`, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown status=%d", missing.Code)
	}
}

func TestOneClickEnrollmentRevokeAfterUseIsRefused(t *testing.T) {
	app, _ := oneClickApp(t)
	view, token := createOneClick(t, app, "used")
	if response := agentEnrollCall(app, token, "used"); response.Code != http.StatusOK {
		t.Fatalf("enroll status=%d", response.Code)
	}
	response := postEnrollmentAPI(t, app, "/api/v1/web/agent-enrollments/"+view.EnrollmentID+"/revoke", `{}`, nil)
	if response.Code != http.StatusConflict || jobErrorCode(t, response) != "enrollment_used" {
		t.Fatalf("revoke used status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOneClickEnrollmentUnavailableOnDevelopmentBuilds(t *testing.T) {
	app, agentsNamed := oneClickApp(t)
	for _, info := range []buildinfo.Info{
		{Version: "dev", Commit: strings.Repeat("a", 40)},
		{Version: "v1.0.4", Commit: strings.Repeat("a", 40), Dirty: true},
		{Version: "v1.0.4"},
		{Version: "v1.0.4-rc.0", Commit: strings.Repeat("a", 40)},
	} {
		app.buildInfo = info
		response := postEnrollmentAPI(t, app, "/api/v1/web/agent-enrollments", `{"name":"x"}`, nil)
		if response.Code != http.StatusConflict || jobErrorCode(t, response) != "install_unavailable" {
			t.Fatalf("%+v status=%d body=%s", info, response.Code, response.Body.String())
		}
	}
	if n := agentsNamed("x"); n != 0 {
		t.Fatalf("agent created for an unavailable build: %d", n)
	}
}

func TestOneClickEnrollmentMutationBoundary(t *testing.T) {
	app, _ := oneClickApp(t)
	unauthenticated := webRequest(app, http.MethodPost, "/api/v1/web/agent-enrollments", strings.NewReader(`{"name":"x"}`), nil)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticated.Code)
	}
	for name, configure := range map[string]func(*http.Request, *webSession){
		"missing csrf": func(r *http.Request, _ *webSession) { r.Header.Del("X-CSRF-Token") },
		"wrong origin": func(r *http.Request, _ *webSession) { r.Header.Set("Origin", "https://attacker.test") },
	} {
		if response := postEnrollmentAPI(t, app, "/api/v1/web/agent-enrollments", `{"name":"x"}`, configure); response.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d", name, response.Code)
		}
	}
	for name, body := range map[string]string{"malformed": `{`, "unknown field": `{"name":"x","ttl":9}`, "empty name": `{"name":""}`, "padded name": `{"name":" x"}`} {
		if response := postEnrollmentAPI(t, app, "/api/v1/web/agent-enrollments", body, nil); response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d", name, response.Code)
		}
	}
	oversized := postEnrollmentAPI(t, app, "/api/v1/web/agent-enrollments", strings.Repeat("x", maxWebEnrollmentBodyBytes+1), nil)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d", oversized.Code)
	}
	// The Agent-side endpoint takes POST JSON only.
	get := httptest.NewRecorder()
	app.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/agent/enroll", nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", get.Code)
	}
}

func TestOneClickCommandQuotesTheAgentName(t *testing.T) {
	info := buildinfo.Info{Version: "v1.0.4", Commit: strings.Repeat("a", 40)}
	token := strings.Repeat("T", 43)
	for name, want := range map[string]string{
		"plain":           "PROBE_404_AGENT_NAME=plain bash",
		"with space":      "PROBE_404_AGENT_NAME='with space' bash",
		"it's":            `PROBE_404_AGENT_NAME='it'"'"'s' bash`,
		`say "hi"`:        `PROBE_404_AGENT_NAME='say "hi"' bash`,
		"$HOME `id`":      "PROBE_404_AGENT_NAME='$HOME `id`' bash",
		"a;b&c|d":         "PROBE_404_AGENT_NAME='a;b&c|d' bash",
		"东京 节点 #1":        "PROBE_404_AGENT_NAME='东京 节点 #1' bash",
		`back\slash`:      `PROBE_404_AGENT_NAME='back\slash' bash`,
		"line*glob?[x]~!": "PROBE_404_AGENT_NAME='line*glob?[x]~!' bash",
	} {
		command := webOneClickInstallCommand("https://probe.example.com", name, token, info)
		if !strings.HasSuffix(command, want) {
			t.Fatalf("name %q: command=%s want suffix %s", name, command, want)
		}
	}
}

func TestOneClickIsAvailableForCleanReleaseBuildsIncludingRC(t *testing.T) {
	app, _ := oneClickApp(t)
	for _, version := range []string{"v1.0.4", "v1.0.4-rc.4", "v1.0.4-beta.1"} {
		app.buildInfo = buildinfo.Info{Version: version, Commit: strings.Repeat("a", 40)}
		view, _ := createOneClick(t, app, "r")
		if !strings.Contains(view.Command, "/releases/download/"+version+"/install.sh") {
			t.Fatalf("%s: command=%s", version, view.Command)
		}
		if !webOneClickAvailable(app.buildInfo) {
			t.Fatalf("%s reported unavailable", version)
		}
	}
}
