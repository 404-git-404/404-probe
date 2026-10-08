package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func p2aWebWrite(t *testing.T, app *App, method, path, body string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	session := addTestWebSession(t, app, r)
	r.Header.Set("Origin", "https://probe.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	if csrf {
		r.Header.Set("X-CSRF-Token", session.csrfToken)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	return w
}

func TestP2aWebAgentNameUTF8BoundaryAndOfflineMutationProof(t *testing.T) {
	app, store, id := newWebAuthenticationTestApp(t)
	defer store.Close()
	path := "/api/v1/web/agents/" + id + "/name"
	for _, name := range []string{"", strings.Repeat("中", 34), "bad\nname", "bad\x00name"} {
		body, _ := json.Marshal(map[string]string{"name": name})
		if w := p2aWebWrite(t, app, http.MethodPut, path, string(body), true); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid name status=%d body=%s", w.Code, w.Body.String())
		}
	}
	name := strings.Repeat("中", 33) + "a"
	body, _ := json.Marshal(map[string]string{"name": name})
	if w := p2aWebWrite(t, app, http.MethodPut, path, string(body), false); w.Code != http.StatusForbidden {
		t.Fatalf("CSRF status=%d", w.Code)
	}
	w := p2aWebWrite(t, app, http.MethodPut, path, string(body), true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), name) {
		t.Fatalf("offline rename status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestP2aWebTrafficResetFreshSampleAndOriginalReceipt(t *testing.T) {
	app, store, id, _ := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	ctx := context.Background()
	base := app.now()
	path := "/api/v1/web/agents/" + id + "/traffic/reset"
	body := `{"request_id":"dddddddddddddddddddddddddddddddd"}`
	if w := p2aWebWrite(t, app, http.MethodPost, path, body, true); w.Code != http.StatusConflict {
		t.Fatalf("missing sample status=%d", w.Code)
	}
	report := reportFor(id, 1)
	if _, accepted, _, err := store.ProcessReport(ctx, id, report, base); err != nil || !accepted {
		t.Fatalf("report accepted=%t err=%v", accepted, err)
	}
	if w := p2aWebWrite(t, app, http.MethodPost, path, body, false); w.Code != http.StatusForbidden {
		t.Fatalf("CSRF status=%d", w.Code)
	}
	first := p2aWebWrite(t, app, http.MethodPost, path, body, true)
	if first.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", first.Code, first.Body.String())
	}
	app.now = func() time.Time { return base.Add(time.Second) }
	second := p2aWebWrite(t, app, http.MethodPost, path, `{"request_id":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}`, true)
	if second.Code != http.StatusOK {
		t.Fatalf("later reset status=%d", second.Code)
	}
	replayed := p2aWebWrite(t, app, http.MethodPost, path, body, true)
	if replayed.Code != http.StatusOK || replayed.Body.String() != first.Body.String() {
		t.Fatalf("original receipt changed: %s / %s", first.Body.String(), replayed.Body.String())
	}
	current, err := store.TrafficTotals(ctx, id)
	if err != nil || current.StartedAt == nil || *current.StartedAt != base.Add(time.Second).UnixMilli() {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}
