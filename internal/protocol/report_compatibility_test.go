package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReportVersionFieldsRemainWireOptional(t *testing.T) {
	encoded, err := json.Marshal(Report{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "agent_version") || strings.Contains(string(encoded), "agent_upgrade_capable") {
		t.Fatalf("legacy-compatible report exposed optional fields: %s", encoded)
	}
	encoded, err = json.Marshal(Report{AgentVersion: "v0.8.0", AgentUpgradeCapable: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"agent_version":"v0.8.0"`) || !strings.Contains(string(encoded), `"agent_upgrade_capable":true`) {
		t.Fatalf("negotiated report omitted version fields: %s", encoded)
	}
}

func TestLegacyReportResponseDecodesWithoutCapabilities(t *testing.T) {
	var response ReportResponse
	if err := json.Unmarshal([]byte(`{"accepted":true}`), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.Capabilities.AgentVersionReport || response.Capabilities.AgentUpgrade || response.Capabilities.InteractiveControl {
		t.Fatalf("legacy response=%+v", response)
	}
}
