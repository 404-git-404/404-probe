package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/storage"
)

func newSuffixPolicyTestApp(t *testing.T, suffixes ...string) (*App, *storage.Store) {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range suffixes {
		if _, _, err := store.AddWebDomainSuffix(context.Background(), suffix, time.Now()); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	app, err := NewApp(store, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{
		PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://legacy.example.net",
	}))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	app.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return app, store
}

func loginAtTarget(t *testing.T, app *App, host, origin string) (*http.Cookie, *webSession, *httptest.ResponseRecorder) {
	t.Helper()
	pageRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
	pageRequest.Host = host
	page := httptest.NewRecorder()
	app.Handler().ServeHTTP(page, pageRequest)
	if page.Code != http.StatusOK {
		t.Fatalf("login page host=%q status=%d body=%s", host, page.Code, page.Body.String())
	}
	match := webLoginTokenPattern.FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatalf("missing login token: %s", page.Body.String())
	}
	var loginCookie *http.Cookie
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == app.webAuth.loginCookieName() {
			loginCookie = cookie
		}
	}
	form := url.Values{"csrf_token": {match[1]}, "password": {"test-password"}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Host = host
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(loginCookie)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		return nil, nil, response
	}
	cookie := sessionCookieFromResponse(t, app, response)
	return cookie, app.webAuth.authenticate(cookie.Value, app.now()), response
}

func TestWebDomainSuffixTargetAdmissionAndExactOrigin(t *testing.T) {
	app, store := newSuffixPolicyTestApp(t, "navolyn.com", "example.com")
	defer store.Close()
	for _, host := range []string{"navolyn.com", "a.navolyn.com", "x.a.navolyn.com", "example.com", "b.example.com"} {
		request := httptest.NewRequest(http.MethodGet, "/login", nil)
		request.Host = host
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("allowed host=%q status=%d body=%s", host, response.Code, response.Body.String())
		}
	}
	for _, host := range []string{"evilnavolyn.com", "navolyn.com.evil.example", "legacy.example.net", "navolyn.com.", "user@navolyn.com", "navolyn.com/path", "navolyn.com:0"} {
		request := httptest.NewRequest(http.MethodGet, "/login", nil)
		request.Host = host
		request.Header.Set("Forwarded", "host=a.navolyn.com;proto=https")
		request.Header.Set("X-Forwarded-Host", "a.navolyn.com")
		request.Header.Set("X-Forwarded-Proto", "https")
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("rejected host=%q status=%d body=%s", host, response.Code, response.Body.String())
		}
	}

	if _, _, response := loginAtTarget(t, app, "a.navolyn.com", "https://b.navolyn.com"); response.Code != http.StatusForbidden {
		t.Fatalf("cross-subdomain login status=%d body=%s", response.Code, response.Body.String())
	}
	if _, _, response := loginAtTarget(t, app, "a.navolyn.com:8443", "https://a.navolyn.com"); response.Code != http.StatusForbidden {
		t.Fatalf("missing non-default port status=%d body=%s", response.Code, response.Body.String())
	}
	if _, session8443, response := loginAtTarget(t, app, "a.navolyn.com:8443", "https://a.navolyn.com:8443"); response.Code != http.StatusSeeOther || session8443.origin != "https://a.navolyn.com:8443" {
		t.Fatalf("matching non-default port status=%d session=%+v", response.Code, session8443)
	}
	cookie, session, response := loginAtTarget(t, app, "A.Navolyn.com:443", "https://a.navolyn.com")
	if response.Code != http.StatusSeeOther || session == nil || session.origin != "https://a.navolyn.com" {
		t.Fatalf("canonical login status=%d session=%+v", response.Code, session)
	}
	crossHost := httptest.NewRequest(http.MethodGet, "/api/v1/web/session", nil)
	crossHost.Host = "b.navolyn.com"
	crossHost.AddCookie(cookie)
	crossResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(crossResponse, crossHost)
	if crossResponse.Code != http.StatusUnauthorized {
		t.Fatalf("cross-host session status=%d body=%s", crossResponse.Code, crossResponse.Body.String())
	}
	mutation := httptest.NewRequest(http.MethodPost, "/api/v1/web/agents", strings.NewReader(`{"name":"blocked"}`))
	mutation.Host = "a.navolyn.com"
	mutation.AddCookie(cookie)
	mutation.Header.Set("Origin", "https://b.navolyn.com")
	mutation.Header.Set("Sec-Fetch-Site", "same-origin")
	mutation.Header.Set("X-CSRF-Token", session.csrfToken)
	mutation.Header.Set("Content-Type", "application/json")
	mutationResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(mutationResponse, mutation)
	if mutationResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-subdomain mutation status=%d body=%s", mutationResponse.Code, mutationResponse.Body.String())
	}
}

