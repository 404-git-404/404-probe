package server

import (
	"encoding/json"
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
		if a.withUpdaterHealth(record).State.AgentUpdaterHealth != nil {
			t.Fatal("stale health attached to another session")
		}
	}
	legacy := storage.AgentSnapshot{Online: true, State: &state}
	if target := targetForAgent(upgradeTarget{Version: "v1.0.10", Supported: true, RequiredProtocol: 2}, legacy); !target.Supported {
		t.Fatal("legacy Agent behavior changed", target)
	}
}
