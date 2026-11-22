package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"404-probe/internal/protocol"
)

func TestStartupDiskFailureAuthenticatesDeduplicatesAndClearsOnReport(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	handler := app.Handler()
	alert := func(credential string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, protocol.StartupDiskFailurePath, nil)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("alert status=%d, want %d", w.Code, want)
		}
	}
	dashboard := func(active, hasState bool) {
		t.Helper()
		w := organizationHTTP(t, app, "GET", "/api/v1/web/dashboard?ids="+id, "", true, false)
		var response dashboardResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Items) != 1 {
			t.Fatalf("dashboard=%d %s", w.Code, w.Body.String())
		}
		row := response.Items[0]
		if row.StartupDiskFailure != active || (row.State != nil) != hasState {
			t.Fatalf("dashboard alert=%t state=%t, want alert=%t state=%t", row.StartupDiskFailure, row.State != nil, active, hasState)
		}
	}

	alert("", http.StatusUnauthorized)
	alert("wrong-credential", http.StatusUnauthorized)
	dashboard(false, false)
	events, ok := app.hub.subscribe()
	if !ok {
		t.Fatal("could not subscribe to dashboard events")
	}
	defer app.hub.unsubscribe(events)

	alert(token, http.StatusNoContent)
	dashboard(true, false)
	select {
	case event := <-events:
		if !bytes.Contains(event, []byte(`"kind":"startup-disk-failure"`)) {
			t.Fatalf("unexpected event %s", event)
		}
	default:
		t.Fatal("missing startup disk dashboard event")
	}
	for range 3 {
		alert(token, http.StatusNoContent)
	}
	select {
	case event := <-events:
		t.Fatalf("duplicate alert published an event: %s", event)
	default:
	}
	report := reportFor(id, 1)
	if w := postReport(t, app, token, report); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	dashboard(false, true)

	alert(token, http.StatusNoContent)
	if w := postReport(t, app, token, report); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	dashboard(true, true)
	report.Sequence++
	if w := postReport(t, app, token, report); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	dashboard(false, true)

	if _, err := store.DisableAgent(t.Context(), id, app.now()); err != nil {
		t.Fatal(err)
	}
	alert(token, http.StatusLocked)
	dashboard(false, true)
}
