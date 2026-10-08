package storage

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func reportWithNetworkCounters(id string, epoch uint64, session, boot string, sequence uint64, counters ...protocol.NetworkInterfaceCounter) protocol.Report {
	report := validReport(id, epoch, session, boot, sequence, 0, 0)
	set := &protocol.NetworkCounterSet{Version: 1, Interfaces: append(make([]protocol.NetworkInterfaceCounter, 0, len(counters)), counters...)}
	for _, counter := range counters {
		report.RXBytes += counter.RXBytes
		report.TXBytes += counter.TXBytes
	}
	report.NetworkCounters = set
	return report
}

func TestNetworkCounterContinuityTracksInterfacesByName(t *testing.T) {
	store, id, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	base := time.Unix(1000, 0)
	steps := []struct {
		name       string
		report     protocol.Report
		rx, tx     uint64
		partial    bool
		interfaces string
	}{
		{"first baseline", reportWithNetworkCounters(id, 1, "s", "b", 1,
			protocol.NetworkInterfaceCounter{Name: "eth1", RXBytes: 200, TXBytes: 200}, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 100, TXBytes: 100}), 0, 0, false, "eth0,eth1"},
		{"both grow", reportWithNetworkCounters(id, 1, "s", "b", 2,
			protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 110, TXBytes: 120}, protocol.NetworkInterfaceCounter{Name: "eth1", RXBytes: 220, TXBytes: 230}), 30, 50, false, "eth0,eth1"},
		{"interface removed", reportWithNetworkCounters(id, 1, "s", "b", 3,
			protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 120, TXBytes: 130}), 40, 60, true, "eth0"},
		{"interface added baseline", reportWithNetworkCounters(id, 1, "s", "b", 4,
			protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 130, TXBytes: 140}, protocol.NetworkInterfaceCounter{Name: "eth2", RXBytes: 5, TXBytes: 6}), 50, 70, true, "eth0,eth2"},
		{"new interface now grows", reportWithNetworkCounters(id, 1, "s", "b", 5,
			protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 140, TXBytes: 150}, protocol.NetworkInterfaceCounter{Name: "eth2", RXBytes: 15, TXBytes: 16}), 70, 90, false, "eth0,eth2"},
		{"one interface rollback", reportWithNetworkCounters(id, 1, "s", "b", 6,
			protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 1, TXBytes: 1}, protocol.NetworkInterfaceCounter{Name: "eth2", RXBytes: 20, TXBytes: 21}), 75, 95, true, "eth0,eth2"},
	}
	for i, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			state, accepted, _, err := store.ProcessReport(ctx, id, step.report, base.Add(time.Duration(i)*time.Second))
			if err != nil || !accepted {
				t.Fatalf("accepted=%t err=%v", accepted, err)
			}
			if state.RXTotal != step.rx || state.TXTotal != step.tx || state.ContinuityPartial != step.partial {
				t.Fatalf("state totals=%d/%d partial=%t want=%d/%d partial=%t", state.RXTotal, state.TXTotal, state.ContinuityPartial, step.rx, step.tx, step.partial)
			}
			got := ""
			for _, counter := range state.NetworkCounters {
				if got != "" {
					got += ","
				}
				got += counter.Name
			}
			if got != step.interfaces {
				t.Fatalf("stored interfaces=%q want=%q", got, step.interfaces)
			}
		})
	}
	duplicate := steps[len(steps)-1].report
	duplicate.NetworkCounters = &protocol.NetworkCounterSet{Version: 1, Interfaces: []protocol.NetworkInterfaceCounter{{Name: "eth9", RXBytes: 999, TXBytes: 999}}}
	duplicate.RXBytes, duplicate.TXBytes = 999, 999
	state, accepted, reason, err := store.ProcessReport(ctx, id, duplicate, base.Add(10*time.Second))
	if err != nil || accepted || reason != "duplicate or stale sequence" || state.RXTotal != 75 || len(state.NetworkCounters) != 2 || state.NetworkCounters[1].Name != "eth2" {
		t.Fatalf("duplicate changed snapshot: accepted=%t reason=%q state=%+v err=%v", accepted, reason, state, err)
	}
}

