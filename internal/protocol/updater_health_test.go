package protocol

import (
	"encoding/json"
	"testing"
)

func TestUpdaterHealthAdditiveMappingAndUnknownFallback(t *testing.T) {
	for _, row := range []struct{ status, reason, code string }{
		{"healthy", "", ""}, {"missing", "socket_missing", "updater_socket_missing"}, {"missing", "runtime_directory_missing", "updater_directory_missing"},
		{"unreachable", "connect_failed", "updater_unreachable"}, {"identity_mismatch", "socket_replaced", "updater_identity_mismatch"}, {"unsupported", "system_unsupported", "updater_unsupported"}, {"future", "", "updater_unavailable"},
	} {
		h := &UpdaterHealth{Status: row.status, Reason: row.reason}
		if h.ReasonCode() != row.code || h.Valid() != (row.status != "future") {
			t.Fatal(row, h)
		}
	}
	var legacy Report
	if err := json.Unmarshal([]byte(`{"agent_version":"v1.0.9","agent_upgrade_capable":true}`), &legacy); err != nil || legacy.AgentUpdaterHealth != nil {
		t.Fatal(err, legacy)
	}
	if (*UpdaterHealth)(nil).ReasonCode() != "" {
		t.Fatal("legacy health changed")
	}
}
