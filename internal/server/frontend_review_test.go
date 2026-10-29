package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"404-probe/internal/storage"
)

func TestFrontendReviewStrictJSON(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	for _, path := range []string{"/api/v1/web/groups", "/api/v1/web/expenses/estimate"} {
		for _, body := range []string{
			`{"name":"a","Name":"b","expected_revision":1}`, `{"expected_revision":1,"EXPECTED_REVISION":2}`,
			`{"scope":"all","SCOPE":"filtered"}`, `{"name":"` + strings.Repeat("a", 64<<10) + `"}`,
			`{"a":` + strings.Repeat("[", 9) + `0` + strings.Repeat("]", 9) + `}`,
		} {
			if w := organizationHTTP(t, a, "POST", path, body, true, true); w.Code != 400 {
				t.Fatal(path, w.Code, w.Body.String())
			}
		}
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Host = "probe.test"
		session := addTestWebSession(t, a, r)
		r.Header.Set("Origin", "https://probe.test")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-CSRF-Token", session.csrfToken)
		r.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := organizationHTTP(t, a, "POST", "/api/v1/web/expenses/estimate", `{"scope":"all","agent_ids":null}`, true, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	// GET dashboard has no JSON body contract; malformed body cannot influence it.
	for _, body := range []string{strings.Repeat("x", (64<<10)+1), `[[[[[[[[[[0]]]]]]]]]]`} {
		if w := organizationHTTP(t, a, "GET", "/api/v1/web/dashboard", body, true, false); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
}

func TestFrontendReviewDeleteIfMatch(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	result := organizationHTTPChange(t, a, "POST", "/api/v1/web/groups", map[string]any{"expected_revision": 1, "name": "delete"})
	for _, header := range []string{"*", "W/\"2\"", "2,3", "0", "-1", "02", "\"1\"", "\"2\""} {
		r := httptest.NewRequest("DELETE", "/api/v1/web/groups/"+result.ID, nil)
		r.Host = "probe.test"
		session := addTestWebSession(t, a, r)
		r.Header.Set("Origin", "https://probe.test")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-CSRF-Token", session.csrfToken)
		r.Header.Set("If-Match", header)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		want := 400
		if header == `"1"` {
			want = 409
		}
		if header == `"2"` {
			want = 200
		}
		if w.Code != want {
			t.Fatal(header, w.Code, w.Body.String())
		}
	}
}

func TestFrontendReviewMetadataEventsAndCache(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	a.invalidateDashboardNetwork(id)
	if a.dashboardNetwork == nil || a.dashboardNetwork[id].Generation == 0 {
		t.Fatal("first invalidation lost")
	}
	generation := a.dashboardGeneration
	for i := 0; i < 2050; i++ {
		a.dashboardNetwork[fmt.Sprint(i)] = dashboardNetworkEntry{}
	}
	a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), id, time.Now())
	if a.dashboardGeneration <= generation {
		t.Fatal("cache reset reused generation")
	}
	ch, _ := a.hub.subscribe()
	defer a.hub.unsubscribe(ch)
	for _, action := range []string{"disable", "enable", "revoke"} {
		w := organizationHTTP(t, a, "POST", webAgentPathPrefix+id+"/"+action, `{}`, true, true)
		if w.Code != 200 {
			t.Fatal(action, w.Code, w.Body.String())
		}
		select {
		case event := <-ch:
			var v map[string]any
			json.Unmarshal(event, &v)
			if v["event"] != "dashboard" || v["agent_id"] != id {
				t.Fatal(string(event))
			}
		default:
			t.Fatal("missing committed event", action)
		}
	}
}

func TestFrontendReviewLocalTimezoneInvalid(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	if w := webPlanPut(t, a, id, `{"timezone":"Local","renewal_date":"2026-10-29"}`, true); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if v := renewalSummary(&storage.AgentPlan{Timezone: "Local", RenewalDate: "2026-10-29"}, time.Now()); v.Type != "invalid" {
		t.Fatal(v)
	}
}

func TestFrontendReviewQualityWritesInvalidateAndPublish(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	ch, _ := a.hub.subscribe()
	defer a.hub.unsubscribe(ch)
	for _, row := range []struct{ path, body string }{
		{v4Path(id, "config"), v4Body("0", 30000, "8.8.8.8")},
		{v4Path(id, "enabled"), `{"expected_revision":"1","enabled":true}`},
	} {
		a.dashboardNetwork = map[string]dashboardNetworkEntry{id: {Expires: time.Now().Add(time.Hour)}}
		w := qualityWebRequest(t, a, "PUT", row.path, row.body)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if !a.dashboardNetwork[id].Expires.IsZero() {
			t.Fatal("quality write retained cached result")
		}
		select {
		case msg := <-ch:
			if !strings.Contains(string(msg), `"event":"dashboard"`) {
				t.Fatal(string(msg))
			}
		default:
			t.Fatal("missing quality event")
		}
	}
}
