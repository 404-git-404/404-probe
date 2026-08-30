package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"404-probe/internal/auth"
)

const (
	webSessionIdleTimeout     = 30 * time.Minute
	webSessionAbsoluteTimeout = 12 * time.Hour
	webSessionLimit           = 16
	webLoginTokenTTL          = 10 * time.Minute
	webLoginTokenLimit        = 128
	webLoginBodyBytes         = 2 << 10
	webLoginWindow            = 10 * time.Minute
	webLoginFailuresPerIP     = 5
	webLoginFailuresGlobal    = 50
	webLoginIPLimit           = 1024
)

var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>404-probe · 登录</title><link rel="stylesheet" href="/style.css"></head>
<body><main class="login"><article class="login-card"><h1>404-probe</h1><p>管理员登录</p>
{{if .Failed}}<div class="login-error" role="alert">登录失败，请稍后重试。</div>{{end}}
<form method="post" action="/login"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
<label for="password">密码</label><input id="password" name="password" type="password" maxlength="1024" required autocomplete="current-password" autofocus>
<button type="submit">登录</button></form></article></main></body></html>`))

type WebAuthenticationConfig struct {
	PasswordHash      string
	PublicOrigin      string
	AllowInsecureHTTP bool
}

type webAuthenticator struct {
	passwordHash string
	publicOrigin *url.URL
	secureCookie bool

	mu            sync.Mutex
	sessions      map[string]*webSession
	loginTokens   map[string]time.Time
	ipFailures    map[string][]time.Time
	globalFailure []time.Time
	passwordSlots chan struct{}
}

type webSession struct {
	key       string
	csrfToken string
	createdAt time.Time
	lastSeen  time.Time
	done      chan struct{}
}

type webSessionContextKey struct{}

type webLoginPageView struct {
	CSRFToken string
	Failed    bool
}

func WithWebAuthentication(config WebAuthenticationConfig) Option {
	return func(app *App) error {
		if app.webAuth != nil {
			return errors.New("Web authentication is already configured")
		}
		authenticator, err := newWebAuthenticator(config)
		if err != nil {
			return err
		}
		app.webAuth = authenticator
		return nil
	}
}

func newWebAuthenticator(config WebAuthenticationConfig) (*webAuthenticator, error) {
	if err := auth.ParsePasswordHash(config.PasswordHash); err != nil {
		return nil, errors.New("Web password hash is invalid")
	}
	origin, err := parseWebPublicOrigin(config.PublicOrigin, config.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	return &webAuthenticator{
		passwordHash: config.PasswordHash, publicOrigin: origin, secureCookie: origin.Scheme == "https",
		sessions: make(map[string]*webSession), loginTokens: make(map[string]time.Time),
		ipFailures: make(map[string][]time.Time), passwordSlots: make(chan struct{}, 2),
	}, nil
}

func parseWebPublicOrigin(value string, allowInsecure bool) (*url.URL, error) {
	origin, err := url.Parse(value)
	if err != nil || origin.User != nil || origin.Host == "" || origin.RawQuery != "" || origin.Fragment != "" ||
		(origin.Path != "" && origin.Path != "/") {
		return nil, errors.New("Web public origin must be an absolute origin without a path")
	}
	origin.Path = ""
	switch origin.Scheme {
	case "https":
	case "http":
		if !allowInsecure || !webLoopbackHost(origin.Hostname()) {
			return nil, errors.New("Web public origin must use HTTPS; insecure HTTP is allowed only for explicit loopback development")
		}
	default:
		return nil, errors.New("Web public origin must use HTTPS")
	}
	return origin, nil
}

func webLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (a *App) webRoutes(mux *http.ServeMux, static http.Handler) {
	mux.HandleFunc("GET /login", a.handleWebLoginPage)
	mux.HandleFunc("POST /login", a.handleWebLogin)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("POST /logout", a.requireWebSession(http.HandlerFunc(a.handleWebLogout), true))
	mux.Handle("GET /api/v1/web/session", a.requireWebSession(http.HandlerFunc(a.handleWebSession), true))
	mux.Handle("POST /api/v1/web/agents", a.requireWebMutation(http.HandlerFunc(a.handleCreateWebAgent)))
	mux.Handle("GET /api/v1/web/agents", a.requireWebSession(http.HandlerFunc(a.handleGetWebAgents), true))
	mux.Handle("/api/v1/web/agents", a.requireWebSession(http.HandlerFunc(a.handleWebAgentCollectionMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/agents/{agent_id}/history", a.requireWebSession(http.HandlerFunc(a.handleGetWebAgentHistory), true))
	mux.Handle("/api/v1/web/agents/{agent_id}/history", a.requireWebSession(http.HandlerFunc(a.handleWebAgentHistoryMethodNotAllowed), true))
	mux.Handle("POST /api/v1/web/agents/{agent_id}/revoke", a.requireWebMutation(http.HandlerFunc(a.handleRevokeWebAgent)))
	mux.Handle("/api/v1/web/agents/{agent_id}/revoke", a.requireWebSession(http.HandlerFunc(a.handleWebAgentRevokeMethodNotAllowed), true))
	mux.Handle("POST /api/v1/web/agents/{agent_id}/disable", a.requireWebMutation(http.HandlerFunc(a.handleDisableWebAgent)))
	mux.Handle("/api/v1/web/agents/{agent_id}/disable", a.requireWebSession(http.HandlerFunc(a.handleWebAgentStateMethodNotAllowed), true))
	mux.Handle("POST /api/v1/web/agents/{agent_id}/enable", a.requireWebMutation(http.HandlerFunc(a.handleEnableWebAgent)))
	mux.Handle("/api/v1/web/agents/{agent_id}/enable", a.requireWebSession(http.HandlerFunc(a.handleWebAgentStateMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/agents/{agent_id}", a.requireWebSession(http.HandlerFunc(a.handleGetWebAgent), true))
	mux.Handle("/api/v1/web/agents/{agent_id}", a.requireWebSession(http.HandlerFunc(a.handleWebAgentMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/schedules", a.requireWebSession(http.HandlerFunc(a.handleGetWebSchedules), true))
	mux.Handle("/api/v1/web/schedules", a.requireWebSession(http.HandlerFunc(a.handleWebScheduleCollectionMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/schedules/{schedule_id}", a.requireWebSession(http.HandlerFunc(a.handleGetWebSchedule), true))
	mux.Handle("/api/v1/web/schedules/{schedule_id}", a.requireWebSession(http.HandlerFunc(a.handleWebScheduleMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/jobs", a.requireWebSession(http.HandlerFunc(a.handleGetWebJobs), true))
	mux.Handle("/api/v1/web/jobs", a.requireWebSession(http.HandlerFunc(a.handleWebJobCollectionMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/jobs/{job_id}", a.requireWebSession(http.HandlerFunc(a.handleGetWebJob), true))
	mux.Handle("/api/v1/web/jobs/{job_id}", a.requireWebSession(http.HandlerFunc(a.handleWebJobMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/events", a.requireWebSession(http.HandlerFunc(a.handleEvents), true))
	mux.Handle("/api/v1/web/events", a.requireWebSession(http.HandlerFunc(a.handleWebEventsMethodNotAllowed), true))
	mux.Handle("/api/v1/web/", a.requireWebSession(http.NotFoundHandler(), true))
	mux.Handle("/api/v1/agents", a.requireWebSession(http.NotFoundHandler(), true))
	mux.Handle("/api/v1/agents/", a.requireWebSession(http.NotFoundHandler(), true))
	mux.Handle("/api/v1/events", a.requireWebSession(http.NotFoundHandler(), true))
	mux.Handle("/api/v1/events/", a.requireWebSession(http.NotFoundHandler(), true))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		setWebNoStore(w)
		http.NotFound(w, r)
	})
	mux.Handle("GET /style.css", static)
	mux.Handle("/", a.requireWebSession(static, false))
}

func (a *App) requireWebMutation(next http.Handler) http.Handler {
	return a.requireWebSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := webSessionFromContext(r.Context())
		originValues := r.Header.Values("Origin")
		fetchSiteValues := r.Header.Values("Sec-Fetch-Site")
		csrfValues := r.Header.Values("X-CSRF-Token")
		if session == nil || len(originValues) != 1 || len(fetchSiteValues) != 1 || fetchSiteValues[0] != "same-origin" ||
			!a.webAuth.sameOriginRequest(r) || len(csrfValues) != 1 ||
			subtle.ConstantTimeCompare([]byte(csrfValues[0]), []byte(session.csrfToken)) != 1 {
			writeJobError(w, http.StatusForbidden, "forbidden", "request rejected")
			return
		}
		next.ServeHTTP(w, r)
	}), true)
}

func (a *App) requireWebSession(next http.Handler, api bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setWebNoStore(w)
		if a.webAuth == nil {
			http.NotFound(w, r)
			return
		}
		cookie, err := r.Cookie(a.webAuth.sessionCookieName())
		if err != nil {
			a.writeWebUnauthenticated(w, r, api)
			return
		}
		session := a.webAuth.authenticate(cookie.Value, a.now())
		if session == nil {
			a.webAuth.clearSessionCookie(w)
			a.writeWebUnauthenticated(w, r, api)
			return
		}
		ctx := context.WithValue(r.Context(), webSessionContextKey{}, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) writeWebUnauthenticated(w http.ResponseWriter, r *http.Request, api bool) {
	if api {
		writeJobError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) handleWebLoginPage(w http.ResponseWriter, r *http.Request) {
	setWebNoStore(w)
	if a.webAuth == nil {
		http.NotFound(w, r)
		return
	}
	if cookie, err := r.Cookie(a.webAuth.sessionCookieName()); err == nil && a.webAuth.authenticate(cookie.Value, a.now()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.renderWebLogin(w, http.StatusOK, false)
}

func (a *App) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	setWebNoStore(w)
	if a.webAuth == nil {
		http.NotFound(w, r)
		return
	}
	if !a.webAuth.sameOriginRequest(r) {
		writeJobError(w, http.StatusForbidden, "forbidden", "request rejected")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid login request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, webLoginBodyBytes)
	if err := r.ParseForm(); err != nil || !exactWebForm(r.PostForm, "csrf_token", "password") {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid login request")
		return
	}
	loginCookie, err := r.Cookie(a.webAuth.loginCookieName())
	if err != nil || !a.webAuth.consumeLoginToken(loginCookie.Value, r.PostForm.Get("csrf_token"), a.now()) {
		writeJobError(w, http.StatusForbidden, "forbidden", "request rejected")
		return
	}
	client := webClientAddress(r.RemoteAddr)
	if !a.webAuth.beginPasswordCheck(client, a.now()) {
		w.Header().Set("Retry-After", "60")
		a.renderWebLogin(w, http.StatusTooManyRequests, true)
		return
	}
	valid := auth.VerifyPassword(a.webAuth.passwordHash, []byte(r.PostForm.Get("password")))
	a.webAuth.endPasswordCheck()
	if !valid {
		a.webAuth.recordLoginFailure(client, a.now())
		a.renderWebLogin(w, http.StatusUnauthorized, true)
		return
	}
	a.webAuth.recordLoginSuccess(client, a.now())
	plain, _, err := a.webAuth.createSession(a.now())
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create session")
		return
	}
	a.webAuth.setSessionCookie(w, plain, a.now())
	a.webAuth.clearLoginCookie(w)
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}

func (a *App) renderWebLogin(w http.ResponseWriter, status int, failed bool) {
	token, err := a.webAuth.issueLoginToken(a.now())
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create login request")
		return
	}
	a.webAuth.setLoginCookie(w, token, a.now())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = loginTemplate.Execute(w, webLoginPageView{CSRFToken: token, Failed: failed})
}

func (a *App) handleWebLogout(w http.ResponseWriter, r *http.Request) {
	session := webSessionFromContext(r.Context())
	if session == nil || !a.webAuth.sameOriginRequest(r) {
		writeJobError(w, http.StatusForbidden, "forbidden", "request rejected")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_request", "invalid logout request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, webLoginBodyBytes)
	if err := r.ParseForm(); err != nil || !exactWebForm(r.PostForm, "csrf_token") ||
		subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf_token")), []byte(session.csrfToken)) != 1 {
		writeJobError(w, http.StatusForbidden, "forbidden", "request rejected")
		return
	}
	a.webAuth.revokeSession(session)
	a.webAuth.clearSessionCookie(w)
	w.Header().Set("Location", "/login")
	w.WriteHeader(http.StatusSeeOther)
}

func (a *App) handleWebSession(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeJobError(w, http.StatusBadRequest, "invalid_query", "invalid session query")
		return
	}
	session := webSessionFromContext(r.Context())
	deadline := a.webAuth.sessionDeadline(session)
	writeJSON(w, http.StatusOK, struct {
		Principal string `json:"principal"`
		CSRFToken string `json:"csrf_token"`
		ExpiresAt int64  `json:"expires_at"`
	}{Principal: "admin", CSRFToken: session.csrfToken, ExpiresAt: deadline.UnixMilli()})
}

func exactWebForm(values url.Values, names ...string) bool {
	if len(values) != len(names) {
		return false
	}
	for _, name := range names {
		entries, ok := values[name]
		if !ok || len(entries) != 1 || entries[0] == "" {
			return false
		}
	}
	return true
}

func webSessionFromContext(ctx context.Context) *webSession {
	session, _ := ctx.Value(webSessionContextKey{}).(*webSession)
	return session
}

func webClientAddress(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err == nil && host != "" {
		return host
	}
	if remote == "" {
		return "unknown"
	}
	return remote
}

func (w *webAuthenticator) sameOriginRequest(r *http.Request) bool {
	if fetchSite := r.Header.Get("Sec-Fetch-Site"); fetchSite != "" && fetchSite != "same-origin" {
		return false
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && origin.User == nil && origin.RawQuery == "" && origin.Fragment == "" &&
		(origin.Path == "" || origin.Path == "/") && strings.EqualFold(origin.Scheme, w.publicOrigin.Scheme) &&
		strings.EqualFold(origin.Host, w.publicOrigin.Host)
}

func (w *webAuthenticator) issueLoginToken(now time.Time) (string, error) {
	plain, err := randomWebToken()
	if err != nil {
		return "", err
	}
	key := webTokenKey(plain)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLoginTokensLocked(now)
	if len(w.loginTokens) >= webLoginTokenLimit {
		w.removeOldestLoginTokenLocked()
	}
	w.loginTokens[key] = now.Add(webLoginTokenTTL)
	return plain, nil
}

func (w *webAuthenticator) consumeLoginToken(cookie, form string, now time.Time) bool {
	if subtle.ConstantTimeCompare([]byte(cookie), []byte(form)) != 1 || !validWebToken(form) {
		return false
	}
	key := webTokenKey(form)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLoginTokensLocked(now)
	expiresAt, ok := w.loginTokens[key]
	delete(w.loginTokens, key)
	return ok && now.Before(expiresAt)
}

func (w *webAuthenticator) createSession(now time.Time) (string, *webSession, error) {
	plain, err := randomWebToken()
	if err != nil {
		return "", nil, err
	}
	csrf, err := randomWebToken()
	if err != nil {
		return "", nil, err
	}
	session := &webSession{key: webTokenKey(plain), csrfToken: csrf, createdAt: now, lastSeen: now, done: make(chan struct{})}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneSessionsLocked(now)
	if len(w.sessions) >= webSessionLimit {
		w.removeOldestSessionLocked()
	}
	w.sessions[session.key] = session
	return plain, session, nil
}

func (w *webAuthenticator) authenticate(plain string, now time.Time) *webSession {
	if !validWebToken(plain) {
		return nil
	}
	key := webTokenKey(plain)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneSessionsLocked(now)
	session := w.sessions[key]
	if session == nil {
		return nil
	}
	session.lastSeen = now
	return session
}

func (w *webAuthenticator) sessionDeadline(session *webSession) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return sessionDeadline(session)
}

func (w *webAuthenticator) sessionStillActive(session *webSession, now time.Time) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	current := w.sessions[session.key]
	if current != session || !now.Before(sessionDeadline(session)) {
		if current == session {
			w.removeSessionLocked(session)
		}
		return time.Time{}, false
	}
	return sessionDeadline(session), true
}

func sessionDeadline(session *webSession) time.Time {
	absolute := session.createdAt.Add(webSessionAbsoluteTimeout)
	idle := session.lastSeen.Add(webSessionIdleTimeout)
	if idle.Before(absolute) {
		return idle
	}
	return absolute
}

func (w *webAuthenticator) revokeSession(session *webSession) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sessions[session.key] == session {
		w.removeSessionLocked(session)
	}
}

func (w *webAuthenticator) shutdown() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, session := range w.sessions {
		close(session.done)
	}
	clear(w.sessions)
	clear(w.loginTokens)
	clear(w.ipFailures)
	w.globalFailure = nil
}

func (w *webAuthenticator) beginPasswordCheck(client string, now time.Time) bool {
	w.mu.Lock()
	w.pruneLoginFailuresLocked(now)
	allowed := len(w.globalFailure) < webLoginFailuresGlobal && len(w.ipFailures[client]) < webLoginFailuresPerIP
	if _, exists := w.ipFailures[client]; !exists && len(w.ipFailures) >= webLoginIPLimit {
		allowed = false
	}
	w.mu.Unlock()
	if !allowed {
		return false
	}
	select {
	case w.passwordSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (w *webAuthenticator) endPasswordCheck() { <-w.passwordSlots }

func (w *webAuthenticator) recordLoginFailure(client string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLoginFailuresLocked(now)
	w.ipFailures[client] = append(w.ipFailures[client], now)
	w.globalFailure = append(w.globalFailure, now)
}

func (w *webAuthenticator) recordLoginSuccess(client string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLoginFailuresLocked(now)
	delete(w.ipFailures, client)
}

func (w *webAuthenticator) pruneLoginFailuresLocked(now time.Time) {
	cutoff := now.Add(-webLoginWindow)
	w.globalFailure = recentWebFailures(w.globalFailure, cutoff)
	for client, failures := range w.ipFailures {
		failures = recentWebFailures(failures, cutoff)
		if len(failures) == 0 {
			delete(w.ipFailures, client)
		} else {
			w.ipFailures[client] = failures
		}
	}
}

func recentWebFailures(values []time.Time, cutoff time.Time) []time.Time {
	index := sort.Search(len(values), func(index int) bool { return values[index].After(cutoff) })
	return values[index:]
}

func (w *webAuthenticator) pruneSessionsLocked(now time.Time) {
	for _, session := range w.sessions {
		if !now.Before(sessionDeadline(session)) {
			w.removeSessionLocked(session)
		}
	}
}

func (w *webAuthenticator) removeOldestSessionLocked() {
	var oldest *webSession
	for _, session := range w.sessions {
		if oldest == nil || session.createdAt.Before(oldest.createdAt) {
			oldest = session
		}
	}
	if oldest != nil {
		w.removeSessionLocked(oldest)
	}
}

func (w *webAuthenticator) removeSessionLocked(session *webSession) {
	delete(w.sessions, session.key)
	select {
	case <-session.done:
	default:
		close(session.done)
	}
}

func (w *webAuthenticator) pruneLoginTokensLocked(now time.Time) {
	for key, expiresAt := range w.loginTokens {
		if !now.Before(expiresAt) {
			delete(w.loginTokens, key)
		}
	}
}

func (w *webAuthenticator) removeOldestLoginTokenLocked() {
	oldestKey := ""
	var oldest time.Time
	for key, expiresAt := range w.loginTokens {
		if oldestKey == "" || expiresAt.Before(oldest) {
			oldestKey, oldest = key, expiresAt
		}
	}
	delete(w.loginTokens, oldestKey)
}

func (w *webAuthenticator) sessionCookieName() string {
	if w.secureCookie {
		return "__Host-404-probe-session"
	}
	return "404-probe-dev-session"
}

func (w *webAuthenticator) loginCookieName() string {
	if w.secureCookie {
		return "__Host-404-probe-login"
	}
	return "404-probe-dev-login"
}

func (w *webAuthenticator) setSessionCookie(writer http.ResponseWriter, value string, now time.Time) {
	http.SetCookie(writer, &http.Cookie{Name: w.sessionCookieName(), Value: value, Path: "/", Secure: w.secureCookie,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(webSessionAbsoluteTimeout.Seconds()), Expires: now.Add(webSessionAbsoluteTimeout)})
}

func (w *webAuthenticator) clearSessionCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{Name: w.sessionCookieName(), Path: "/", Secure: w.secureCookie,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (w *webAuthenticator) setLoginCookie(writer http.ResponseWriter, value string, now time.Time) {
	http.SetCookie(writer, &http.Cookie{Name: w.loginCookieName(), Value: value, Path: "/", Secure: w.secureCookie,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(webLoginTokenTTL.Seconds()), Expires: now.Add(webLoginTokenTTL)})
}

func (w *webAuthenticator) clearLoginCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{Name: w.loginCookieName(), Path: "/", Secure: w.secureCookie,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func randomWebToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validWebToken(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func webTokenKey(value string) string {
	hash := sha256.Sum256([]byte(value))
	return string(hash[:])
}

func setWebNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
