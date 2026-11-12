package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

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

	assertViews := func(region, country string) {
		t.Helper()
		detail := webAgentResponse(t, app, http.MethodGet, webAgentPathPrefix+id)
		check(detail, http.StatusOK)
		var item webAgentDetailView
		if err := json.Unmarshal(detail.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if item.GoogleStatus.Supported || item.GoogleStatus.ManualRegion != region || item.CountryCode != country ||
			!slices.Equal(item.YouTubeAdFreeRegions, youtubeAdFreeRegions[:]) {
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
				data.Items[0].CountryCode != country || !slices.Equal(data.YouTubeAdFreeRegions, youtubeAdFreeRegions[:]) {
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
