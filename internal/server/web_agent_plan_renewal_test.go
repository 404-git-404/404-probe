package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/storage"
)

func webRenewalSetup(t *testing.T) (*App, *storage.Store, string) {
	t.Helper()
	a, s, id := newWebAuthenticationTestApp(t)
	t.Cleanup(func() { a.Shutdown(); s.Close() })
	now := time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	if _, err := s.PutAgentPlan(context.Background(), storage.AgentPlan{AgentID: id, PurchaseDate: "2024-01-31", RenewalDate: "2024-01-31", ExpiryDate: "2024-02-02", RenewalPricePeriod: "monthly", RenewalPrice: "9", Currency: "USD", Timezone: "UTC"}, nil, now); err != nil {
		t.Fatal(err)
	}
	return a, s, id
}
func renewalWebRequest(t *testing.T, a *App, method, path, body string, change func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	session := addTestWebSession(t, a, r)
	r.Header.Set("Origin", "https://probe.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-CSRF-Token", session.csrfToken)
	r.Header.Set("Content-Type", "application/json")
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func renewalWebPath(id, action string) string {
	return "/api/v1/web/agents/" + id + "/plan/renewal/" + action
}
func renewalWebJSON(t *testing.T, q any) string {
	t.Helper()
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func renewalWebPreview(t *testing.T, a *App, id, field string) storage.PlanRenewalPreview {
	t.Helper()
	body := "{}"
	if field != "" {
		body = renewalWebJSON(t, map[string]string{"field": field})
	}
	w := renewalWebRequest(t, a, http.MethodPost, renewalWebPath(id, "preview"), body, nil)
	var out storage.PlanRenewalPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Eligible {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return out
}
func renewalWebApplyDTO(p storage.PlanRenewalPreview, id string) storage.PlanRenewalApply {
	return storage.PlanRenewalApply{RequestID: id, ExpectedRevision: p.ExpectedRevision, Field: p.Field, FromDate: p.FromDate, ToDate: p.ToDate, NewOverdue: p.NewOverdue, DueToday: p.DueToday}
}
func renewalWebApply(t *testing.T, a *App, id string, q storage.PlanRenewalApply) storage.PlanRenewalResult {
	t.Helper()
	w := renewalWebRequest(t, a, http.MethodPost, renewalWebPath(id, "apply"), renewalWebJSON(t, q), nil)
	var out storage.PlanRenewalResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return out
}
func TestWebPlanRenewalAuthenticationAndMutationProof(t *testing.T) {
	for _, action := range []string{"preview", "apply", "undo"} {
		t.Run(action, func(t *testing.T) {
			a, s, id := webRenewalSetup(t)
			before, _, _ := s.GetAgentPlan(context.Background(), id)
			for _, v := range []struct {
				name   string
				status int
				change func(*http.Request)
			}{
				{"no session", 401, func(r *http.Request) { r.Header.Del("Cookie") }},
				{"host", 403, func(r *http.Request) { r.Host = "evil.test" }},
				{"origin", 403, func(r *http.Request) { r.Header.Set("Origin", "https://evil.test") }},
				{"duplicate origin", 403, func(r *http.Request) { r.Header.Add("Origin", "https://probe.test") }},
				{"fetch site", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
				{"no csrf", 403, func(r *http.Request) { r.Header.Del("X-CSRF-Token") }},
				{"duplicate csrf", 403, func(r *http.Request) { r.Header.Add("X-CSRF-Token", "extra") }},
			} {
				t.Run(v.name, func(t *testing.T) {
					w := renewalWebRequest(t, a, "POST", renewalWebPath(id, action), "{}", v.change)
					if w.Code != v.status || w.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("%d %s", w.Code, w.Body)
					}
				})
			}
			after, _, _ := s.GetAgentPlan(context.Background(), id)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("auth failure wrote")
			}
		})
	}
}
func TestWebPlanRenewalStrictJSONAndHTTPBoundaries(t *testing.T) {
	a, s, id := webRenewalSetup(t)
	before, _, _ := s.GetAgentPlan(context.Background(), id)
	for _, action := range []string{"preview", "apply", "undo"} {
		for _, body := range []string{"null", "[]", "", `{} {}`, `{"unknown":1}`, `{"request_id":null}`, `{"field":null}`, `{"FIELD":"renewal_date"}`, `{"field":"renewal_date","field":"expiry_date"}`} {
			t.Run(action+body, func(t *testing.T) {
				w := renewalWebRequest(t, a, "POST", renewalWebPath(id, action), body, nil)
				if w.Code != 400 {
					t.Fatalf("%d %s", w.Code, w.Body)
				}
			})
		}
		for _, method := range []string{"GET", "PUT", "DELETE"} {
			w := renewalWebRequest(t, a, method, renewalWebPath(id, action), "{}", nil)
			if w.Code != 405 || w.Header().Get("Allow") != "POST" {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		}
		w := renewalWebRequest(t, a, "POST", renewalWebPath(id, action)+"?x=1", "{}", nil)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
		w = renewalWebRequest(t, a, "POST", renewalWebPath(id, action)+"?", "{}", nil)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
		encoded := renewalWebPath("%"+fmt.Sprintf("%02x", id[0])+id[1:], action)
		w = renewalWebRequest(t, a, "POST", encoded, "{}", nil)
		if w.Code != 400 {
			t.Fatalf("encoded %d %s", w.Code, w.Body)
		}
		w = renewalWebRequest(t, a, "POST", renewalWebPath(id, action), "{}", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") })
		if w.Code != 415 {
			t.Fatal(w.Code)
		}
		w = renewalWebRequest(t, a, "POST", renewalWebPath(id, action), "{}"+strings.Repeat(" ", 2047), nil)
		if w.Code != 413 {
			t.Fatal(w.Code)
		}
	}
	w := renewalWebRequest(t, a, "POST", renewalWebPath(id, "preview"), "{}"+strings.Repeat(" ", 2046), func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") })
	if w.Code != 200 {
		t.Fatalf("exact size %d %s", w.Code, w.Body)
	}
	after, _, _ := s.GetAgentPlan(context.Background(), id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("HTTP rejection changed plan")
	}
}
func TestWebPlanRenewalRequiredConfirmationFlagsAndFields(t *testing.T) {
	a, _, id := webRenewalSetup(t)
	q := renewalWebApplyDTO(renewalWebPreview(t, a, id, ""), "11111111111111111111111111111111")
	var original map[string]any
	if err := json.Unmarshal([]byte(renewalWebJSON(t, q)), &original); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"request_id", "expected_revision", "field", "from_date", "to_date", "new_overdue", "due_today"} {
		for _, null := range []bool{false, true} {
			t.Run(fmt.Sprint(key, null), func(t *testing.T) {
				m := map[string]any{}
				for k, v := range original {
					m[k] = v
				}
				if null {
					m[key] = nil
				} else {
					delete(m, key)
				}
				w := renewalWebRequest(t, a, "POST", renewalWebPath(id, "apply"), renewalWebJSON(t, m), nil)
				if w.Code != 400 {
					t.Fatalf("%d %s", w.Code, w.Body)
				}
			})
		}
	}
	// Both explicit false flags must still be accepted; they are not omissions.
	r := renewalWebApply(t, a, id, q)
	if r.Replayed || r.CurrentPlan.RenewalDate != q.ToDate {
		t.Fatal(r)
	}
}
func TestWebPlanRenewalApplyReplayAndUndoCurrentState(t *testing.T) {
	a, _, id := webRenewalSetup(t)
	q := renewalWebApplyDTO(renewalWebPreview(t, a, id, ""), "11111111111111111111111111111111")
	first := renewalWebApply(t, a, id, q)
	q2 := renewalWebApplyDTO(renewalWebPreview(t, a, id, ""), "22222222222222222222222222222222")
	second := renewalWebApply(t, a, id, q2)
	replay := renewalWebApply(t, a, id, q)
	if !replay.Replayed || replay.Operation != first.Operation || replay.CurrentRevision != second.CurrentRevision || replay.CurrentPlan.RenewalDate != "2024-03-31" {
		t.Fatal(replay)
	}
	u := storage.PlanRenewalUndo{RequestID: "33333333333333333333333333333333", OperationID: q2.RequestID, ExpectedRevision: second.CurrentRevision}
	w := renewalWebRequest(t, a, "POST", renewalWebPath(id, "undo"), renewalWebJSON(t, u), nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var undone storage.PlanRenewalResult
	json.Unmarshal(w.Body.Bytes(), &undone)
	if undone.CurrentPlan.RenewalDate != "2024-02-29" || undone.Undo.Available {
		t.Fatal(undone)
	}
	w = renewalWebRequest(t, a, "POST", renewalWebPath(id, "undo"), renewalWebJSON(t, u), nil)
	var repeat storage.PlanRenewalResult
	json.Unmarshal(w.Body.Bytes(), &repeat)
	if w.Code != 200 || !repeat.Replayed || repeat.Operation != undone.Operation {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	q2.ToDate = "2024-03-30"
	w = renewalWebRequest(t, a, "POST", renewalWebPath(id, "apply"), renewalWebJSON(t, q2), nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "request_id_conflict") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
func TestWebPlanRenewalOldPutClearOfflineAndMissing(t *testing.T) {
	a, s, id := webRenewalSetup(t)
	q := renewalWebApplyDTO(renewalWebPreview(t, a, id, ""), "11111111111111111111111111111111")
	// Use the unmodified legacy HTTP DTO, without a configuration revision.
	body := `{"currency":"USD","renewal_price":"9","renewal_price_period":"monthly","purchase_date":"2024-01-31","renewal_date":"2024-01-31","expiry_date":"2024-02-02","timezone":"UTC"}`
	if w := webPlanPut(t, a, id, body, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w := renewalWebRequest(t, a, "POST", renewalWebPath(id, "apply"), renewalWebJSON(t, q), nil)
	if w.Code != 409 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, err := s.DisableAgent(context.Background(), id, a.now()); err != nil {
		t.Fatal(err)
	}
	q = renewalWebApplyDTO(renewalWebPreview(t, a, id, "expiry_date"), q.RequestID)
	r := renewalWebApply(t, a, id, q)
	if r.CurrentPlan.RenewalDate != "2024-01-31" || r.CurrentPlan.ExpiryDate != "2024-03-02" {
		t.Fatal(r)
	}
	if w := webPlanPut(t, a, id, "{}", true); w.Code != 204 {
		t.Fatal(w.Code)
	}
	w = renewalWebRequest(t, a, "POST", renewalWebPath(id, "preview"), "{}", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reason":"plan_missing"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	missing := strings.Repeat("f", 32)
	w = renewalWebRequest(t, a, "POST", renewalWebPath(missing, "preview"), "{}", nil)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	if _, err := s.RevokeAgent(context.Background(), id, a.now()); err != nil {
		t.Fatal(err)
	}
	w = renewalWebRequest(t, a, "POST", renewalWebPath(id, "preview"), "{}", nil)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestWebPlanRenewalErrorStatusAndCapacity(t *testing.T) {
	a, s, id := webRenewalSetup(t)
	for _, v := range []struct {
		err    error
		status int
		code   string
	}{
		{&storage.PlanRenewalError{Code: "receipt_capacity", RetryAfter: 123}, 429, "receipt_capacity"},
		{&storage.PlanRenewalError{Code: "invalid_timezone"}, 400, "invalid_timezone"},
		{storage.ErrAgentRemovalPending, 409, "removal_pending"}, {storage.ErrAgentNotFound, 404, "agent_not_found"},
	} {
		w := httptest.NewRecorder()
		a.writePlanRenewalError(w, id, v.err)
		if w.Code != v.status || !strings.Contains(w.Body.String(), v.code) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if v.status == 429 && w.Header().Get("Retry-After") != "123" {
			t.Fatal(w.Header())
		}
	}
	for i := 0; i < 63; i++ {
		q := renewalWebApplyDTO(renewalWebPreview(t, a, id, ""), fmt.Sprintf("%032x", i+1))
		renewalWebApply(t, a, id, q)
	}
	q := renewalWebApplyDTO(renewalWebPreview(t, a, id, ""), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	w := renewalWebRequest(t, a, "POST", renewalWebPath(id, "apply"), renewalWebJSON(t, q), nil)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = renewalWebRequest(t, a, "POST", renewalWebPath(id, "preview"), "{}", func(*http.Request) { s.Close() }) // Host policy read fails before storage handler.
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

// A real file-backed inspector is used only by these tests for local fault
// injection/retention fixtures; product Store internals stay private.
func renewalFileApp(t *testing.T) (*App, *storage.Store, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "renewal.db")
	s, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC)
	if err := s.AddAgent(context.Background(), id, "test", hash, now); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(s, time.Minute, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: webTestPasswordHash(t), PublicOrigin: "https://probe.test"}))
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Shutdown(); db.Close(); s.Close() })
	return a, s, db, id
}
func TestWebPlanRenewalCommittedPublishFailureIsStillSuccess(t *testing.T) {
	a, s, db, id := renewalFileApp(t)
	p := storage.AgentPlan{AgentID: id, RenewalDate: "2024-01-31", RenewalPricePeriod: "monthly", TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
	if _, err := s.PutAgentPlan(context.Background(), p, nil, a.now()); err != nil {
		t.Fatal(err)
	}
	// Incomplete accounting bounds make existing detail projection fail, while
	// the independent date-only transaction can still operate without resetting it.
	if _, err := db.Exec(`UPDATE agent_plans SET cycle_end=NULL WHERE agent_id=?`, id); err != nil {
		t.Fatal(err)
	}
	q := renewalWebApplyDTO(renewalWebPreview(t, a, id, ""), "11111111111111111111111111111111")
	if err := a.publishAgentDetail(context.Background(), id); err == nil {
		t.Fatal("fault did not fail publication")
	}
	r := renewalWebApply(t, a, id, q)
	if r.Replayed || r.CurrentPlan.RenewalDate != q.ToDate {
		t.Fatal(r)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM agent_plan_renewal_requests`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("%d %v", count, err)
	}
	replay := renewalWebApply(t, a, id, q)
	if !replay.Replayed || replay.Operation != r.Operation {
		t.Fatal(replay)
	}
}
func TestWebPlanRenewalRemovalPendingAndUncertainFailure(t *testing.T) {
	a, s, db, id := renewalFileApp(t)
	if _, err := s.PutAgentPlan(context.Background(), storage.AgentPlan{AgentID: id, RenewalDate: "2024-01-31", RenewalPricePeriod: "monthly"}, nil, a.now()); err != nil {
		t.Fatal(err)
	}
	q := renewalWebApplyDTO(renewalWebPreview(t, a, id, ""), "11111111111111111111111111111111")
	if _, err := db.Exec(`CREATE TRIGGER renewal_route_fault BEFORE INSERT ON agent_plan_renewal_requests BEGIN SELECT RAISE(ABORT,'receipt fail'); END`); err != nil {
		t.Fatal(err)
	}
	w := renewalWebRequest(t, a, "POST", renewalWebPath(id, "apply"), renewalWebJSON(t, q), nil)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "原请求 ID") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, err := db.Exec(`DROP TRIGGER renewal_route_fault`); err != nil {
		t.Fatal(err)
	}
	r := renewalWebApply(t, a, id, q)
	if r.Replayed {
		t.Fatal("rollback created receipt")
	}
	_, err := db.Exec(`INSERT INTO agent_removal_operations(operation_id,agent_id,status,created_at,updated_at,requested_epoch,requested_session_id,active_epoch,active_session_id) VALUES(?,?,'requested',?,?,1,'session',1,'session')`, strings.Repeat("a", 32), id, a.now().UnixMilli(), a.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	w = renewalWebRequest(t, a, "POST", renewalWebPath(id, "preview"), "{}", nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "removal_pending") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
