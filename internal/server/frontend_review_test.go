package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
	"404-probe/internal/storage"
)

func TestFrontendReviewStrictJSON(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	for _, path := range []string{"/api/v1/web/groups", "/api/v1/web/expenses/estimate"} {
		for _, body := range []string{
			`{"scope":"all","\u0073cope":"filtered"}`, `{"scope":"all","ſcope":"filtered"}`,
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
	for _, body := range []string{`{"key":1,"Key":2}`, `{"scope":1,"ſcope":2}`, `{"name":1,"\u006eame":2}`} {
		if err := uniqueOrganizationJSON(json.NewDecoder(strings.NewReader(body)), 0); err == nil {
			t.Fatal("duplicate simple-fold key accepted", body)
		}
	}
	// Valid short fields plus whitespace isolate the byte limit from name limits.
	valid := `{"scope":"all"}`
	if w := organizationHTTP(t, a, "POST", "/api/v1/web/expenses/estimate", valid+strings.Repeat(" ", (64<<10)-len(valid)), true, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := organizationHTTP(t, a, "POST", "/api/v1/web/expenses/estimate", valid+strings.Repeat(" ", (64<<10)+1-len(valid)), true, true); w.Code != 400 {
		t.Fatal("oversize body accepted", w.Code)
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

func TestDashboardReviewDirectoryAndGzip(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	group := organizationHTTPChange(t, a, "POST", "/api/v1/web/groups", map[string]any{"expected_revision": 1, "name": "Directory"})
	organizationHTTPChange(t, a, "PUT", webAgentPathPrefix+id+"/organization", map[string]any{"expected_revision": group.Revision, "group_id": group.ID, "tag_ids": []string{}, "new_tags": []string{"Tag"}})
	if _, err := s.PutAgentPlan(t.Context(), storage.AgentPlan{AgentID: id, Currency: "EUR", RenewalPrice: "12", RenewalPricePeriod: "monthly"}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	w := organizationHTTP(t, a, "GET", "/api/v1/web/organization/catalog", "", true, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"currency":"EUR"`) || strings.Contains(w.Body.String(), `"state"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, encoding := range []string{"gzip", "gzip;q=0", "br, gzip;q=0.5"} {
		r := httptest.NewRequest("GET", "/api/v1/web/dashboard", nil)
		r.Host = "probe.test"
		addTestWebSession(t, a, r)
		r.Header.Set("Accept-Encoding", encoding)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		body := w.Body.Bytes()
		if encoding != "gzip;q=0" {
			if w.Header().Get("Content-Encoding") != "gzip" {
				t.Fatal(w.Header())
			}
			reader, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			body, err = io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else if w.Header().Get("Content-Encoding") != "" {
			t.Fatal("ignored gzip q=0")
		}
		var data dashboardResponse
		if w.Code != 200 || json.Unmarshal(body, &data) != nil || len(data.Items) != 1 {
			t.Fatal(w.Code, string(body))
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
	reset := a.dashboardResets
	for i := 0; i < 2050; i++ {
		a.dashboardNetwork[fmt.Sprint(i)] = dashboardNetworkEntry{}
	}
	a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), id, time.Now())
	if a.dashboardResets <= reset {
		t.Fatal("cache reset reused reset epoch")
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

func TestDashboardNetworkUnrelatedInvalidationRemainsKnown(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	var readers sync.WaitGroup
	var unknown atomic.Int64
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				a.invalidateDashboardNetwork("unrelated")
			}
		}
	}()
	for worker := 0; worker < 8; worker++ {
		readers.Go(func() {
			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("reader-%d-%d", worker, i)
				v := a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), id, time.Now())
				if !v.Known {
					unknown.Add(1)
				}
			}
		})
	}
	readers.Wait()
	close(done)
	<-stopped
	if n := unknown.Load(); n != 0 {
		t.Fatalf("unrelated invalidation made %d of 1600 reads unknown", n)
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

func TestFrontendReviewActiveUpgradeDisplayKeepsSupportDecision(t *testing.T) {
	a, s, id := newWebAuthenticationTestApp(t)
	defer s.Close()
	defer a.Shutdown()
	a.buildInfo = buildinfo.Info{Version: "v1.0.10", Commit: strings.Repeat("b", 40)}
	makeUpgradeableAgent(t, a, s, id)
	read := func() upgradeTarget {
		w := webAgentResponse(t, a, "GET", webAgentPathPrefix+id+"/upgrade-targets")
		var v struct {
			Targets []upgradeTarget `json:"targets"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Targets) != 1 {
			t.Fatal(w.Code, w.Body.String())
		}
		return v.Targets[0]
	}
	before := read()
	if _, err := s.CreateUpgrade(t.Context(), storage.UpgradeOperation{OperationID: strings.Repeat("a", 32), AgentID: id, FromVersion: "v1.0.9", TargetVersion: "v1.0.10", CreatedAt: a.now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.ReasonCode != "upgrade_in_progress" || after.Supported != before.Supported {
		t.Fatal(before, after)
	}
}
