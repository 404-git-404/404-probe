package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"404-probe/internal/auth"
)

func webAgentCreateResponse(t *testing.T, app *App, body string, configure func(*http.Request, *webSession)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/web/agents", strings.NewReader(body))
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

func TestWebAgentCreateMutationBoundary(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	valid := `{"name":"lax-01"}`

	unauthenticated := webRequest(app, http.MethodPost, "/api/v1/web/agents", strings.NewReader(`{`), nil)
	if unauthenticated.Code != http.StatusUnauthorized || jobErrorCode(t, unauthenticated) != "unauthorized" {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	invalidSession := webAgentCreateResponse(t, app, `{`, func(r *http.Request, _ *webSession) {
		r.Header.Set("Cookie", app.webAuth.sessionCookieName()+"=invalid")
	})
	if invalidSession.Code != http.StatusUnauthorized || jobErrorCode(t, invalidSession) != "unauthorized" {
		t.Fatalf("invalid session status=%d body=%s", invalidSession.Code, invalidSession.Body.String())
	}

	tests := []struct {
		name      string
		body      string
		configure func(*http.Request, *webSession)
		status    int
		code      string
	}{
		{"missing csrf", valid, func(r *http.Request, _ *webSession) { r.Header.Del("X-CSRF-Token") }, http.StatusForbidden, "forbidden"},
		{"invalid csrf", valid, func(r *http.Request, _ *webSession) { r.Header.Set("X-CSRF-Token", "wrong") }, http.StatusForbidden, "forbidden"},
		{"multiple csrf", valid, func(r *http.Request, s *webSession) { r.Header.Add("X-CSRF-Token", s.csrfToken) }, http.StatusForbidden, "forbidden"},
		{"missing origin", valid, func(r *http.Request, _ *webSession) { r.Header.Del("Origin") }, http.StatusForbidden, "forbidden"},
		{"wrong origin", valid, func(r *http.Request, _ *webSession) { r.Header.Set("Origin", "https://attacker.test") }, http.StatusForbidden, "forbidden"},
		{"null origin", valid, func(r *http.Request, _ *webSession) { r.Header.Set("Origin", "null") }, http.StatusForbidden, "forbidden"},
		{"multiple origin", valid, func(r *http.Request, _ *webSession) { r.Header.Add("Origin", "https://probe.test") }, http.StatusForbidden, "forbidden"},
		{"missing fetch site", valid, func(r *http.Request, _ *webSession) { r.Header.Del("Sec-Fetch-Site") }, http.StatusForbidden, "forbidden"},
		{"cross site", valid, func(r *http.Request, _ *webSession) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden, "forbidden"},
		{"multiple fetch site", valid, func(r *http.Request, _ *webSession) { r.Header.Add("Sec-Fetch-Site", "same-origin") }, http.StatusForbidden, "forbidden"},
		{"wrong content type", valid, func(r *http.Request, _ *webSession) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType, "invalid_content_type"},
		{"query", valid, func(r *http.Request, _ *webSession) { r.URL.RawQuery = "x=1" }, http.StatusBadRequest, "invalid_query"},
		{"malformed", `{`, nil, http.StatusBadRequest, "invalid_request"},
		{"unknown field", `{"name":"lax-01","token":"secret"}`, nil, http.StatusBadRequest, "invalid_request"},
		{"multiple values", valid + valid, nil, http.StatusBadRequest, "invalid_request"},
		{"oversized", strings.Repeat("x", maxWebAgentCreateBodyBytes+1), nil, http.StatusRequestEntityTooLarge, "request_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := webAgentCreateResponse(t, app, test.body, test.configure)
			if response.Code != test.status || jobErrorCode(t, response) != test.code || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}
}

func TestWebAgentCreateAndShownOnceCredential(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&logs, nil))

	response := webAgentCreateResponse(t, app, `{"name":"lax-01"}`, nil)
	if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var created webAgentEnrollmentView
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Agent.AgentID == "" || created.Agent.Name != "lax-01" || created.Agent.Revoked || created.Agent.Online || created.Agent.LastSeen != nil {
		t.Fatalf("agent=%+v", created.Agent)
	}
	if !strings.Contains(created.InstallCommand, "/v0.8.0/install.sh") || strings.Contains(created.InstallCommand, created.EnrollmentValue) ||
		!strings.Contains(created.InstallCommand, "--server 'https://probe.test'") {
		t.Fatalf("install command=%q", created.InstallCommand)
	}
	if !strings.HasPrefix(created.EnrollmentValue, auth.EnrollmentPrefix) {
		t.Fatalf("enrollment=%q", created.EnrollmentValue)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(created.EnrollmentValue, auth.EnrollmentPrefix))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(payload), ":")
	if len(parts) != 2 || parts[0] != created.Agent.AgentID {
		t.Fatalf("payload=%q agent=%q", payload, created.Agent.AgentID)
	}
	if authenticatedID, err := store.Authenticate(context.Background(), parts[1]); err != nil || authenticatedID != created.Agent.AgentID {
		t.Fatalf("authenticated=%q err=%v", authenticatedID, err)
	}

	for _, path := range []string{"/api/v1/web/agents?limit=100", "/api/v1/web/agents/" + created.Agent.AgentID} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		addTestWebSession(t, app, request)
		read := httptest.NewRecorder()
		app.Handler().ServeHTTP(read, request)
		if read.Code != http.StatusOK || strings.Contains(read.Body.String(), parts[1]) || strings.Contains(read.Body.String(), created.EnrollmentValue) {
			t.Fatalf("path=%s status=%d body=%s", path, read.Code, read.Body.String())
		}
	}
	if strings.Contains(logs.String(), parts[1]) || strings.Contains(logs.String(), created.EnrollmentValue) {
		t.Fatalf("credential leaked to logs: %s", logs.String())
	}

	second := webAgentCreateResponse(t, app, `{"name":"lax-01"}`, nil)
	if second.Code != http.StatusCreated {
		t.Fatalf("duplicate name status=%d body=%s", second.Code, second.Body.String())
	}
	agents, err := store.ListAgents(context.Background())
	if err != nil || len(agents) != 3 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
}

func TestWebAgentCreateRejectsInvalidNames(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	for _, name := range []string{"", " lax", "lax ", "bad\nname", "bad\u0000name", strings.Repeat("x", maxWebAgentNameBytes+1)} {
		body := fmt.Sprintf(`{"name":%q}`, name)
		response := webAgentCreateResponse(t, app, body, nil)
		if response.Code != http.StatusBadRequest || jobErrorCode(t, response) != "invalid_request" {
			t.Fatalf("name=%q status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
}

func TestWebAgentInstallCommandQuotesOrigin(t *testing.T) {
	command := webAgentInstallCommand("https://probe.example.com")
	want := "curl -fsSL '" + webAgentInstallerURL + "' | sudo bash -s -- agent --server 'https://probe.example.com'"
	if command != want {
		t.Fatalf("command=%q want=%q", command, want)
	}
	if quoted := shellSingleQuote("a'b"); quoted != `'a'"'"'b'` {
		t.Fatalf("quoted=%q", quoted)
	}
}
