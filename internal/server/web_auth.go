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
	"sync"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/storage"
	"404-probe/internal/webdomain"
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
{{if .Expired}}<div class="login-error" role="alert">登录请求已过期，请重新登录。</div>{{end}}
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
	store        *storage.Store

	mu            sync.Mutex
	sessions      map[string]*webSession
	loginTokens   map[string]webLoginToken
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
	origin    string
	policyRev int64
}

type webLoginToken struct {
	expiresAt time.Time
	origin    string
	policyRev int64
}

type webAllowedTarget struct {
	origin    *url.URL
	policyRev int64
}

type webSessionContextKey struct{}
type webTargetContextKey struct{}

type webLoginPageView struct {
	CSRFToken string
	Failed    bool
	Expired   bool
}

func WithWebAuthentication(config WebAuthenticationConfig) Option {
	return func(app *App) error {
		if app.webAuth != nil {
			return errors.New("Web authentication is already configured")
		}
		authenticator, err := newWebAuthenticator(config, app.store)
		if err != nil {
			return err
		}
		app.webAuth = authenticator
		return nil
	}
}

func newWebAuthenticator(config WebAuthenticationConfig, stores ...*storage.Store) (*webAuthenticator, error) {
	if err := auth.ParsePasswordHash(config.PasswordHash); err != nil {
		return nil, errors.New("Web password hash is invalid")
	}
	origin, err := parseWebPublicOrigin(config.PublicOrigin, config.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	var store *storage.Store
	if len(stores) != 0 {
		store = stores[0]
	}
	return &webAuthenticator{
		passwordHash: config.PasswordHash, publicOrigin: origin, secureCookie: origin.Scheme == "https", store: store,
		sessions: make(map[string]*webSession), loginTokens: make(map[string]webLoginToken),
		ipFailures: make(map[string][]time.Time), passwordSlots: make(chan struct{}, 2),
	}, nil
}

func parseWebPublicOrigin(value string, allowInsecure bool) (*url.URL, error) {
	return webdomain.ParsePublicOrigin(value, allowInsecure)
}

func webLoopbackHost(host string) bool {
	return webdomain.LoopbackHost(host)
}

func (a *App) webRoutes(mux *http.ServeMux, static http.Handler) {
	mux.Handle("GET /login", a.requireWebTarget(http.HandlerFunc(a.handleWebLoginPage)))
	mux.Handle("POST /login", a.requireWebTarget(http.HandlerFunc(a.handleWebLogin)))
	mux.Handle("GET /favicon.ico", a.requireWebTarget(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("POST /logout", a.requireWebSession(http.HandlerFunc(a.handleWebLogout), true))
	mux.Handle("GET /api/v1/web/session", a.requireWebSession(http.HandlerFunc(a.handleWebSession), true))
	mux.Handle("GET /api/v1/web/version", a.requireWebSession(http.HandlerFunc(a.handleGetWebVersion), true))
	mux.Handle("POST /api/v1/web/agents", a.requireWebMutation(http.HandlerFunc(a.handleCreateWebAgent)))
	mux.Handle("GET /api/v1/web/agents", a.requireWebSession(http.HandlerFunc(a.handleGetWebAgents), true))
	mux.Handle("/api/v1/web/agents", a.requireWebSession(http.HandlerFunc(a.handleWebAgentCollectionMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/agents/{agent_id}/history", a.requireWebSession(http.HandlerFunc(a.handleGetWebAgentHistory), true))
	mux.Handle("/api/v1/web/agents/{agent_id}/history", a.requireWebSession(http.HandlerFunc(a.handleWebAgentHistoryMethodNotAllowed), true))
	mux.Handle("GET /api/v1/web/agents/{agent_id}/plan", a.requireWebSession(http.HandlerFunc(a.handleGetWebAgentPlan), true))
	mux.Handle("PUT /api/v1/web/agents/{agent_id}/plan", a.requireWebMutation(http.HandlerFunc(a.handlePutWebAgentPlan)))
	mux.Handle("/api/v1/web/agents/{agent_id}/plan", a.requireWebSession(http.HandlerFunc(a.handleWebAgentPlanMethodNotAllowed), true))
	mux.Handle("POST /api/v1/web/agents/{agent_id}/outbounds/switch", a.requireWebMutation(http.HandlerFunc(a.handleWebSelectorSwitch)))
	mux.Handle("/api/v1/web/agents/{agent_id}/outbounds/switch", a.requireWebSession(http.HandlerFunc(a.handleWebSelectorSwitchMethodNotAllowed), true))
	mux.Handle("POST /api/v1/web/agents/{agent_id}/upgrade", a.requireWebMutation(http.HandlerFunc(a.handleCreateWebUpgrade)))
	mux.Handle("GET /api/v1/web/agents/{agent_id}/upgrade", a.requireWebSession(http.HandlerFunc(a.handleGetWebUpgrade), true))
	mux.Handle("POST /api/v1/web/agents/{agent_id}/google-status", a.requireWebMutation(http.HandlerFunc(a.handleCreateWebGoogleStatus)))
	mux.Handle("/api/v1/web/agents/{agent_id}/google-status", a.requireWebSession(http.HandlerFunc(a.handleWebGoogleStatusMethodNotAllowed), true))
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
	mux.Handle("GET /style.css", a.requireWebTarget(static))
	mux.Handle("/", a.requireWebSession(static, false))
}

func (a *App) requireWebTarget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.webAuth == nil {
			next.ServeHTTP(w, r)
			return
		}
		target, err := a.webAuth.allowedTarget(r)
		if err != nil {
			setWebNoStore(w)
			writeJobError(w, http.StatusForbidden, "forbidden", "Web request host is not allowed")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), webTargetContextKey{}, target)))
	})
}

