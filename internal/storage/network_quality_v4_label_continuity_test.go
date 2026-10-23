package storage

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// A save that only changes the labels (kind, region, ISP) must not interrupt the measurement:
// same target and revisions, the Agent configuration is byte for byte the same, the real samples
// before and after land in one continuous series, and TCP Mini keeps accumulating (design 10.1, M1).
func TestV4LabelOnlySaveKeepsMeasurementContinuous(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	target := k.targetID(cfg, "telecom", "ipv4", "tcp")
	start := k.now
	step := 30 * time.Second
	var seq int
	feed := func(from, n int) {
		for i := from; i < from+n; i++ {
			at := start.Add(time.Duration(i+1) * step)
			seq++
			sample := k.sample(cfg, "telecom", "ipv4", "tcp", seq, at, "success")
			if acks := k.upload(at.Add(time.Second), sample); acks[0] != "committed" {
				t.Fatalf("sample %d: %v", i, acks)
			}
		}
	}
	agentConfig := func() []byte {
		got, err := k.sessionConfig(2, true)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(got)
		return wire
	}
	feed(0, 40) // 20 minutes
	k.now = start.Add(41 * step)
	beforeConfig := agentConfig()
	settings, measurement := k.revisions()
	slotRevision := k.lane(cfg, "telecom", "ipv4", "tcp").SlotRevision

	renamed := v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp")
	renamed.Kind, renamed.Region, renamed.ISP = "custom", "Another place", "Another ISP"
	k.save(30000, renamed)

	settingsAfter, measurementAfter := k.revisions()
	if settingsAfter != settings+1 || measurementAfter != measurement {
		t.Fatalf("revisions: settings %d->%d measurement %d->%d", settings, settingsAfter, measurement, measurementAfter)
	}
	if !bytes.Equal(beforeConfig, agentConfig()) {
		t.Fatalf("the Agent configuration changed:\n%s\n%s", beforeConfig, agentConfig())
	}
	after := k.get()
	if got := k.targetID(after, "telecom", "ipv4", "tcp"); got != target || k.lane(after, "telecom", "ipv4", "tcp").SlotRevision != slotRevision {
		t.Fatal("target or slot revision changed")
	}
	feed(40, 40) // 20 more minutes, uploaded with the old configuration the Agent still has
	k.now = start.Add(81 * step)

	history, err := k.s.QualityCardHistory(k.ctx, k.id, target, k.now)
	if err != nil {
		t.Fatal(err)
	}
	if history.Target.Region != "Another place" || history.Target.ISP != "Another ISP" {
		t.Fatalf("history labels: %+v", history.Target)
	}
	if len(history.Groups) != 1 || len(history.Groups[0].Points) != 12 {
		t.Fatalf("expected one group of 12 cells: %+v", history.Groups)
	}
	group := history.Groups[0]
	if group.Window.P50 == nil || group.Window.Count == 0 {
		t.Fatalf("p50 over the window is missing: %+v", group.Window)
	}
	// Forty minutes of 30-second samples fill the last eight 5-minute cells without a hole.
	filled := 0
	for i, point := range group.Points {
		if point.Count == 0 {
			if filled > 0 {
				t.Fatalf("cell %d is empty after data started: a gap across the label change", i)
			}
			continue
		}
		filled++
		if point.ConfigMin != point.ConfigMax || point.SlotMin != point.SlotMax || point.ConfigMin != after.ConfigRevision {
			t.Fatalf("cell %d spans revisions: %+v", i, point)
		}
	}
	if filled < 7 {
		t.Fatalf("only %d cells carry data", filled)
	}
}
