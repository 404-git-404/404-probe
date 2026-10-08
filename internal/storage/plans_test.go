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
	if got.UsageBytes != 150 || got.UsageStatus != "partial" {
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

func TestAgentPlanAtClockRollbackPreservesCalibratedCycle(t *testing.T) {
	februaryStart := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	marchStart := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	plan := AgentPlan{
		TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC",
		UsageBytes: 1000, UsageStatus: "calibrated", CycleStart: &februaryStart, CycleEnd: &marchStart,
	}
	got, err := AgentPlanAt(plan, time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.UsageBytes != 1000 || got.UsageStatus != "partial" || got.CycleStart == nil || *got.CycleStart != februaryStart || got.CycleEnd == nil || *got.CycleEnd != marchStart {
		t.Fatalf("clock rollback changed calibrated cycle: %+v", got)
	}
}

func TestPlanCycleResolverIsSharedByPlanView(t *testing.T) {
	jan1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	feb1 := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	mar1 := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	base := AgentPlan{TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC", UsageBytes: 75, UsageStatus: "calibrated"}
	tests := []struct {
		name  string
		plan  AgentPlan
		now   time.Time
		phase planCyclePhase
	}{
		{"uninitialized inactive", base, time.Date(2023, 12, 15, 0, 0, 0, 0, time.UTC), planCycleInactive},
		{"uninitialized active", base, time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC), planCycleInitialized},
		{"same period", func() AgentPlan { p := base; p.CycleStart, p.CycleEnd = &feb1, &mar1; return p }(), time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC), planCycleCurrent},
		{"forward period", func() AgentPlan { p := base; p.CycleStart, p.CycleEnd = &jan1, &feb1; return p }(), time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC), planCycleAdvanced},
		{"rollback active", func() AgentPlan { p := base; p.CycleStart, p.CycleEnd = &feb1, &mar1; return p }(), time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), planCycleRollback},
		{"rollback before anchor", func() AgentPlan {
			p := base
			p.CycleAnchor = "2024-02-01"
			p.CycleStart, p.CycleEnd = &feb1, &mar1
			return p
		}(), time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), planCycleRollback},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, phase, err := resolvePlanCycle(test.plan, test.now)
			if err != nil || phase != test.phase {
				t.Fatalf("resolved phase=%d err=%v want=%d", phase, err, test.phase)
			}
			view, err := AgentPlanAt(test.plan, test.now)
			if err != nil {
				t.Fatal(err)
			}
			if view.UsageBytes != resolved.UsageBytes || view.UsageStatus != resolved.UsageStatus || !sameInt64(view.CycleStart, resolved.CycleStart) || !sameInt64(view.CycleEnd, resolved.CycleEnd) {
				t.Fatalf("view=%+v resolver=%+v", view, resolved)
			}
		})
	}
}

func TestPlanCycleResolverRejectsIncompleteStoredBounds(t *testing.T) {
	start := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	end := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	for _, test := range []struct {
		name string
		plan AgentPlan
	}{
		{"missing end", AgentPlan{CycleStart: &start}},
		{"missing start", AgentPlan{CycleEnd: &end}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := resolvePlanCycle(test.plan, time.Now()); err == nil {
				t.Fatal("incomplete stored cycle bounds accepted")
			}
			if _, err := AgentPlanAt(test.plan, time.Now()); err == nil {
				t.Fatal("plan view accepted incomplete stored cycle bounds")
			}
		})
	}
}

func TestPlanUsageAndCycleCursorSurviveClockRollbackAndAdvance(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	feb10 := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)
	calibration := uint64(1000)
	plan := AgentPlan{AgentID: agentID, TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(ctx, plan, &calibration, feb10); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name       string
		sequence   uint64
		rawRX      uint64
		received   time.Time
		usage      uint64
		status     string
		cycleStart int64
		cycleEnd   int64
	}{
		{"first sample", 1, 1000, feb10.Add(time.Second), 1000, "calibrated", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{"normal February growth", 2, 1100, feb10.Add(2 * time.Second), 1100, "calibrated", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{"first January rollback report", 3, 1200, time.Date(2024, 1, 20, 12, 0, 0, 0, time.UTC), 1200, "partial", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{"second January rollback report", 4, 1300, time.Date(2024, 1, 21, 12, 0, 0, 0, time.UTC), 1300, "partial", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{"February clock recovery", 5, 1400, time.Date(2024, 2, 11, 12, 0, 0, 0, time.UTC), 1400, "partial", time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{"March rollover baseline", 6, 1500, time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC), 0, "partial", time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{"March growth resumes", 7, 1525, time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC), 25, "partial", time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
	}
	var previousUpdatedAt int64
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			report := validReport(agentID, 1, "session", "boot", step.sequence, step.rawRX, 0)
			if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, step.received); err != nil || !accepted {
				t.Fatalf("accepted=%t err=%v", accepted, err)
			}
			got, exists, err := store.GetAgentPlan(ctx, agentID)
			if err != nil || !exists {
				t.Fatalf("plan exists=%t err=%v", exists, err)
			}
			if got.UsageBytes != step.usage || got.UsageStatus != step.status || got.CycleStart == nil || *got.CycleStart != step.cycleStart || got.CycleEnd == nil || *got.CycleEnd != step.cycleEnd {
				t.Fatalf("plan=%+v want usage=%d status=%s cycle=%d..%d", got, step.usage, step.status, step.cycleStart, step.cycleEnd)
			}
			if got.UpdatedAt < previousUpdatedAt {
				t.Fatalf("updated_at regressed from %d to %d", previousUpdatedAt, got.UpdatedAt)
			}
			previousUpdatedAt = got.UpdatedAt
		})
	}
}

