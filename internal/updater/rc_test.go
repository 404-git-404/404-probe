package updater

import (
	"os"
	"strings"
	"testing"
)

func TestRCCapabilityAndExplicitRequestBoundary(t *testing.T) {
	for _, action := range []Action{ActionCapabilities, ActionRCCapabilities} {
		r := Request{ProtocolVersion: 2, Action: action}
		if r.Validate() != nil {
			t.Fatal(r)
		}
	}
	if (Request{ProtocolVersion: 1, Action: ActionRCCapabilities}).Validate() == nil {
		t.Fatal("RC negotiation accepted on protocol1")
	}
	r := Request{ProtocolVersion: 2, Action: ActionStart, OperationID: strings.Repeat("a", 32), TargetVersion: "v1.0.3-rc.2", Channel: "beta", TargetCommit: strings.Repeat("b", 40), ServerVersion: "v1.0.3-rc.1"}
	if r.Validate() != nil {
		t.Fatal(r)
	}
	r.Channel = "stable"
	if r.Validate() == nil {
		t.Fatal("RC entered stable wire channel")
	}
}
func TestRCUpdaterCurrentVersionCanAdvanceWithHealth(t *testing.T) {
	e, r, live, _, _ := versionedEngineFixture(t, "v1.0.3-rc.2", 2)
	e.config.CurrentVersion = "v1.0.3-rc.1"
	r.ServerVersion = "v1.0.3-rc.1"
	if _, err := e.Start(r); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, e, r, "health_check")
	healthy := r
	healthy.Action = ActionHealthy
	if _, err := e.Healthy(healthy); err != nil {
		t.Fatal(err)
	}
	waitLocalStatus(t, e, r, "succeeded")
	b, err := os.ReadFile(live)
	if err != nil || string(b) != "new-agent" {
		t.Fatal(string(b), err)
	}
}
