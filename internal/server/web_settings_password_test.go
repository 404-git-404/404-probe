package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"404-probe/internal/auth"
	"404-probe/internal/storage"
)

const passwordSettingsPath = "/api/v1/web/settings/password"
const webSessionPath = "/api/v1/web/session"

func passwordTestSession(t *testing.T, a *App) (*http.Cookie, *webSession) {
	t.Helper()
	plain, session, err := a.webAuth.createSession(a.now())
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: a.webAuth.sessionCookieName(), Value: plain}, session
}

func passwordChange(t *testing.T, a *App, cookie *http.Cookie, csrf, old, new, confirmation string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"current_password": old, "new_password": new, "confirm_password": confirmation})
	if err != nil {
		t.Fatal(err)
	}
	return passwordChangeRaw(a, cookie, csrf, body, "")
}

func passwordChangeRaw(a *App, cookie *http.Cookie, csrf string, body []byte, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, passwordSettingsPath, bytes.NewReader(body))
	r.Host = a.webAuth.publicOrigin.Host
	r.Header.Set("Origin", a.webAuth.publicOrigin.String())
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func expectPasswordError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) webPasswordResponseError {
	t.Helper()
	var got webPasswordErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || response.Code != status || got.Error.Code != code {
		t.Fatalf("status=%d code=%q decode=%v, want %d %q", response.Code, got.Error.Code, err, status, code)
	}
	return got.Error
}

func TestWebPasswordChangeAttemptsArePerSession(t *testing.T) {
	a, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	first, session := passwordTestSession(t, a)
	other, otherSession := passwordTestSession(t, a)
	wrong := func(cookie *http.Cookie, s *webSession) *httptest.ResponseRecorder {
		return passwordChange(t, a, cookie, s.csrfToken, "wrong password", "a new password here", "a new password here")
	}
	for range webPasswordCheckSlots {
		if !a.webAuth.beginPasswordCheck() {
			t.Fatal("hashing slot unavailable")
		}
	}
	busy := wrong(first, session)
	expectPasswordError(t, busy, http.StatusTooManyRequests, "busy")
	if busy.Header().Get("Retry-After") != "1" {
		t.Fatal("busy response lacks Retry-After")
	}
	for range webPasswordCheckSlots {
		a.webAuth.endPasswordCheck()
	}
	for remaining := 4; remaining > 0; remaining-- {
		if got := expectPasswordError(t, wrong(first, session), http.StatusBadRequest, "wrong_current"); got.Remaining != remaining {
			t.Fatalf("remaining=%d, want %d", got.Remaining, remaining)
		}
	}
	if got := expectPasswordError(t, wrong(other, otherSession), http.StatusBadRequest, "wrong_current"); got.Remaining != 4 {
		t.Fatalf("separate session remaining=%d", got.Remaining)
	}
	expectPasswordError(t, wrong(first, session), http.StatusUnauthorized, "locked_out")
	select {
	case <-session.done:
	default:
		t.Fatal("locked session SSE was not cancelled")
	}
	for _, check := range []struct {
		cookie *http.Cookie
		status int
	}{{first, http.StatusUnauthorized}, {other, http.StatusOK}} {
		if got := webRequest(a, http.MethodGet, webSessionPath, nil, check.cookie).Code; got != check.status {
			t.Fatalf("session status=%d want=%d", got, check.status)
		}
	}
}