func TestPlanClockRollbackBeforeAnchorStillAccumulatesCurrentProof(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	feb10 := time.Date(2024, 2, 10, 0, 0, 0, 0, time.UTC)
	calibration := uint64(100)
	plan := AgentPlan{AgentID: agentID, TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-02-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(ctx, plan, &calibration, feb10); err != nil {
		t.Fatal(err)
	}
	first := validReport(agentID, 1, "session", "boot", 1, 1000, 0)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, first, feb10.Add(time.Second)); err != nil || !accepted {
		t.Fatal(err)
	}
	rolledBack := validReport(agentID, 1, "session", "boot", 2, 1010, 0)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, rolledBack, time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)); err != nil || !accepted {
		t.Fatal(err)
	}
	got, exists, err := store.GetAgentPlan(ctx, agentID)
	if err != nil || !exists || got.UsageBytes != 110 || got.UsageStatus != "partial" || got.CycleStart == nil || *got.CycleStart != time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("pre-anchor rollback plan=%+v exists=%t err=%v", got, exists, err)
	}
}

func TestObserveAfterStillBlocksRollbackSpanningDeltaAndClearsOnRecovery(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	feb9 := time.Date(2024, 2, 9, 0, 0, 0, 0, time.UTC)
	initial := validReport(agentID, 1, "session", "boot", 1, 1000, 0)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, initial, feb9); err != nil || !accepted {
		t.Fatal(err)
	}
	feb10 := time.Date(2024, 2, 10, 0, 0, 0, 0, time.UTC)
	calibration := uint64(1000)
	plan := AgentPlan{AgentID: agentID, TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(ctx, plan, &calibration, feb10); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		sequence uint64
		rawRX    uint64
		at       time.Time
		usage    uint64
		observe  bool
	}{
		{2, 1010, time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC), 1000, true},
		{3, 1020, time.Date(2024, 1, 21, 0, 0, 0, 0, time.UTC), 1000, true},
		{4, 1030, time.Date(2024, 2, 11, 0, 0, 0, 0, time.UTC), 1000, false},
		{5, 1040, time.Date(2024, 2, 11, 0, 0, 1, 0, time.UTC), 1010, false},
	} {
		report := validReport(agentID, 1, "session", "boot", step.sequence, step.rawRX, 0)
		if _, accepted, _, err := store.ProcessReport(ctx, agentID, report, step.at); err != nil || !accepted {
			t.Fatalf("sequence %d accepted=%t err=%v", step.sequence, accepted, err)
		}
		got, _, err := store.GetAgentPlan(ctx, agentID)
		if err != nil || got.UsageBytes != step.usage || (got.ObserveAfter != 0) != step.observe {
			t.Fatalf("sequence %d plan=%+v err=%v", step.sequence, got, err)
		}
	}
}

func TestIncompleteCycleBoundsAbortReportTransaction(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	base := time.Date(2024, 2, 10, 0, 0, 0, 0, time.UTC)
	plan := AgentPlan{AgentID: agentID, TrafficMode: "rx", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
	if _, err := store.PutAgentPlan(ctx, plan, nil, base); err != nil {
		t.Fatal(err)
	}
	first := validReport(agentID, 1, "session", "boot", 1, 100, 0)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, first, base.Add(time.Second)); err != nil || !accepted {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_plans SET cycle_end=NULL WHERE agent_id=?`, agentID); err != nil {
		t.Fatal(err)
	}
	second := validReport(agentID, 1, "session", "boot", 2, 200, 0)
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, second, base.Add(2*time.Second)); err == nil || accepted {
		t.Fatalf("report with corrupt cycle bounds accepted=%t err=%v", accepted, err)
	}
	states, err := store.ListStates(ctx)
	if err != nil || len(states) != 1 || states[0].Sequence != 1 || states[0].RXTotal != 0 {
		t.Fatalf("failed plan accounting partially committed report: states=%+v err=%v", states, err)
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
