package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/storage"
)

var webLoginTokenPattern = regexp.MustCompile(`name="csrf_token" value="([A-Za-z0-9_-]+)"`)

func newWebAuthenticationTestApp(t *testing.T) (*App, *storage.Store, string) {
	t.Helper()
	app, store, agentID, _ := testApp(t)
	now := time.Unix(1_800_000_000, 0)
	app.now = func() time.Time { return now }
	return app, store, agentID
}

func webLoginPage(t *testing.T, app *App) (*http.Cookie, string, *httptest.ResponseRecorder) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login page status=%d body=%s", response.Code, response.Body.String())
	}
	match := webLoginTokenPattern.FindStringSubmatch(response.Body.String())
	if len(match) != 2 || !validWebToken(match[1]) {
		t.Fatalf("missing login token: %s", response.Body.String())
	}
	var loginCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == app.webAuth.loginCookieName() {
			loginCookie = cookie
		}
	}
	if loginCookie == nil || loginCookie.Value != match[1] {
		t.Fatalf("login cookie=%v token=%q", loginCookie, match[1])
	}
	return loginCookie, match[1], response
}

func postWebLogin(t *testing.T, app *App, password, origin string) *httptest.ResponseRecorder {
	t.Helper()
	loginCookie, token, _ := webLoginPage(t, app)
	form := url.Values{"csrf_token": {token}, "password": {password}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(loginCookie)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func sessionCookieFromResponse(t *testing.T, app *App, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == app.webAuth.sessionCookieName() && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatalf("missing session cookie: %v", response.Header().Values("Set-Cookie"))
	return nil
}

func webRequest(app *App, method, path string, body io.Reader, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, body)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func TestWebAuthenticationDisabledIsFailClosed(t *testing.T) {
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app, err := NewApp(store, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/login", "/api/v1/agents", "/api/v1/web/agents", "/api/v1/events"} {
		response := webRequest(app, http.MethodGet, path, nil, nil)
		if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("path=%s status=%d cache=%q", path, response.Code, response.Header().Get("Cache-Control"))
		}
	}
}

func TestWebLoginSessionProofAndCookieSecurity(t *testing.T) {
	app, store, agentID := newWebAuthenticationTestApp(t)
	defer store.Close()
	response := postWebLogin(t, app, "test-password", "https://probe.test")
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
		t.Fatalf("login status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	cookie := sessionCookieFromResponse(t, app, response)
	if cookie.Name != "__Host-404-probe-session" || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" ||
		cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != int(webSessionAbsoluteTimeout.Seconds()) {
		t.Fatalf("session cookie=%+v", cookie)
	}
	proof := webRequest(app, http.MethodGet, "/api/v1/web/agents", nil, cookie)
	if proof.Code != http.StatusOK || proof.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(proof.Body.String(), agentID) || strings.Contains(proof.Body.String(), "token") {
		t.Fatalf("proof status=%d cache=%q body=%s", proof.Code, proof.Header().Get("Cache-Control"), proof.Body.String())
	}
	sessionResponse := webRequest(app, http.MethodGet, "/api/v1/web/session", nil, cookie)
	var sessionView struct {
		Principal string `json:"principal"`
		CSRFToken string `json:"csrf_token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if sessionResponse.Code != http.StatusOK || json.Unmarshal(sessionResponse.Body.Bytes(), &sessionView) != nil ||
		sessionView.Principal != "admin" || !validWebToken(sessionView.CSRFToken) || sessionView.ExpiresAt <= app.now().UnixMilli() {
		t.Fatalf("session status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}
	for _, header := range []string{"Content-Security-Policy", "Permissions-Policy", "Strict-Transport-Security", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if sessionResponse.Header().Get(header) == "" {
			t.Fatalf("missing security header %s", header)
		}
	}
}

func TestWebAuthenticationPrecedesQueryValidation(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	unauthenticated := webRequest(app, http.MethodGet, "/api/v1/web/agents?unknown=x", nil, nil)
	if unauthenticated.Code != http.StatusUnauthorized || !strings.Contains(unauthenticated.Body.String(), "authentication required") {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/web/agents?unknown=x", nil)
	addTestWebSession(t, app, request)
	authenticated := httptest.NewRecorder()
	app.Handler().ServeHTTP(authenticated, request)
	if authenticated.Code != http.StatusBadRequest || !strings.Contains(authenticated.Body.String(), "invalid_query") {
		t.Fatalf("authenticated status=%d body=%s", authenticated.Code, authenticated.Body.String())
	}
}

func TestWebLoginRejectsOriginCSRFAndReplays(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	wrongOrigin := postWebLogin(t, app, "test-password", "https://attacker.test")
	if wrongOrigin.Code != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d body=%s", wrongOrigin.Code, wrongOrigin.Body.String())
	}
	loginCookie, token, _ := webLoginPage(t, app)
	form := url.Values{"csrf_token": {token}, "password": {"wrong"}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://probe.test")
	request.AddCookie(loginCookie)
	first := httptest.NewRecorder()
	app.Handler().ServeHTTP(first, request)
	if first.Code != http.StatusUnauthorized || strings.Contains(first.Body.String(), "wrong") {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	replay.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replay.Header.Set("Origin", "https://probe.test")
	replay.AddCookie(loginCookie)
	replayed := httptest.NewRecorder()
	app.Handler().ServeHTTP(replayed, replay)
	if replayed.Code != http.StatusForbidden {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
}

func TestWebLogoutCSRFAndImmediateInvalidation(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	login := postWebLogin(t, app, "test-password", "https://probe.test")
	cookie := sessionCookieFromResponse(t, app, login)
	session := app.webAuth.authenticate(cookie.Value, app.now())
	bad := url.Values{"csrf_token": {"wrong"}}
	badRequest := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(bad.Encode()))
	badRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badRequest.Header.Set("Origin", "https://probe.test")
	badRequest.AddCookie(cookie)
	badResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusForbidden {
		t.Fatalf("bad CSRF status=%d body=%s", badResponse.Code, badResponse.Body.String())
	}
	form := url.Values{"csrf_token": {session.csrfToken}}
	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://probe.test")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
		t.Fatalf("logout status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-session.done:
	default:
		t.Fatal("logout did not cancel session streams")
	}
	if after := webRequest(app, http.MethodGet, "/api/v1/web/agents", nil, cookie); after.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d", after.Code)
	}
}

func TestWebLogoutClosesAuthenticatedSSE(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	plain, session, err := app.webAuth.createSession(app.now())
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: app.webAuth.sessionCookieName(), Value: plain}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	eventRequest, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	eventRequest.AddCookie(cookie)
	events, err := client.Do(eventRequest)
	if err != nil || events.StatusCode != http.StatusOK {
		t.Fatalf("events status=%v err=%v", events, err)
	}
	eventsDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(io.Discard, events.Body)
		eventsDone <- copyErr
	}()
	form := url.Values{"csrf_token": {session.csrfToken}}
	logoutRequest, err := http.NewRequest(http.MethodPost, server.URL+"/logout", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	logoutRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logoutRequest.Header.Set("Origin", "https://probe.test")
	logoutRequest.AddCookie(cookie)
	logout, err := client.Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	logout.Body.Close()
	if logout.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout status=%d", logout.StatusCode)
	}
	select {
	case <-eventsDone:
	case <-time.After(time.Second):
		events.Body.Close()
		t.Fatal("SSE remained open after logout")
	}
	events.Body.Close()
}

func TestWebSessionIdleAbsoluteExpiryAndLimit(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	base := app.now()
	plain, idle, err := app.webAuth.createSession(base)
	if err != nil {
		t.Fatal(err)
	}
	if app.webAuth.authenticate(plain, base.Add(webSessionIdleTimeout)) != nil {
		t.Fatal("idle-expired session accepted")
	}
	select {
	case <-idle.done:
	default:
		t.Fatal("idle expiry did not cancel session")
	}
	plain, absolute, err := app.webAuth.createSession(base)
	if err != nil {
		t.Fatal(err)
	}
	app.webAuth.mu.Lock()
	absolute.lastSeen = base.Add(webSessionAbsoluteTimeout - time.Minute)
	app.webAuth.mu.Unlock()
	if app.webAuth.authenticate(plain, base.Add(webSessionAbsoluteTimeout)) != nil {
		t.Fatal("absolute-expired session accepted")
	}
	for index := 0; index < webSessionLimit+1; index++ {
		if _, _, err := app.webAuth.createSession(base.Add(time.Duration(index) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	app.webAuth.mu.Lock()
	count := len(app.webAuth.sessions)
	app.webAuth.mu.Unlock()
	if count != webSessionLimit {
		t.Fatalf("sessions=%d want %d", count, webSessionLimit)
	}
}

func TestWebLoginRateLimitAndControlBoundary(t *testing.T) {
	app, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	client := "192.0.2.10"
	for index := 0; index < webLoginFailuresPerIP; index++ {
		app.webAuth.recordLoginFailure(client, app.now())
	}
	if app.webAuth.beginPasswordCheck(client, app.now()) {
		app.webAuth.endPasswordCheck()
		t.Fatal("rate-limited client acquired password slot")
	}
	controlToken, controlHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	controlStore, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer controlStore.Close()
	controlApp, err := NewApp(controlStore, 30*time.Second, nil, WithControlTokenHash(controlHash), WithWebAuthentication(WebAuthenticationConfig{
		PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/control/agents", nil)
	addTestWebSession(t, controlApp, request)
	response := httptest.NewRecorder()
	controlApp.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), controlToken) {
		t.Fatalf("Web session crossed Control boundary: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAuthenticationConfigurationRejectsUnsafeOrigins(t *testing.T) {
	for _, test := range []WebAuthenticationConfig{
		{PasswordHash: "bad", PublicOrigin: "https://probe.test"},
		{PasswordHash: webTestPasswordHash(t), PublicOrigin: "http://probe.example"},
		{PasswordHash: webTestPasswordHash(t), PublicOrigin: "http://127.0.0.1", AllowInsecureHTTP: false},
		{PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test/path"},
	} {
		if _, err := newWebAuthenticator(test); err == nil {
			t.Fatalf("accepted config %+v", test)
		}
	}
	development, err := newWebAuthenticator(WebAuthenticationConfig{
		PasswordHash: webTestPasswordHash(t), PublicOrigin: "http://127.0.0.1:8080", AllowInsecureHTTP: true,
	})
	if err != nil || development.secureCookie || development.sessionCookieName() == "__Host-404-probe-session" {
		t.Fatalf("loopback development config=%+v err=%v", development, err)
	}
}
