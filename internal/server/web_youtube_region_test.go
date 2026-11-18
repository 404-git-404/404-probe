package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"404-probe/internal/storage"
)

func TestWebYouTubeAdFreeRegionSettingsPublishDashboardRevision(t *testing.T) {
	a, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	const path = "/api/v1/web/settings/youtube-ad-free-regions"
	if got := webRequest(a, http.MethodGet, path, nil, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settings status=%d", got)
	}
	if w := webAgentResponse(t, a, http.MethodGet, path); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"defaults":[`) {
		t.Fatalf("default settings status=%d body=%s", w.Code, w.Body)
	}
	if w := renewalWebRequest(t, a, http.MethodPut, path, `{"regions":["MO"]}`, func(r *http.Request) { r.Header.Del("X-CSRF-Token") }); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", w.Code)
	}
	for _, body := range []string{`{}`, `{"regions":null}`, `{"regions":["EU"]}`, `{"regions":["mo"]}`, `{"regions":[` + strings.Repeat(`"MO",`, 249) + `"MO"]}`, `{"regions":[],"extra":true}`} {
		if w := renewalWebRequest(t, a, http.MethodPut, path, body, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid settings status=%d", w.Code)
		}
	}
	readDashboard := func() dashboardResponse {
		t.Helper()
		w := webAgentResponse(t, a, http.MethodGet, "/api/v1/web/dashboard")
		var out dashboardResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("dashboard status=%d err=%v", w.Code, err)
		}
		return out
	}
	before := readDashboard()
	events, ok := a.hub.subscribe()
	if !ok {
		t.Fatal("could not subscribe to SSE hub")
	}
	defer a.hub.unsubscribe(events)
	if w := renewalWebRequest(t, a, http.MethodPut, path, `{"regions":["US","US","MO"]}`, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"regions":["MO","US"]`) {
		t.Fatalf("save status=%d body=%s", w.Code, w.Body)
	}
	select {
	case event := <-events:
		if !strings.Contains(string(event), `"kind":"youtube-ad-free-regions"`) {
			t.Fatalf("unexpected SSE event %s", event)
		}
	case <-time.After(time.Second):
		t.Fatal("region update did not publish SSE")
	}
	after := readDashboard()
	if after.Revision <= before.Revision || !slices.Equal(after.YouTubeAdFreeRegions, []string{"MO", "US"}) {
		t.Fatalf("outdated catalog revision=%d, regions=%v", after.Revision, after.YouTubeAdFreeRegions)
	}
	if w := renewalWebRequest(t, a, http.MethodPut, path, `{"regions":[]}`, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"regions":[]`) {
		t.Fatalf("clear status=%d body=%s", w.Code, w.Body)
	}
	if w := webAgentResponse(t, a, http.MethodGet, webAgentPathPrefix+id); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"youtube_ad_free_regions":[]`) {
		t.Fatalf("detail status=%d body=%s", w.Code, w.Body)
	}
}