func (w *webAuthenticator) allowedTarget(r *http.Request) (*webAllowedTarget, error) {
	target, err := webdomain.TargetOrigin(w.publicOrigin.Scheme, r.Host)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "http" && !webdomain.LoopbackHost(target.Hostname()) {
		return nil, errors.New("insecure Web targets are allowed only on loopback")
	}
	policy := storage.WebDomainPolicy{Mode: storage.WebDomainModeExact}
	if w.store != nil {
		policy, err = w.store.GetWebDomainPolicy(r.Context())
		if err != nil {
			return nil, err
		}
	}
	if policy.Mode == storage.WebDomainModeExact {
		if target.String() != w.publicOrigin.String() {
			return nil, errors.New("Web request host does not match the exact public origin")
		}
		return &webAllowedTarget{origin: target, policyRev: policy.Revision}, nil
	}
	for _, item := range policy.Suffixes {
		if webdomain.MatchesSuffix(target.Hostname(), item.Suffix) {
			return &webAllowedTarget{origin: target, policyRev: policy.Revision}, nil
		}
	}
	return nil, errors.New("Web request host does not match an allowed domain suffix")
}

func webTargetFromContext(r *http.Request) *webAllowedTarget {
	target, _ := r.Context().Value(webTargetContextKey{}).(*webAllowedTarget)
	return target
}

func (w *webAuthenticator) sessionOriginAllowed(ctx context.Context, session *webSession) bool {
	if session == nil {
		return false
	}
	parsed, err := webdomain.ParseRequestOrigin(session.origin)
	if err != nil || parsed.Scheme != w.publicOrigin.Scheme {
		return false
	}
	request := (&http.Request{Host: parsed.Host}).WithContext(ctx)
	target, err := w.allowedTarget(request)
	return err == nil && target.origin.String() == session.origin && target.policyRev == session.policyRev
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
		target, err := a.webAuth.allowedTarget(r)
		if err != nil {
			writeJobError(w, http.StatusForbidden, "forbidden", "Web request host is not allowed")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), webTargetContextKey{}, target))
		cookie, err := r.Cookie(a.webAuth.sessionCookieName())
		if err != nil {
			a.writeWebUnauthenticated(w, r, api)
			return
		}
		session := a.webAuth.authenticate(cookie.Value, a.now())
		if session == nil || session.origin != target.origin.String() || session.policyRev != target.policyRev {
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
	target := webTargetFromContext(r)
	if target == nil {
		writeJobError(w, http.StatusForbidden, "forbidden", "Web request host is not allowed")
		return
	}
	if cookie, err := r.Cookie(a.webAuth.sessionCookieName()); err == nil {
		if session := a.webAuth.authenticate(cookie.Value, a.now()); session != nil && session.origin == target.origin.String() && session.policyRev == target.policyRev {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		a.webAuth.clearSessionCookie(w)
	}
	a.renderWebLogin(w, r, http.StatusOK, false, false)
}

func (a *App) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	setWebNoStore(w)
	if a.webAuth == nil {
		http.NotFound(w, r)
		return
	}
	target := webTargetFromContext(r)
	if target == nil || !a.webAuth.sameOriginRequest(r) {
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
	if err != nil || !a.webAuth.consumeLoginToken(loginCookie.Value, r.PostForm.Get("csrf_token"), target.origin.String(), target.policyRev, a.now()) {
		a.renderWebLogin(w, r, http.StatusForbidden, false, true)
		return
	}
	client := webClientAddress(r.RemoteAddr)
	if !a.webAuth.beginPasswordCheck(client, a.now()) {
		w.Header().Set("Retry-After", "60")
		a.renderWebLogin(w, r, http.StatusTooManyRequests, true, false)
		return
	}
	valid := auth.VerifyPassword(a.webAuth.passwordHash, []byte(r.PostForm.Get("password")))
	a.webAuth.endPasswordCheck()
	if !valid {
		a.webAuth.recordLoginFailure(client, a.now())
		a.renderWebLogin(w, r, http.StatusUnauthorized, true, false)
		return
	}
	a.webAuth.recordLoginSuccess(client, a.now())
	plain, _, err := a.webAuth.createSessionForTarget(a.now(), target.origin.String(), target.policyRev)
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create session")
		return
	}
	a.webAuth.setSessionCookie(w, plain, a.now())
	a.webAuth.clearLoginCookie(w)
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}

