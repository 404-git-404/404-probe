package agent

import (
	"slices"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestRootInventoryResend(t *testing.T) {
	now := time.Now()
	s := inventoryReports{supported: true}
	checks := protocol.SystemChecks{
		SecurityBaseline: &protocol.SecurityBaselineReport{Status: "ok", Digest: "one", Snapshot: &protocol.SecuritySnapshot{}},
		SingboxStructure: &protocol.SingboxStructureReport{Status: "ok", Digest: "one", Structure: &protocol.SingboxStructure{}},
	}
	prepare := func(at time.Time, baseline, structure bool) *protocol.SystemChecks {
		t.Helper()
		out := checks
		s.prepare(&out, at)
		if (out.SecurityBaseline.Snapshot != nil) != baseline || (out.SingboxStructure.Structure != nil) != structure ||
			out.SecurityBaseline.Digest != checks.SecurityBaseline.Digest || out.SingboxStructure.Digest != checks.SingboxStructure.Digest {
			t.Fatal(out)
		}
		return &out
	}
	prepare(now, true, true) // No ACK yet.
	s.accepted(prepare(now, true, true), now)
	prepare(now.Add(29*time.Minute), false, false)
	prepare(now.Add(30*time.Minute), true, true)
	checks.SecurityBaseline.Digest = "two"
	s.accepted(prepare(now.Add(time.Minute), true, false), now.Add(time.Minute))
	prepare(now.Add(30*time.Minute), false, true)
	prepare(now.Add(31*time.Minute), true, true)
	unsupported := checks
	inventoryReports{}.prepare(&unsupported, now)
	if unsupported.SecurityBaseline != nil || unsupported.SingboxStructure != nil || checks.SecurityBaseline.Snapshot == nil {
		t.Fatal("incorrect compatibility or mutation of collector sample")
	}
}

func TestOrderSelectorsWithSingboxStructure(t *testing.T) {
	selectors := []protocol.OutboundSelector{{Name: "other", Choices: []string{"m", "a", "b"}}, {Name: "main", Choices: []string{"x", "y"}}, {Name: "unknown"}}
	structure := &protocol.SingboxStructure{Outbounds: []protocol.SingboxOutbound{
		{Tag: "other", Type: "selector", Outbounds: []string{"b", "m"}}, {Tag: "main", Type: "selector", Outbounds: []string{"y", "x"}},
	}}
	if source := orderSelectorsWithStructure(selectors, structure, ""); source != "config" || selectors[0].Name != "other" ||
		!slices.Equal(selectors[0].Choices, []string{"b", "m", "a"}) || selectors[1].Name != "main" ||
		!slices.Equal(selectors[1].Choices, []string{"y", "x"}) || selectors[2].Name != "unknown" {
		t.Fatal(source, selectors)
	}
	if source := orderSelectorsWithStructure(selectors, nil, ""); source != "name" || selectors[0].Name != "main" {
		t.Fatal(source, selectors)
	}
}