func TestNetworkCounterModeBootAndEmptySetTransitions(t *testing.T) {
	store, id, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	base := time.Unix(2000, 0)
	steps := []struct {
		name    string
		report  protocol.Report
		rx, tx  uint64
		partial bool
	}{
		{"legacy baseline", validReport(id, 1, "s", "b", 1, 100, 100), 0, 0, false},
		{"legacy delta", validReport(id, 1, "s", "b", 2, 150, 140), 50, 40, false},
		{"legacy reset is not traffic", validReport(id, 1, "s", "b", 3, 2, 150), 50, 40, true},
		{"legacy after reset", validReport(id, 1, "s", "b", 4, 20, 160), 68, 50, false},
		{"legacy to v1 baseline", reportWithNetworkCounters(id, 1, "s", "b", 5, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 1000, TXBytes: 2000}), 68, 50, true},
		{"v1 delta", reportWithNetworkCounters(id, 1, "s", "b", 6, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 1030, TXBytes: 2035}), 98, 85, false},
		{"boot rebaseline", reportWithNetworkCounters(id, 2, "s2", "b2", 1, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 1, TXBytes: 1}), 98, 85, true},
		{"v1 after boot", reportWithNetworkCounters(id, 2, "s2", "b2", 2, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 6, TXBytes: 7}), 103, 91, false},
		{"v1 to legacy baseline", validReport(id, 3, "s3", "b2", 1, 500, 700), 103, 91, true},
		{"legacy resumes", validReport(id, 3, "s3", "b2", 2, 510, 720), 113, 111, false},
	}
	for i, step := range steps {
		state, accepted, _, err := store.ProcessReport(ctx, id, step.report, base.Add(time.Duration(i)*time.Second))
		if err != nil || !accepted {
			t.Fatalf("%s: accepted=%t err=%v", step.name, accepted, err)
		}
		if state.RXTotal != step.rx || state.TXTotal != step.tx || state.ContinuityPartial != step.partial {
			t.Fatalf("%s: totals=%d/%d partial=%t want=%d/%d partial=%t", step.name, state.RXTotal, state.TXTotal, state.ContinuityPartial, step.rx, step.tx, step.partial)
		}
	}

	emptyStore, emptyID, _ := testStore(t, ":memory:")
	defer emptyStore.Close()
	empty := func(sequence uint64, counters ...protocol.NetworkInterfaceCounter) protocol.Report {
		return reportWithNetworkCounters(emptyID, 1, "empty", "b", sequence, counters...)
	}
	for i, step := range []struct {
		report  protocol.Report
		partial bool
	}{
		{empty(1), false},
		{empty(2), false},
		{empty(3, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 50, TXBytes: 60}), true},
		{empty(4), true},
	} {
		state, accepted, _, err := emptyStore.ProcessReport(ctx, emptyID, step.report, base.Add(time.Duration(i)*time.Second))
		if err != nil || !accepted || state.ContinuityPartial != step.partial {
			t.Fatalf("empty-set step %d accepted=%t partial=%t err=%v", i, accepted, state.ContinuityPartial, err)
		}
	}
}

func TestNetworkCounterSnapshotPersistsAcrossRestartAndRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-snapshot.db")
	store, id, _ := testStore(t, path)
	base := time.Unix(3000, 0)
	first := reportWithNetworkCounters(id, 1, "s", "b", 1, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 100, TXBytes: 200})
	if _, accepted, _, err := store.ProcessReport(context.Background(), id, first, base); err != nil || !accepted {
		t.Fatalf("first accepted=%t err=%v", accepted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second := reportWithNetworkCounters(id, 1, "s", "b", 2, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 125, TXBytes: 240})
	state, accepted, _, err := store.ProcessReport(context.Background(), id, second, base.Add(time.Second))
	if err != nil || !accepted || state.RXTotal != 25 || state.TXTotal != 40 || state.ContinuityPartial {
		store.Close()
		t.Fatalf("persisted baseline lost: state=%+v accepted=%t err=%v", state, accepted, err)
	}
	if _, err := store.db.Exec(`UPDATE agent_state SET network_counters_json='{"bad":true}' WHERE agent_id=?`, id); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.ListStates(context.Background()); err == nil {
		store.Close()
		t.Fatal("corrupt stored interface snapshot was silently accepted")
	}
	store.Close()
}

func TestEmptyNetworkCounterListPersistsAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-network-snapshot.db")
	store, id, _ := testStore(t, path)
	ctx := context.Background()
	base := time.Unix(3500, 0)
	first := reportWithNetworkCounters(id, 1, "s", "b", 1)
	if _, accepted, _, err := store.ProcessReport(ctx, id, first, base); err != nil || !accepted {
		store.Close()
		t.Fatalf("empty baseline accepted=%t err=%v", accepted, err)
	}
	var raw string
	if err := store.db.QueryRow(`SELECT network_counters_json FROM agent_state WHERE agent_id=?`, id).Scan(&raw); err != nil || raw != "[]" {
		store.Close()
		t.Fatalf("empty snapshot stored as %q err=%v", raw, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	states, err := store.ListStates(ctx)
	if err != nil || len(states) != 1 || states[0].NetworkCountersVersion != 1 || states[0].NetworkCounters == nil || len(states[0].NetworkCounters) != 0 {
		t.Fatalf("reopened empty snapshot=%+v err=%v", states, err)
	}
	second := reportWithNetworkCounters(id, 1, "s", "b", 2)
	state, accepted, _, err := store.ProcessReport(ctx, id, second, base.Add(time.Second))
	if err != nil || !accepted || state.ContinuityPartial || state.RXTotal != 0 || state.TXTotal != 0 {
		t.Fatalf("empty snapshot did not continue after restart: state=%+v accepted=%t err=%v", state, accepted, err)
	}
}

func TestNetworkCounterModeSnapshotMismatchFailsClosed(t *testing.T) {
	store, id, _ := testStore(t, ":memory:")
	defer store.Close()
	report := reportWithNetworkCounters(id, 1, "s", "b", 1, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 1, TXBytes: 2})
	if _, accepted, _, err := store.ProcessReport(context.Background(), id, report, time.Unix(4000, 0)); err != nil || !accepted {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_state SET network_counters_version=0 WHERE agent_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListStates(context.Background()); err == nil {
		t.Fatal("stored v0 version with a v1 snapshot was accepted")
	}
}

func TestNetworkCounterPermanentOverflowLeavesWholeReportUncommitted(t *testing.T) {
	store, id, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	base := time.Unix(5000, 0)
	max := uint64(math.MaxInt64)
	steps := []protocol.Report{
		reportWithNetworkCounters(id, 1, "s1", "b1", 1, protocol.NetworkInterfaceCounter{Name: "eth0"}),
		reportWithNetworkCounters(id, 1, "s1", "b1", 2, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: max, TXBytes: max}),
		reportWithNetworkCounters(id, 2, "s2", "b2", 1, protocol.NetworkInterfaceCounter{Name: "eth0"}),
	}
	for i, report := range steps {
		if _, accepted, _, err := store.ProcessReport(ctx, id, report, base.Add(time.Duration(i)*time.Second)); err != nil || !accepted {
			t.Fatalf("baseline step %d accepted=%t err=%v", i, accepted, err)
		}
	}
	tooMuch := reportWithNetworkCounters(id, 2, "s2", "b2", 2, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 1, TXBytes: 1})
	if _, accepted, _, err := store.ProcessReport(ctx, id, tooMuch, base.Add(3*time.Second)); err == nil || accepted {
		t.Fatalf("overflow report accepted=%t err=%v", accepted, err)
	}
	states, err := store.ListStates(ctx)
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%v err=%v", states, err)
	}
	state := states[0]
	if state.Epoch != 2 || state.Sequence != 1 || state.RXTotal != max || state.TXTotal != max || len(state.NetworkCounters) != 1 || state.NetworkCounters[0].RXBytes != 0 {
		t.Fatalf("overflow report partially advanced persisted state: %+v", state)
	}
	var activeSessionCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM agent_sessions WHERE agent_id=? AND active=1`, id).Scan(&activeSessionCount); err != nil || activeSessionCount != 1 {
		t.Fatalf("overflow report changed sessions: active=%d err=%v", activeSessionCount, err)
	}
}

func TestNetworkCounterPartialPlanAccumulatesProvableDeltaAndTrafficReset(t *testing.T) {
	store, id, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	base := time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)
	plan := AgentPlan{AgentID: id, TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(ctx, plan, nil, base); err != nil {
		t.Fatal(err)
	}
	reports := []protocol.Report{
		reportWithNetworkCounters(id, 1, "s", "b", 1, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 100}, protocol.NetworkInterfaceCounter{Name: "eth1", RXBytes: 200}),
		reportWithNetworkCounters(id, 1, "s", "b", 2, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 110}, protocol.NetworkInterfaceCounter{Name: "eth1", RXBytes: 220}),
		reportWithNetworkCounters(id, 1, "s", "b", 3, protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 120}, protocol.NetworkInterfaceCounter{Name: "eth2", RXBytes: 50}),
	}
	for i, report := range reports {
		if _, accepted, _, err := store.ProcessReport(ctx, id, report, base.Add(time.Duration(i+1)*time.Second)); err != nil || !accepted {
			t.Fatalf("report %d accepted=%t err=%v", i, accepted, err)
		}
	}
	got, exists, err := store.GetAgentPlan(ctx, id)
	if err != nil || !exists || got.UsageBytes != 40 || got.UsageStatus != "partial" {
		t.Fatalf("plan did not accumulate provable delta on partial continuity: %+v exists=%t err=%v", got, exists, err)
	}
	totals, err := store.TrafficTotals(ctx, id)
	if err != nil || totals.RXTotal != 40 {
		t.Fatalf("traffic totals before reset=%+v err=%v", totals, err)
	}
	reset, err := store.ResetTrafficTotals(ctx, id, "abcdef0123456789abcdef0123456789", base.Add(4*time.Second), time.Minute)
	if err != nil || reset.RXTotal != 0 {
		t.Fatalf("traffic reset=%+v err=%v", reset, err)
	}
	continued := reportWithNetworkCounters(id, 1, "s", "b", 4,
		protocol.NetworkInterfaceCounter{Name: "eth0", RXBytes: 130}, protocol.NetworkInterfaceCounter{Name: "eth2", RXBytes: 60})
	if _, accepted, _, err := store.ProcessReport(ctx, id, continued, base.Add(5*time.Second)); err != nil || !accepted {
		t.Fatalf("post-reset report accepted=%t err=%v", accepted, err)
	}
	totals, err = store.TrafficTotals(ctx, id)
	if err != nil || totals.RXTotal != 20 {
		t.Fatalf("traffic totals after reset=%+v err=%v", totals, err)
	}
}