func TestWebYouTubeRegion(t *testing.T) {
	app, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	mutate := func(method, agentID, body string, authenticated, csrf bool, origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, webAgentPathPrefix+agentID+"/youtube-region", strings.NewReader(body))
		request.Host = app.webAuth.publicOrigin.Host
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		if authenticated {
			session := addTestWebSession(t, app, request)
			if csrf {
				request.Header.Set("X-CSRF-Token", session.csrfToken)
			}
		}
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		return response
	}
	check := func(response *httptest.ResponseRecorder, status int) {
		t.Helper()
		if response.Code != status {
			t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body)
		}
	}
	const origin = "https://probe.test"
	const mo = `{"region":"MO"}`
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		body := mo
		if method == http.MethodDelete {
			body = ""
		}
		check(mutate(method, id, body, false, false, origin), http.StatusUnauthorized)
		check(mutate(method, id, body, true, false, origin), http.StatusForbidden)
		check(mutate(method, id, body, true, true, "https://other.test"), http.StatusForbidden)
		check(mutate(method, strings.Repeat("f", 32), body, true, true, origin), http.StatusNotFound)
	}
	for _, body := range []string{`{"region":"mo"}`, `{"region":"ZZ"}`, `{"region":"US "}`, `{"region":"EU"}`, `{"region":"U1"}`, `{"region":""}`, `{"region":1}`, `null`, `{}`, `{"region":"US","extra":true}`, mo + mo} {
		check(mutate(http.MethodPut, id, body, true, true, origin), http.StatusBadRequest)
	}
	check(mutate(http.MethodDelete, id, mo, true, true, origin), http.StatusBadRequest)

	defaults := storage.DefaultYouTubeAdFreeRegions()
	slices.Sort(defaults)
	assertViews := func(region, country string) {
		t.Helper()
		detail := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+id)
		check(detail, http.StatusOK)
		var item webAgentDetailView
		if err := json.Unmarshal(detail.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if item.GoogleStatus.Supported || item.GoogleStatus.ManualRegion != region || item.CountryCode != country ||
			!slices.Equal(item.YouTubeAdFreeRegions, defaults) {
			t.Fatalf("detail=%s", detail.Body)
		}
		for _, suffix := range []string{"", "?ids=" + id} {
			dash := webAgentResponse(t, app, http.MethodGet, "/api/v1/web/dashboard"+suffix)
			check(dash, http.StatusOK)
			var data dashboardResponse
			if err := json.Unmarshal(dash.Body.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
			if len(data.Items) != 1 || data.Items[0].GoogleStatus.ManualRegion != region ||
				data.Items[0].CountryCode != country || !slices.Equal(data.YouTubeAdFreeRegions, defaults) {
				t.Fatalf("dashboard=%s", dash.Body)
			}
		}
	}
	assertViews("", "")
	check(mutate(http.MethodPut, id, mo, true, true, origin), http.StatusOK)
	assertViews("MO", "")
	check(webPlanPut(t, app, id, `{"country_code_override":"JP"}`, true), http.StatusOK)
	assertViews("MO", "JP")
	check(webPlanPut(t, app, id, `{"country_code_override":"JP","currency":"USD","renewal_price":"9","renewal_price_period":"monthly"}`, true), http.StatusOK)
	assertViews("MO", "JP")
	check(mutate(http.MethodPut, id, `{"region":"US"}`, true, true, origin), http.StatusOK)
	assertViews("US", "JP")
	check(mutate(http.MethodDelete, id, "", true, true, origin), http.StatusOK)
	check(mutate(http.MethodDelete, id, "", true, true, origin), http.StatusOK)
	assertViews("", "JP")
	check(mutate(http.MethodPut, id, mo, true, true, origin), http.StatusOK)
	ctx := context.Background()
	if changed, err := store.RevokeAgent(ctx, id, app.now()); err != nil || !changed {
		t.Fatalf("revoke changed=%t err=%v", changed, err)
	}
	if region, err := store.YouTubeManualRegion(ctx, id); err != nil || region != "" {
		t.Fatalf("revoked region=%q err=%v", region, err)
	}
	check(mutate(http.MethodPut, id, mo, true, true, origin), http.StatusNotFound)
}

func TestWebYouTubeRegionMethodsAndContentType(t *testing.T) {
	app, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	request := func(method, contentType, body string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, webAgentPathPrefix+id+"/youtube-region", strings.NewReader(body))
		req.Host = app.webAuth.publicOrigin.Host
		req.Header.Set("Origin", "https://probe.test")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if authenticated {
			session := addTestWebSession(t, app, req)
			req.Header.Set("X-CSRF-Token", session.csrfToken)
		}
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, req)
		return response
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch} {
		if res := request(method, "", "", false); res.Code != http.StatusUnauthorized || res.Header().Get("Allow") != "" {
			t.Errorf("unauthenticated %s: status=%d Allow=%q", method, res.Code, res.Header().Get("Allow"))
		}
		if res := request(method, "", "", true); res.Code != http.StatusMethodNotAllowed || res.Header().Get("Allow") != "PUT, DELETE" {
			t.Errorf("authenticated %s: status=%d Allow=%q", method, res.Code, res.Header().Get("Allow"))
		}
	}
	for _, contentType := range []string{"", "text/plain"} {
		if res := request(http.MethodPut, contentType, `{"region":"MO"}`, true); res.Code != http.StatusUnsupportedMediaType {
			t.Errorf("PUT with Content-Type %q: status=%d body=%s", contentType, res.Code, res.Body)
		}
	}
}
