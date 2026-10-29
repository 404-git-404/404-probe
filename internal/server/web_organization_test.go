package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"404-probe/internal/storage"
)

func organizationHTTP(t *testing.T, a *App, method, path, body string, authenticated, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "probe.test"
	r.Header.Set("Content-Type", "application/json")
	if authenticated {
		s := addTestWebSession(t, a, r)
		if csrf {
			r.Header.Set("Origin", "https://probe.test")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("X-CSRF-Token", s.csrfToken)
		}
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func organizationHTTPChange(t *testing.T, a *App, method, path string, body any) storage.OrganizationChangeResult {
	t.Helper()
	encoded, _ := json.Marshal(body)
	w := organizationHTTP(t, a, method, path, string(encoded), true, true)
	if w.Code != 200 && w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out storage.OrganizationChangeResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOrganizationAPIAuthenticationCSRFAndStrictInputs(t *testing.T) {
	a, s, _, token := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	for _, path := range []string{"/api/v1/web/groups", "/api/v1/web/tags", "/api/v1/web/exchange-rates"} {
		if w := organizationHTTP(t, a, "GET", path, "", false, false); w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/web/groups", strings.NewReader(`{"expected_revision":1,"name":"forbidden"}`))
	r.Host = "probe.test"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("Agent bearer crossed Web boundary", w.Code)
	}
	if w := organizationHTTP(t, a, "POST", "/api/v1/web/groups", `{"expected_revision":1,"name":"forbidden"}`, true, false); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"expected_revision":1,"name":""}`, `{"expected_revision":1,"name":"x","unexpected":1}`, `{"expected_revision":1,"expected_revision":2,"name":"x"}`, `{"expected_revision":1.5,"name":"x"}`, `{"expected_revision":1,"name":"x"} {}`} {
		if w := organizationHTTP(t, a, "POST", "/api/v1/web/groups", body, true, true); w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	if w := organizationHTTP(t, a, "POST", "/api/v1/web/groups?x=1", `{"expected_revision":1,"name":"x"}`, true, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	created := organizationHTTPChange(t, a, "POST", "/api/v1/web/groups", map[string]any{"expected_revision": 1, "name": "Main"})
	if created.Revision != 2 || len(created.ID) != 32 {
		t.Fatal(created)
	}
	if w := organizationHTTP(t, a, "PATCH", "/api/v1/web/groups/"+created.ID, `{"expected_revision":1,"name":"stale"}`, true, true); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := organizationHTTP(t, a, "DELETE", "/api/v1/web/groups/"+strings.Repeat("f", 32), `{"expected_revision":2}`, true, true); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestOrganizationAPIManageMembershipRatesAndEvents(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	events, _ := a.hub.subscribe()
	defer a.hub.unsubscribe(events)
	group := organizationHTTPChange(t, a, "POST", "/api/v1/web/groups", map[string]any{"expected_revision": 1, "name": "Main"})
	var event map[string]any
	json.Unmarshal(<-events, &event)
	if event["event"] != "organization" || event["revision"] != float64(2) {
		t.Fatal(event)
	}
	tag := organizationHTTPChange(t, a, "POST", "/api/v1/web/tags", map[string]any{"expected_revision": group.Revision, "name": "Tag"})
	<-events
	assigned := organizationHTTPChange(t, a, "PUT", "/api/v1/web/agents/"+id+"/organization", map[string]any{"expected_revision": tag.Revision, "group_id": group.ID, "tag_ids": []string{tag.ID}})
	<-events
	if len(assigned.ChangedAgentIDs) != 1 || assigned.ChangedAgentIDs[0] != id {
		t.Fatal(assigned)
	}
	w := organizationHTTP(t, a, "GET", "/api/v1/web/agents/"+id+"/organization", "", true, false)
	var membership struct {
		Revision int64                     `json:"revision"`
		Item     storage.AgentOrganization `json:"item"`
	}
	if json.Unmarshal(w.Body.Bytes(), &membership) != nil || w.Code != 200 || membership.Item.GroupID == nil || *membership.Item.GroupID != group.ID || len(membership.Item.TagIDs) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	changed := organizationHTTPChange(t, a, "PATCH", "/api/v1/web/tags/"+tag.ID, map[string]any{"expected_revision": assigned.Revision, "name": "Renamed"})
	<-events
	deleted := organizationHTTPChange(t, a, "DELETE", "/api/v1/web/groups/"+group.ID, map[string]any{"expected_revision": changed.Revision})
	<-events
	organizationHTTPChange(t, a, "DELETE", "/api/v1/web/tags/"+tag.ID, map[string]any{"expected_revision": deleted.Revision})
	<-events
	c, _ := s.OrganizationCatalog(t.Context())
	if v := c.Assignments[id]; v.GroupID != nil || len(v.TagIDs) != 0 {
		t.Fatal(v)
	}
	rate := organizationHTTPChange(t, a, "PUT", "/api/v1/web/exchange-rates", map[string]any{"expected_revision": c.Revision, "rates": []map[string]string{{"currency": "CNY", "units_per_usd": "7.10"}}})
	<-events
	for _, currency := range []string{"USD", "ZZZ"} {
		body := fmt.Sprintf(`{"expected_revision":%d,"rates":[{"currency":%q,"units_per_usd":"1"}]}`, rate.Revision, currency)
		if w := organizationHTTP(t, a, "PUT", "/api/v1/web/exchange-rates", body, true, true); w.Code != 400 {
			t.Fatal(currency, w.Code)
		}
	}
	organizationHTTPChange(t, a, "DELETE", "/api/v1/web/exchange-rates/CNY", map[string]any{"expected_revision": rate.Revision})
	<-events
	if w := organizationHTTP(t, a, "GET", "/api/v1/web/exchange-rates", "", true, false); w.Code != 200 || strings.Contains(w.Body.String(), "CNY") {
		t.Fatal(w.Code, w.Body.String())
	}
	// A malformed composite write must not leave its inline new group behind.
	c, _ = s.OrganizationCatalog(t.Context())
	body := fmt.Sprintf(`{"expected_revision":%d,"new_group":"rollback","tag_ids":[%q]}`, c.Revision, strings.Repeat("f", 32))
	if w := organizationHTTP(t, a, "PUT", "/api/v1/web/agents/"+id+"/organization", body, true, true); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := s.OrganizationCatalog(t.Context())
	if after.Revision != c.Revision || len(after.Groups) != 0 {
		t.Fatal("partial inline commit", after)
	}
}

func TestOrganizationAPIConcurrentCASAndRevokedDevice(t *testing.T) {
	a, s, id, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			codes <- organizationHTTP(t, a, "POST", "/api/v1/web/tags", fmt.Sprintf(`{"expected_revision":1,"name":%q}`, name), true, true).Code
		}(name)
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for code := range codes {
		got[code]++
	}
	if got[201] != 1 || got[409] != 1 {
		t.Fatal(got)
	}
	s.RevokeAgent(t.Context(), id, a.now())
	c, _ := s.OrganizationCatalog(t.Context())
	body := fmt.Sprintf(`{"expected_revision":%d,"group_id":null,"tag_ids":[]}`, c.Revision)
	if w := organizationHTTP(t, a, "PUT", "/api/v1/web/agents/"+id+"/organization", body, true, true); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}