func (a *App) renderWebLogin(w http.ResponseWriter, r *http.Request, status int, failed, expired bool) {
	target := webTargetFromContext(r)
	if target == nil {
		writeJobError(w, http.StatusForbidden, "forbidden", "Web request host is not allowed")
		return
	}
	token, err := a.webAuth.issueLoginToken(target.origin.String(), target.policyRev, a.now())
	if err != nil {
		writeJobError(w, http.StatusInternalServerError, "internal_error", "could not create login request")
		return
	}
	a.webAuth.setLoginCookie(w, token, a.now())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = loginTemplate.Execute(w, webLoginPageView{CSRFToken: token, Failed: failed, Expired: expired})
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
	fetchSites := r.Header.Values("Sec-Fetch-Site")
	if len(fetchSites) > 1 || (len(fetchSites) == 1 && fetchSites[0] != "same-origin") {
		return false
	}
	if len(r.Header.Values("Origin")) != 1 {
		return false
	}
	target := webTargetFromContext(r)
	if target == nil {
		var err error
		target, err = w.allowedTarget(r)
		if err != nil {
			return false
		}
	}
	origin, err := webdomain.ParseRequestOrigin(r.Header.Get("Origin"))
	return err == nil && origin.String() == target.origin.String()
}

func (w *webAuthenticator) issueLoginToken(origin string, policyRev int64, now time.Time) (string, error) {
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
	w.loginTokens[key] = webLoginToken{expiresAt: now.Add(webLoginTokenTTL), origin: origin, policyRev: policyRev}
	return plain, nil
}

func (w *webAuthenticator) consumeLoginToken(cookie, form, origin string, policyRev int64, now time.Time) bool {
	if subtle.ConstantTimeCompare([]byte(cookie), []byte(form)) != 1 || !validWebToken(form) {
		return false
	}
	key := webTokenKey(form)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLoginTokensLocked(now)
	token, ok := w.loginTokens[key]
	delete(w.loginTokens, key)
	return ok && token.origin == origin && token.policyRev == policyRev && now.Before(token.expiresAt)
}

func (w *webAuthenticator) createSession(now time.Time) (string, *webSession, error) {
	policyRev := int64(0)
	if w.store != nil {
		policy, err := w.store.GetWebDomainPolicy(context.Background())
		if err != nil {
			return "", nil, err
		}
		policyRev = policy.Revision
	}
	return w.createSessionForTarget(now, w.publicOrigin.String(), policyRev)
}

func (w *webAuthenticator) createSessionForTarget(now time.Time, origin string, policyRev int64) (string, *webSession, error) {
	plain, err := randomWebToken()
	if err != nil {
		return "", nil, err
	}
	csrf, err := randomWebToken()
	if err != nil {
		return "", nil, err
	}
	session := &webSession{key: webTokenKey(plain), csrfToken: csrf, createdAt: now, lastSeen: now, done: make(chan struct{}), origin: origin, policyRev: policyRev}
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
	for key, token := range w.loginTokens {
		if !now.Before(token.expiresAt) {
			delete(w.loginTokens, key)
		}
	}
}

func (w *webAuthenticator) removeOldestLoginTokenLocked() {
	oldestKey := ""
	var oldest time.Time
	for key, token := range w.loginTokens {
		if oldestKey == "" || token.expiresAt.Before(oldest) {
			oldestKey, oldest = key, token.expiresAt
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
