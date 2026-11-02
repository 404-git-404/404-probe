package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/server/updatecheck"
)

func TestUpdateCheckSettingsSecurity(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	a.updateCheck.Close()
	a.updateCheck = updatecheck.New(filepath.Join(t.TempDir(), "update-check.json"), a.buildInfo, nil)
	for _, tc := range []struct {
		name, body    string
		session, csrf bool
		origin        string
		want          int
	}{
		{"no_session", `{"enabled":false}`, false, false, "https://probe.test", 401},
		{"no_csrf", `{"enabled":false}`, true, false, "https://probe.test", 403},
		{"cross_origin", `{"enabled":false}`, true, true, "https://other.test", 403},
		{"too_large", strings.Repeat(" ", maxWebAgentRenameBytes+1), true, true, "https://probe.test", 413},
		{"missing", "{}", true, true, "https://probe.test", 400},
		{"wrong_type", `{"enabled":"false"}`, true, true, "https://probe.test", 400},
		{"unknown", `{"enabled":false,"url":"https://other.test"}`, true, true, "https://probe.test", 400},
		{"valid", `{"enabled":false}`, true, true, "https://probe.test", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, "/api/v1/web/settings/update-check", strings.NewReader(tc.body))
			r.Host = "probe.test"
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			if tc.session {
				session := addTestWebSession(t, a, r)
				if tc.csrf {
					r.Header.Set("X-CSRF-Token", session.csrfToken)
				}
			}
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if a.updateCheck.Snapshot().Enabled {
		t.Fatal("setting not applied")
	}
	// Starting the lifecycle and repeatedly reading the page must stay passive.
	a.updateCheck.Start(t.Context())
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest("GET", "/api/v1/web/version", nil)
		addTestWebSession(t, a, r)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	time.Sleep(time.Millisecond)
}

func TestVersionUpdateSettingRoundTrip(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	a.updateCheck.Close()
	a.updateCheck = updatecheck.New(filepath.Join(t.TempDir(), "update-check.json"), a.buildInfo, nil)
	for _, enabled := range []bool{false, true} {
		body, _ := json.Marshal(map[string]bool{"enabled": enabled})
		r := httptest.NewRequest("PUT", "/api/v1/web/settings/update-check", strings.NewReader(string(body)))
		session := addTestWebSession(t, a, r)
		r.Header.Set("Origin", "https://probe.test")
		r.Header.Set("X-CSRF-Token", session.csrfToken)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		r = httptest.NewRequest("GET", "/api/v1/web/version", nil)
		addTestWebSession(t, a, r)
		w = httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		var got struct {
			Update updatecheck.State `json:"update"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Update.Enabled != enabled {
			t.Fatal(w.Body.String(), err)
		}
	}
}
