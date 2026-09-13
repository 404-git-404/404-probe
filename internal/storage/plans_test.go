package storage

import (
	"context"
	"testing"
	"time"
)

func TestPlanCycleBoundsPreservesRawMonthEndAnchor(t *testing.T) {
	plan := AgentPlan{TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-31", Timezone: "America/Los_Angeles"}
	for _, test := range []struct {
		now, start, end string
	}{
		{"2024-02-15T12:00:00-08:00", "2024-01-31", "2024-02-29"},
		{"2024-03-15T12:00:00-07:00", "2024-02-29", "2024-03-31"},
		{"2025-03-15T12:00:00-07:00", "2025-02-28", "2025-03-31"},
	} {
		now, err := time.Parse(time.RFC3339, test.now)
		if err != nil {
			t.Fatal(err)
		}
		start, end, active, err := PlanCycleBounds(plan, now)
		if err != nil || !active {
			t.Fatalf("bounds active=%t err=%v", active, err)
		}
		location, _ := time.LoadLocation(plan.Timezone)
		if got := time.UnixMilli(*start).In(location).Format("2006-01-02"); got != test.start {
			t.Errorf("start=%s want=%s", got, test.start)
		}
		if got := time.UnixMilli(*end).In(location).Format("2006-01-02"); got != test.end {
			t.Errorf("end=%s want=%s", got, test.end)
		}
	}
}

func TestAgentPlanCountryOverrideRoundTrip(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	saved, err := store.PutAgentPlan(context.Background(), AgentPlan{AgentID: agentID, CountryCodeOverride: "DE"}, nil, time.Unix(100, 0))
	if err != nil || saved.CountryCodeOverride != "DE" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	got, exists, err := store.GetAgentPlan(context.Background(), agentID)
	if err != nil || !exists || got.CountryCodeOverride != "DE" {
		t.Fatalf("got=%+v exists=%t err=%v", got, exists, err)
	}
	for _, invalid := range []string{"D1", "AA", "ZZ"} {
		if _, err := store.PutAgentPlan(context.Background(), AgentPlan{AgentID: agentID, CountryCodeOverride: invalid}, nil, time.Unix(101, 0)); err == nil {
			t.Fatalf("invalid country override %q accepted", invalid)
		}
	}
}

func TestPlanTrafficUsesAcceptedPermanentDeltasAndMarksResetsPartial(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	calibration := uint64(100)
	plan := AgentPlan{AgentID: agentID, TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(ctx, plan, &calibration, time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	first := validReport(agentID, 1, "session", "boot-a", 1, 1000, 2000)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, first, time.Date(2024, 1, 10, 0, 0, 10, 0, time.UTC)); err != nil || !accepted {
		t.Fatalf("first accepted=%t err=%v", accepted, err)
	}
	second := validReport(agentID, 1, "session", "boot-a", 2, 1020, 2030)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, second, time.Date(2024, 1, 10, 0, 0, 20, 0, time.UTC)); err != nil || !accepted {
		t.Fatalf("second accepted=%t err=%v", accepted, err)
	}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, second, time.Date(2024, 1, 10, 0, 0, 21, 0, time.UTC)); err != nil || accepted {
		t.Fatalf("duplicate accepted=%t err=%v", accepted, err)
	}
	got, exists, err := store.GetAgentPlan(ctx, agentID)
	if err != nil || !exists || got.UsageBytes != 150 || got.UsageStatus != "calibrated" {
		t.Fatalf("plan=%+v exists=%t err=%v", got, exists, err)
	}
	reboot := validReport(agentID, 2, "session-b", "boot-b", 1, 5, 7)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, reboot, time.Date(2024, 1, 10, 0, 1, 0, 0, time.UTC)); err != nil || !accepted {
		t.Fatalf("reboot accepted=%t err=%v", accepted, err)
	}
	got, _, _ = store.GetAgentPlan(ctx, agentID)
	if got.UsageBytes != 162 || got.UsageStatus != "partial" {
		t.Fatalf("after reboot=%+v", got)
	}
}

func TestPlanTrafficDoesNotGuessAcrossCycleBoundary(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	plan := AgentPlan{AgentID: agentID, TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(ctx, plan, nil, time.Date(2024, 1, 31, 23, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	first := validReport(agentID, 1, "session", "boot", 1, 100, 0)
	_, _, _, _ = store.ProcessReport(ctx, agentID, first, time.Date(2024, 1, 31, 23, 59, 0, 0, time.UTC))
	crossing := validReport(agentID, 1, "session", "boot", 2, 150, 0)
	_, _, _, _ = store.ProcessReport(ctx, agentID, crossing, time.Date(2024, 2, 1, 0, 1, 0, 0, time.UTC))
	inside := validReport(agentID, 1, "session", "boot", 3, 160, 0)
	_, _, _, _ = store.ProcessReport(ctx, agentID, inside, time.Date(2024, 2, 1, 0, 2, 0, 0, time.UTC))
	got, _, err := store.GetAgentPlan(ctx, agentID)
	if err != nil || got.UsageBytes != 10 || got.UsageStatus != "partial" {
		t.Fatalf("plan=%+v err=%v", got, err)
	}
}

func TestAgentPlanAtMarksUnobservedNewCyclePartial(t *testing.T) {
	oldStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	oldEnd := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	plan := AgentPlan{
		TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1,
		CycleAnchor: "2024-01-01", Timezone: "UTC", UsageBytes: 1234,
		UsageStatus: "calibrated", CycleStart: &oldStart, CycleEnd: &oldEnd,
	}
	got, err := AgentPlanAt(plan, time.Date(2024, 2, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.UsageBytes != 0 || got.UsageStatus != "partial" || got.CycleStart == nil || *got.CycleStart != oldEnd {
		t.Fatalf("plan=%+v", got)
	}
}

func TestPlanCalibrationDoesNotCountDeltaSpanningCalibrationBoundary(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	first := validReport(agentID, 1, "session", "boot", 1, 100, 0)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, first, time.Date(2024, 1, 9, 0, 0, 0, 0, time.UTC)); err != nil || !accepted {
		t.Fatalf("first accepted=%t err=%v", accepted, err)
	}
	calibration := uint64(1000)
	plan := AgentPlan{AgentID: agentID, TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(ctx, plan, &calibration, time.Date(2024, 1, 9, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	spanning := validReport(agentID, 1, "session", "boot", 2, 200, 0)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, spanning, time.Date(2024, 1, 9, 13, 0, 0, 0, time.UTC)); err != nil || !accepted {
		t.Fatalf("spanning accepted=%t err=%v", accepted, err)
	}
	got, _, err := store.GetAgentPlan(ctx, agentID)
	if err != nil || got.UsageBytes != calibration || got.UsageStatus != "partial" || got.ObserveAfter != 0 {
		t.Fatalf("after spanning report=%+v err=%v", got, err)
	}
	next := validReport(agentID, 1, "session", "boot", 3, 225, 0)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, next, time.Date(2024, 1, 9, 14, 0, 0, 0, time.UTC)); err != nil || !accepted {
		t.Fatalf("next accepted=%t err=%v", accepted, err)
	}
	got, _, err = store.GetAgentPlan(ctx, agentID)
	if err != nil || got.UsageBytes != calibration+25 || got.UsageStatus != "partial" {
		t.Fatalf("after next report=%+v err=%v", got, err)
	}
}