func TestWebLoginTokenCannotMoveBetweenAllowedSubdomains(t *testing.T) {
	app, store := newSuffixPolicyTestApp(t, "navolyn.com")
	defer store.Close()

	pageRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
	pageRequest.Host = "a.navolyn.com"
	page := httptest.NewRecorder()
	app.Handler().ServeHTTP(page, pageRequest)
	match := webLoginTokenPattern.FindStringSubmatch(page.Body.String())
	if page.Code != http.StatusOK || len(match) != 2 {
		t.Fatalf("login page status=%d body=%s", page.Code, page.Body.String())
	}
	var loginCookie *http.Cookie
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == app.webAuth.loginCookieName() {
			loginCookie = cookie
		}
	}
	if loginCookie == nil {
		t.Fatal("login page did not set the login token cookie")
	}
	form := url.Values{"csrf_token": {match[1]}, "password": {"test-password"}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Host = "b.navolyn.com"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://b.navolyn.com")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(loginCookie)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("moved login token status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebDomainPolicyChangesAreHotAndCloseSSE(t *testing.T) {
	app, store := newSuffixPolicyTestApp(t, "navolyn.com", "example.com")
	defer store.Close()
	defer app.Shutdown()
	cookie, session, response := loginAtTarget(t, app, "a.navolyn.com", "https://a.navolyn.com")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d", response.Code)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	eventRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/web/events", nil)
	eventRequest.Host = "a.navolyn.com"
	eventRequest.AddCookie(cookie)
	events, err := server.Client().Do(eventRequest)
	if err != nil || events.StatusCode != http.StatusOK {
		t.Fatalf("events=%v err=%v", events, err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, events.Body)
		close(done)
	}()
	if _, _, err := store.RemoveWebDomainSuffix(context.Background(), "navolyn.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	app.hub.publish([]byte(`{"changed":true}`))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		events.Body.Close()
		t.Fatal("SSE remained open after its suffix was removed")
	}
	select {
	case <-session.done:
	default:
		t.Fatal("removed suffix did not revoke bound session")
	}
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	request.Host = "new.navolyn.com"
	blocked := httptest.NewRecorder()
	app.Handler().ServeHTTP(blocked, request)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("removed suffix status=%d", blocked.Code)
	}
	if _, _, err := store.AddWebDomainSuffix(context.Background(), "navolyn.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	allowed := httptest.NewRecorder()
	app.Handler().ServeHTTP(allowed, request)
	if allowed.Code != http.StatusOK {
		t.Fatalf("hot re-add status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestRemovedAndReaddedSuffixDoesNotReviveCredentials(t *testing.T) {
	app, store := newSuffixPolicyTestApp(t, "navolyn.com", "example.com")
	defer store.Close()
	cookie, _, response := loginAtTarget(t, app, "a.navolyn.com", "https://a.navolyn.com")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d", response.Code)
	}
	loginCookie, token, _ := func() (*http.Cookie, string, *httptest.ResponseRecorder) {
		pageRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
		pageRequest.Host = "a.navolyn.com"
		page := httptest.NewRecorder()
		app.Handler().ServeHTTP(page, pageRequest)
		match := webLoginTokenPattern.FindStringSubmatch(page.Body.String())
		if len(match) != 2 {
			t.Fatalf("missing login token: %s", page.Body.String())
		}
		for _, item := range page.Result().Cookies() {
			if item.Name == app.webAuth.loginCookieName() {
				return item, match[1], page
			}
		}
		t.Fatal("missing login cookie")
		return nil, "", page
	}()
	if _, _, err := store.RemoveWebDomainSuffix(context.Background(), "navolyn.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddWebDomainSuffix(context.Background(), "navolyn.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/web/session", nil)
	sessionRequest.Host = "a.navolyn.com"
	sessionRequest.AddCookie(cookie)
	sessionResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revived session status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}
	form := url.Values{"csrf_token": {token}, "password": {"test-password"}}
	loginRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	loginRequest.Host = "a.navolyn.com"
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.Header.Set("Origin", "https://a.navolyn.com")
	loginRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	loginRequest.AddCookie(loginCookie)
	loginResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusForbidden {
		t.Fatalf("revived login token status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
}

func TestInsecureLoopbackOriginCannotExpandToSuffixHosts(t *testing.T) {
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.AddWebDomainSuffix(context.Background(), "navolyn.com", time.Now()); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(store, 30*time.Second, nil, WithWebAuthentication(WebAuthenticationConfig{
		PasswordHash: webTestPasswordHash(t), PublicOrigin: "http://127.0.0.1:8080", AllowInsecureHTTP: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	request.Host = "a.navolyn.com"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("insecure suffix host status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAgentInstallCommandUsesCurrentAllowedTarget(t *testing.T) {
	app, store := newSuffixPolicyTestApp(t, "navolyn.com")
	defer store.Close()
	app.buildInfo = buildinfo.Info{Version: "v0.9.2", Commit: strings.Repeat("a", 40)}
	cookie, session, response := loginAtTarget(t, app, "new.navolyn.com", "https://new.navolyn.com")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d", response.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/web/agents", strings.NewReader(`{"name":"new"}`))
	request.Host = "new.navolyn.com"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "https://new.navolyn.com")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-CSRF-Token", session.csrfToken)
	request.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	app.Handler().ServeHTTP(created, request)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `--server 'https://new.navolyn.com'`) || strings.Contains(created.Body.String(), "legacy.example.net") {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
}

func TestLegacyExactModeDoesNotExpandPublicOrigin(t *testing.T) {
	app, store := newSuffixPolicyTestApp(t)
	defer store.Close()
	for host, want := range map[string]int{"legacy.example.net": http.StatusOK, "a.example.net": http.StatusForbidden} {
		request := httptest.NewRequest(http.MethodGet, "/login", nil)
		request.Host = host
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("host=%q status=%d want=%d", host, response.Code, want)
		}
	}
}