func TestWebPasswordChangeValidation(t *testing.T) {
	a, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	const old = "original password with length"
	hash, err := auth.HashPassword([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdoptWebCredential(context.Background(), hash, "test-file-digest", a.now()); err != nil {
		t.Fatal(err)
	}
	cookie, session := passwordTestSession(t, a)
	for _, tc := range []struct{ code, new, confirmation string }{
		{"mismatch", "several words here", "different words here"},
		{"too_short", strings.Repeat("密", 11), strings.Repeat("密", 11)},
		{"too_long", strings.Repeat("a", 1025), strings.Repeat("a", 1025)},
		{"same_as_current", old, old},
		{"invalid_chars", "a new password\u0085here", "a new password\u0085here"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			got := expectPasswordError(t, passwordChange(t, a, cookie, session.csrfToken, old, tc.new, tc.confirmation), http.StatusBadRequest, tc.code)
			if tc.code == "too_short" && (got.Characters == nil || *got.Characters != 11) {
				t.Fatal("missing Unicode code point count")
			}
		})
	}
	for _, malformed := range []struct{ code, body string }{
		{"invalid_request", `{"extra":true}`},
		{"invalid_request", `{"current_password":"a","new_password":"b","confirm_password":"c"}{}`},
		{"invalid_request", `{"current_password":"` + strings.Repeat("a", 4096) + `"}`},
		{"invalid_chars", `{"current_password":"a","new_password":"abc\udc00","confirm_password":"abc"}`},
		{"invalid_chars", `{"current_password":"a","new_password":"abc` + "\xff" + `","confirm_password":"abc"}`},
	} {
		expectPasswordError(t, passwordChangeRaw(a, cookie, session.csrfToken, []byte(malformed.body), ""), http.StatusBadRequest, malformed.code)
	}
}

func TestWebPasswordChangeRotatesAllSessions(t *testing.T) {
	a, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	current, session := passwordTestSession(t, a)
	other, otherSession := passwordTestSession(t, a)
	const path = webSessionPath
	// The new password occupies 1024 bytes and over 2 KiB when form encoded.
	newPassword := " " + strings.Repeat("密", 340) + "   "
	changed := passwordChange(t, a, current, session.csrfToken, "test-password", newPassword, newPassword)
	if changed.Code != http.StatusNoContent {
		t.Fatalf("change status=%d body=%s", changed.Code, changed.Body)
	}
	for _, s := range []*webSession{session, otherSession} {
		select {
		case <-s.done:
		default:
			t.Fatal("previous session SSE was not cancelled immediately")
		}
	}
	cookies := changed.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != a.webAuth.sessionCookieName() || cookies[0].Value != "" || cookies[0].MaxAge >= 0 {
		t.Fatalf("expected only an expired session cookie, got %v", cookies)
	}
	if len(a.webAuth.sessions) != 0 {
		t.Fatal("password change issued or retained a session")
	}
	for _, cookie := range []*http.Cookie{current, other} {
		if view := webRequest(a, http.MethodGet, path, nil, cookie); view.Code != http.StatusUnauthorized {
			t.Fatalf("previous session status=%d", view.Code)
		}
	}
	if got := postWebLogin(t, a, "test-password", a.webAuth.publicOrigin.String()); got.Code != http.StatusUnauthorized {
		t.Fatalf("old password login=%d", got.Code)
	}
	if got := postWebLogin(t, a, newPassword, a.webAuth.publicOrigin.String()); got.Code != http.StatusSeeOther {
		t.Fatalf("new password login=%d", got.Code)
	}
}

func TestWebPasswordAdoptionInvalidatesSessions(t *testing.T) {
	a, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	first, session := passwordTestSession(t, a)
	second, _ := passwordTestSession(t, a)
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/web/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = a.webAuth.publicOrigin.Host
	request.AddCookie(first)
	events, err := server.Client().Do(request)
	if err != nil || events.StatusCode != http.StatusOK {
		t.Fatalf("opening SSE: %v %v", events, err)
	}
	defer events.Body.Close()
	if err := store.AdoptWebCredential(context.Background(), webTestPasswordHash(t), "new-file", a.now()); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []*http.Cookie{first, second} {
		if got := webRequest(a, http.MethodGet, webSessionPath, nil, cookie).Code; got != http.StatusUnauthorized {
			t.Fatalf("outdated session status=%d", got)
		}
		if got := webRequest(a, http.MethodGet, "/login", nil, cookie).Code; got != http.StatusOK {
			t.Fatalf("outdated session login page status=%d", got)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, events.Body); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("idle SSE stayed open after adoption")
	}
	if _, ok := a.webAuth.sessionStillActive(context.Background(), session, a.now()); ok {
		t.Fatal("invalidated session is still active")
	}
}

func TestWebPasswordChangeCASRejectsConcurrentAdoption(t *testing.T) {
	a, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	_, session := passwordTestSession(t, a)
	credential, err := a.webAuth.currentCredential(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, replacement, err := newWebSession(a.now(), session.origin, session.policyRev, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Adoption commits after the handler reads its expected credential and
	// before replacePassword performs the conditional write.
	adoptHash := webTestPasswordHash(t)
	if err := store.AdoptWebCredential(context.Background(), adoptHash, "adopted", a.now()); err != nil {
		t.Fatal(err)
	}
	err = a.webAuth.replacePassword(context.Background(), session, adoptHash, credential.FileSHA256, a.now())
	if !errors.Is(err, storage.ErrWebCredentialConflict) {
		t.Fatalf("concurrent adoption error=%v", err)
	}
	credential, err = store.WebCredential(context.Background())
	if err != nil || credential.Revision != 1 || credential.PasswordHash != adoptHash || credential.FileSHA256 != "adopted" {
		t.Fatal("concurrent adoption was overwritten or a session was issued", err)
	}
	if a.webAuth.sessions[replacement.key] != nil {
		t.Fatal("conflicting change issued a replacement session")
	}
}

func TestWebPasswordChangeRejectsControlTokenAndDatabaseFailure(t *testing.T) {
	a, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	token, controlHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := WithControlTokenHash(controlHash)(a); err != nil {
		t.Fatal(err)
	}
	control := passwordChangeRaw(a, nil, "", []byte(`{}`), token)
	expectPasswordError(t, control, http.StatusUnauthorized, "unauthorized")
	cookie, session := passwordTestSession(t, a)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	res := passwordChange(t, a, cookie, session.csrfToken, "test-password", "fresh password here", "fresh password here")
	expectPasswordError(t, res, http.StatusForbidden, "forbidden")
}

func TestWebPasswordChangeTwelveCodePointBoundary(t *testing.T) {
	for _, value := range []string{strings.Repeat("a", 11), strings.Repeat("a", 12), strings.Repeat("密", 11), strings.Repeat("密", 12)} {
		t.Run(value, func(t *testing.T) {
			a, store, _ := newWebAuthenticationTestApp(t)
			defer store.Close()
			// Existing passwords are allowed to be shorter than the new-password minimum.
			hash, err := auth.HashPassword([]byte("old"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AdoptWebCredential(context.Background(), hash, "fixture-file", a.now()); err != nil {
				t.Fatal(err)
			}
			cookie, session := passwordTestSession(t, a)
			changed := passwordChange(t, a, cookie, session.csrfToken, "old", value, value)
			if len([]rune(value)) == 11 {
				result := expectPasswordError(t, changed, http.StatusBadRequest, "too_short")
				if result.Characters == nil || *result.Characters != 11 {
					t.Fatal("missing code point count")
				}
			} else if changed.Code != http.StatusNoContent {
				t.Fatalf("12 code points rejected: %d %s", changed.Code, changed.Body)
			}
		})
	}
}

func TestWebPasswordFormsHaveNonSubmittingUsername(t *testing.T) {
	a, store, _ := newWebAuthenticationTestApp(t)
	defer store.Close()
	cookie, _ := passwordTestSession(t, a)
	for _, path := range []string{"/login", "/settings.html"} {
		t.Run(path, func(t *testing.T) {
			var credential *http.Cookie
			if path != "/login" {
				credential = cookie
			}
			response := webRequest(a, http.MethodGet, path, nil, credential)
			if response.Code != http.StatusOK {
				t.Fatalf("form status %d", response.Code)
			}
			document, err := html.Parse(strings.NewReader(response.Body.String()))
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			var walk func(*html.Node)
			walk = func(n *html.Node) {
				if n.Type == html.ElementNode && n.Data == "input" {
					attrs := map[string]string{}
					for _, a := range n.Attr {
						attrs[a.Key] = a.Val
					}
					if attrs["autocomplete"] == "username" {
						count++
						_, hidden := attrs["hidden"]
						_, named := attrs["name"]
						if attrs["value"] != "404-probe" || attrs["tabindex"] != "-1" || attrs["aria-hidden"] != "true" || !hidden || named {
							t.Fatal("username must be fixed, hidden, ignored by assistive navigation, and excluded from submission")
						}
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
			walk(document)
			if count != 1 {
				t.Fatalf("username inputs=%d", count)
			}
		})
	}
}
