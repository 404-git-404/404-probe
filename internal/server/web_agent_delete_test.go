package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
)

func TestWebDeleteOfflineAgentLifecycle(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	var log bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&log, nil))
	now := time.Now()
	app.now = func() time.Time { return now }
	base := webAgentPathPrefix + id
	request := func(authenticated, csrf bool, name string, status int, code string) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"name": name})
		if err != nil {
			t.Fatal(err)
		}
		response := organizationHTTP(t, app, http.MethodPost, base+"/delete", string(body), authenticated, csrf)
		if response.Code != status || code != "" && jobErrorCode(t, response) != code {
			t.Fatalf("delete status %d, want %d / %s: %s", response.Code, status, code, response.Body.String())
		}
	}
	request(false, false, "test", 401, "unauthorized")
	request(true, false, "test", 403, "forbidden")
	report := reportFor(id, 1)
	if response := postReport(t, app, token, report); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	request(true, true, "test", 409, "agent_online")
	if _, err := store.DisableAgent(t.Context(), id, now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Second)
	if response := postReport(t, app, token, report); response.Code != 423 {
		t.Fatal(response.Code, response.Body.String())
	}
	w := organizationHTTP(t, app, http.MethodGet, base, "", true, false)
	var detail webAgentDetailView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Online || !detail.DeleteBlockedOnline {
		t.Fatal(w.Code, w.Body.String())
	}
	request(true, true, "test", 409, "agent_online")
	now = now.Add(61 * time.Second)
	request(true, true, "test", 409, "agent_online")
	now = now.Add(30 * time.Second)
	request(true, true, "test ", 409, "agent_name_mismatch")

	other, _ := auth.NewID()
	if err := store.AddAgent(t.Context(), other, "revoked", auth.Hash("revoked"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeAgent(t.Context(), other, now); err != nil {
		t.Fatal(err)
	}
	w = organizationHTTP(t, app, http.MethodPost, webAgentPathPrefix+other+"/delete", `{"name":"revoked"}`, true, true)
	if w.Code != 409 || jobErrorCode(t, w) != "agent_revoked" {
		t.Fatal(w.Code, w.Body.String())
	}
	events, ok := app.hub.subscribe()
	if !ok {
		t.Fatal("subscribe failed")
	}
	defer app.hub.unsubscribe(events)
	app.deviceScores.Store(&deviceScoreSnapshot{Items: map[string]deviceScore{id: {}, other: {}}})
	request(true, true, "test", 204, "")
	if scores := app.deviceScores.Load(); len(scores.Items) != 1 {
		t.Fatal("deleted Agent remained in score snapshot", scores)
	} else if _, exists := scores.Items[other]; !exists {
		t.Fatal("unrelated score was removed")
	}
	select {
	case payload := <-events:
		var event struct {
			Event   string `json:"event"`
			AgentID string `json:"agent_id"`
			Kind    string `json:"kind"`
		}
		if json.Unmarshal(payload, &event) != nil || event.Event != "dashboard" || event.AgentID != id || event.Kind != "delete" {
			t.Fatalf("delete event: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("missing delete event")
	}
	if w := organizationHTTP(t, app, http.MethodGet, base, "", true, false); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := postReport(t, app, token, report); w.Code != 401 || jobErrorCode(t, w) != "agent_revoked" {
		t.Fatal(w.Code, w.Body.String())
	}
	if !strings.Contains(log.String(), "rejected credential for permanently deleted Agent") || strings.Contains(log.String(), token) {
		t.Fatal("deleted credential rejection was not recorded safely")
	}
}
