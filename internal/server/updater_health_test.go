package server

import (
	"encoding/json"
	"strings"
	"testing"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

func TestUpdaterHealthWebGatesAndSessionFencing(t *testing.T) {
	state := storage.State{AgentID: "agent", Epoch: 2, SessionID: "session", Sequence: 5, AgentVersion: "v1.0.9", AgentUpgradeCapable: true, AgentUpgradeV2: true}
	for _, status := range []string{"healthy", "missing", "unreachable", "identity_mismatch", "unsupported"} {
		h := &protocol.UpdaterHealth{Status: status, Reason: "socket_missing"}
		current := state
		current.AgentUpdaterHealth = h
		a := &App{states: map[string]storage.State{"agent": current}}
		record := storage.AgentSnapshot{Agent: storage.Agent{ID: "agent"}, Online: true, State: &state}
		target := a.compatibleAgentTarget(upgradeTarget{Version: "v1.0.10", Supported: true, RequiredProtocol: 2}, record)
		if target.Supported != (status == "healthy") || target.ReasonCode != h.ReasonCode() {
			t.Fatal(status, target)
		}
		view := newWebAgentSummaryView(a.withUpdaterHealth(record))
		if view.UpdaterHealth != h || view.UpgradeCapable != (status == "healthy") {
			t.Fatal(status, view)
		}
		event := newWebAgentEventView(current)
		body, _ := json.Marshal(event)
		var wire map[string]any
		_ = json.Unmarshal(body, &wire)
		if wire["updater_health"] == nil || wire["upgrade_capable"] != (status == "healthy") {
			t.Fatal(string(body))
		}
		newer := state
		newer.Sequence++
		record.State = &newer
		if a.withUpdaterHealth(record).State.AgentUpdaterHealth != nil {
			t.Fatal("stale health attached to newer snapshot")
		}
		newer = state
		newer.SessionID = "other"
		record.State = &newer
		if a.withUpdaterHealth(record).State.AgentUpdaterHealth != nil {
			t.Fatal("stale health attached to another session")
		}
		older := state
		older.Sequence--
		record.State = &older
		if a.withUpdaterHealth(record).State.AgentUpdaterHealth != h {
			t.Fatal("latest accepted health skipped for lagging DB")
		}
	}
	legacy := storage.AgentSnapshot{Online: true, State: &state}
	encoded, _ := json.Marshal(newWebAgentEventView(state))
	var legacyEvent map[string]any
	_ = json.Unmarshal(encoded, &legacyEvent)
	if value, present := legacyEvent["updater_health"]; !present || value != nil {
		t.Fatal("legacy report must clear stale live health", string(encoded))
	}
	if target := targetForAgent(upgradeTarget{Version: "v1.0.10", Supported: true, RequiredProtocol: 2}, legacy); !target.Supported {
		t.Fatal("legacy Agent behavior changed", target)
	}
}

func TestUpdaterHealthUnknownAndLongReasonStillAcceptReport(t *testing.T) {
	app, store, id, token := testApp(t)
	defer store.Close()
	defer app.Shutdown()
	for i, status := range []string{"future", "unreachable", "healthy"} {
		report := reportFor(id, uint64(i+1))
		report.AgentUpdaterHealth = &protocol.UpdaterHealth{Status: status, Reason: strings.Repeat("x", 4096)}
		if response := postReport(t, app, token, report); response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		app.mu.RLock()
		h := app.states[id].AgentUpdaterHealth
		app.mu.RUnlock()
		if h == nil || h.Reason != "" {
			t.Fatal("untrusted reason survived", h)
		}
		if status == "future" && (h.Status != "unknown" || h.ReasonCode() != "updater_unavailable") {
			t.Fatal(h)
		}
	}
}
