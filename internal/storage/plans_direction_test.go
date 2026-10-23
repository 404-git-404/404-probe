package storage

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"
)

// legacyUsageAfterReport is the usage part of accountPlanTrafficTx exactly as v1.0.6 had it. The property test
// below compares it with the stored usage after every report, so adding the direction counters cannot have
// changed how the billed usage is counted.
func legacyUsageAfterReport(plan AgentPlan, old, current State, existed bool, now time.Time) uint64 {
	resolved, phase, err := resolvePlanCycle(plan, now)
	if err != nil || !planHasCycle(plan) || phase == planCycleInactive {
		return plan.UsageBytes
	}
	usage := resolved.UsageBytes
	spans := existed && resolved.ObserveAfter > 0 && old.LastSeen < resolved.ObserveAfter
	if existed && (phase == planCycleCurrent || phase == planCycleRollback) && !spans {
		rxDelta, txDelta := current.RXTotal-old.RXTotal, current.TXTotal-old.TXTotal
		switch resolved.TrafficMode {
		case "rx":
			usage += rxDelta
		case "tx":
			usage += txDelta
		case "sum":
			usage += rxDelta + txDelta
		}
	}
	return usage
}

func directionPlan(agentID, mode string) AgentPlan {
	return AgentPlan{AgentID: agentID, TrafficMode: mode, CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC"}
}

func assertDirectionInvariant(t *testing.T, plan AgentPlan, context string) {
	t.Helper()
	if reason := planDirectionError(plan.TrafficMode, plan.CycleDirBasis, plan.UsageBytes, plan.CycleRXBytes, plan.CycleTXBytes); reason != "" {
		t.Fatalf("%s: %s: %+v", context, reason, plan)
	}
	if (plan.CycleDirBasis == "exact") != (plan.CycleDirSince == nil) {
		t.Fatalf("%s: since mark %v does not fit basis %s", context, plan.CycleDirSince, plan.CycleDirBasis)
	}
}

// Review B6: the clock-rollback branch counts the usage, so it counts the directions too, all three together.
func TestPlanRollbackDeltaIsCountedInBothDirectionsWithTheUsage(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	feb10 := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)
	if _, err := store.PutAgentPlan(ctx, directionPlan(agentID, "sum"), nil, feb10); err != nil {
		t.Fatal(err)
	}
	report := func(seq, rx, tx uint64, at time.Time) AgentPlan {
		t.Helper()
		if _, accepted, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", seq, rx, tx), at); err != nil || !accepted {
			t.Fatalf("report %d accepted=%t err=%v", seq, accepted, err)
		}
		got, _, err := store.GetAgentPlan(ctx, agentID)
		if err != nil {
			t.Fatal(err)
		}
		assertDirectionInvariant(t, got, fmt.Sprintf("report %d", seq))
		return got
	}
	report(1, 1000, 500, feb10.Add(time.Second))
	got := report(2, 1100, 550, feb10.Add(2*time.Second))
	if got.UsageBytes != 150 || got.CycleRXBytes != 100 || got.CycleTXBytes != 50 || got.CycleDirBasis != "exact" {
		t.Fatalf("before the rollback: %+v", got)
	}
	// The clock goes back into January: rx +30, tx +20. The usage, rx and tx all grow.
	got = report(3, 1130, 570, time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC))
	if got.UsageBytes != 200 || got.CycleRXBytes != 130 || got.CycleTXBytes != 70 || got.UsageStatus != "partial" || got.CycleDirBasis != "exact" {
		t.Fatalf("the rollback delta must raise usage, rx and tx together: %+v", got)
	}
	// A report whose delta spans an edit fence moves none of the three.
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_plans SET observe_after=? WHERE agent_id=?`, time.Date(2024, 1, 25, 0, 0, 0, 0, time.UTC).UnixMilli(), agentID); err != nil {
		t.Fatal(err)
	}
	got = report(4, 1200, 600, time.Date(2024, 1, 21, 0, 0, 0, 0, time.UTC))
	if got.UsageBytes != 200 || got.CycleRXBytes != 130 || got.CycleTXBytes != 70 {
		t.Fatalf("a delta across the fence was counted: %+v", got)
	}
	// The report that first passes the fence still spans it (its delta started before); the next one counts.
	got = report(5, 1210, 604, time.Date(2024, 2, 11, 0, 0, 0, 0, time.UTC))
	if got.UsageBytes != 200 || got.CycleRXBytes != 130 || got.CycleTXBytes != 70 || got.ObserveAfter != 0 {
		t.Fatalf("the first report after the fence: %+v", got)
	}
	got = report(6, 1220, 610, time.Date(2024, 2, 11, 0, 0, 30, 0, time.UTC))
	if got.UsageBytes != 216 || got.CycleRXBytes != 140 || got.CycleTXBytes != 76 {
		t.Fatalf("after the fence: %+v", got)
	}
}

// The directions count whatever the billing mode is, and the mode only decides which of them is billed.
func TestPlanDirectionsAreCountedForEveryBillingMode(t *testing.T) {
	for _, mode := range []string{"rx", "tx", "sum"} {
		t.Run(mode, func(t *testing.T) {
			store, agentID, _ := testStore(t, ":memory:")
			defer store.Close()
			ctx := context.Background()
			feb10 := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)
			if _, err := store.PutAgentPlan(ctx, directionPlan(agentID, mode), nil, feb10); err != nil {
				t.Fatal(err)
			}
			for i, step := range [][2]uint64{{1000, 500}, {1300, 520}, {1310, 900}} {
				if _, accepted, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", uint64(i+1), step[0], step[1]), feb10.Add(time.Duration(i+1)*time.Second)); err != nil || !accepted {
					t.Fatalf("step %d: %v", i, err)
				}
			}
			got, _, _ := store.GetAgentPlan(ctx, agentID)
			if got.CycleRXBytes != 310 || got.CycleTXBytes != 400 || got.CycleDirBasis != "exact" || got.CycleDirSince != nil {
				t.Fatalf("directions %+v", got)
			}
			want := map[string]uint64{"rx": 310, "tx": 400, "sum": 710}[mode]
			if got.UsageBytes != want {
				t.Fatalf("usage %d, want %d", got.UsageBytes, want)
			}
			assertDirectionInvariant(t, got, mode)
		})
	}
}

func TestPlanDirectionsResetWithTheUsageAndSurviveHarmlessEdits(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	feb10 := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)
	plan := directionPlan(agentID, "sum")
	if _, err := store.PutAgentPlan(ctx, plan, nil, feb10); err != nil {
		t.Fatal(err)
	}
	for i, step := range [][2]uint64{{100, 100}, {160, 130}} {
		if _, accepted, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", uint64(i+1), step[0], step[1]), feb10.Add(time.Duration(i+1)*time.Second)); err != nil || !accepted {
			t.Fatal(err)
		}
	}
	got, _, _ := store.GetAgentPlan(ctx, agentID)
	if got.UsageBytes != 90 || got.CycleRXBytes != 60 || got.CycleTXBytes != 30 {
		t.Fatalf("setup %+v", got)
	}

	// Price, quota and bandwidth are not accounting policy: usage and directions stay as they are.
	edited := plan
	edited.PurchasePrice, edited.Currency, edited.RenewalDate = "9", "USD", "2024-03-01"
	kept, err := store.PutAgentPlan(ctx, edited, nil, feb10.Add(time.Minute))
	if err != nil || kept.UsageBytes != 90 || kept.CycleRXBytes != 60 || kept.CycleTXBytes != 30 || kept.CycleDirBasis != "exact" {
		t.Fatalf("harmless edit: %+v %v", kept, err)
	}
	stored, _, _ := store.GetAgentPlan(ctx, agentID)
	if stored.CycleRXBytes != 60 || stored.CycleTXBytes != 30 {
		t.Fatalf("stored after a harmless edit: %+v", stored)
	}

	// A calibration counts again from the calibration, marked "since".
	calibration := uint64(5000)
	calibrated, err := store.PutAgentPlan(ctx, edited, &calibration, feb10.Add(2*time.Minute))
	if err != nil || calibrated.UsageBytes != 5000 || calibrated.CycleRXBytes != 0 || calibrated.CycleTXBytes != 0 || calibrated.CycleDirBasis != "since" ||
		calibrated.CycleDirSince == nil || *calibrated.CycleDirSince != feb10.Add(2*time.Minute).UnixMilli() {
		t.Fatalf("calibration: %+v %v", calibrated, err)
	}
	assertDirectionInvariant(t, calibrated, "calibration")
	// The first report after the calibration spans the boundary (its delta began before): nothing is counted.
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", 3, 200, 140), feb10.Add(3*time.Minute)); err != nil || !accepted {
		t.Fatal(err)
	}
	got, _, _ = store.GetAgentPlan(ctx, agentID)
	if got.UsageBytes != 5000 || got.CycleRXBytes != 0 || got.CycleTXBytes != 0 || got.CycleDirBasis != "since" {
		t.Fatalf("the spanning report moved something: %+v", got)
	}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", 4, 240, 150), feb10.Add(3*time.Minute+30*time.Second)); err != nil || !accepted {
		t.Fatal(err)
	}
	got, _, _ = store.GetAgentPlan(ctx, agentID)
	if got.UsageBytes != 5050 || got.CycleRXBytes != 40 || got.CycleTXBytes != 10 || got.CycleDirBasis != "since" {
		t.Fatalf("after the calibration: %+v", got)
	}
	assertDirectionInvariant(t, got, "after calibration")

	// A harmless edit after a calibration keeps the "since" mark as well.
	again, err := store.PutAgentPlan(ctx, edited, nil, feb10.Add(4*time.Minute))
	if err != nil || again.CycleDirBasis != "since" || again.CycleRXBytes != 40 || again.CycleDirSince == nil || *again.CycleDirSince != *calibrated.CycleDirSince {
		t.Fatalf("edit after calibration: %+v %v", again, err)
	}

	// Changing the billing mode is a policy change: usage and directions start from zero together.
	changed := edited
	changed.TrafficMode = "rx"
	reset, err := store.PutAgentPlan(ctx, changed, nil, feb10.Add(5*time.Minute))
	if err != nil || reset.UsageBytes != 0 || reset.CycleRXBytes != 0 || reset.CycleTXBytes != 0 || reset.CycleDirBasis != "exact" || reset.CycleDirSince != nil {
		t.Fatalf("policy change: %+v %v", reset, err)
	}

	// The next cycle starts clean, whatever the basis was.
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_plans SET cycle_dir_basis='since',cycle_dir_since=1,cycle_rx_bytes=3,cycle_tx_bytes=4,usage_bytes=9 WHERE agent_id=?`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", 5, 260, 160), time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC)); err != nil || !accepted {
		t.Fatal(err)
	}
	got, _, _ = store.GetAgentPlan(ctx, agentID)
	if got.UsageBytes != 0 || got.CycleRXBytes != 0 || got.CycleTXBytes != 0 || got.CycleDirBasis != "exact" || got.CycleDirSince != nil {
		t.Fatalf("new cycle: %+v", got)
	}
	// The read projection of an unobserved new cycle is clean too.
	view, err := AgentPlanAt(AgentPlan{TrafficMode: "sum", CycleKind: "monthly", CycleCount: 1, CycleAnchor: "2024-01-01", Timezone: "UTC",
		UsageBytes: 10, CycleRXBytes: 6, CycleTXBytes: 4, CycleDirBasis: "backfill", CycleDirSince: new(int64),
		CycleStart: new(int64), CycleEnd: func() *int64 { v := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli(); return &v }()}, time.Date(2024, 2, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || view.CycleRXBytes != 0 || view.CycleTXBytes != 0 || view.CycleDirBasis != "exact" || view.CycleDirSince != nil || view.UsageBytes != 0 {
		t.Fatalf("projection of a new cycle: %+v %v", view, err)
	}
}

func TestPlanDirectionOverflowAbortsTheReport(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	feb10 := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)
	if _, err := store.PutAgentPlan(ctx, directionPlan(agentID, "tx"), nil, feb10); err != nil {
		t.Fatal(err)
	}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", 1, 10, 10), feb10.Add(time.Second)); err != nil || !accepted {
		t.Fatal(err)
	}
	// The unbilled direction cannot wrap or exceed the storage range either.
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_plans SET cycle_rx_bytes=? WHERE agent_id=?`, int64(math.MaxInt64-5), agentID); err != nil {
		t.Fatal(err)
	}
	if _, accepted, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", 2, 100, 20), feb10.Add(2*time.Second)); err == nil || accepted {
		t.Fatalf("overflowing direction counter accepted=%t err=%v", accepted, err)
	}
	got, _, _ := store.GetAgentPlan(ctx, agentID)
	if got.UsageBytes != 0 || got.CycleRXBytes != math.MaxInt64-5 {
		t.Fatalf("a failed report changed the plan: %+v", got)
	}
}

// Random report sequences (growth, reboot, duplicate, clock rollback, cycle change, calibration, edits): after
// every step the invariant of design 2.3 holds for each billing mode, and the billed usage is the very number
// the v1.0.6 accounting produces.
func TestPlanDirectionPropertyInvariantAndUnchangedUsage(t *testing.T) {
	for _, mode := range []string{"rx", "tx", "sum"} {
		for seed := int64(1); seed <= 12; seed++ {
			t.Run(fmt.Sprintf("%s/seed%d", mode, seed), func(t *testing.T) {
				store, agentID, _ := testStore(t, ":memory:")
				defer store.Close()
				ctx := context.Background()
				rng := rand.New(rand.NewSource(seed*7919 + int64(len(mode))))
				now := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)
				plan := directionPlan(agentID, mode)
				if _, err := store.PutAgentPlan(ctx, plan, nil, now); err != nil {
					t.Fatal(err)
				}
				var epoch, seq, rx, tx uint64 = 1, 0, 1000, 1000
				session, boot := "session-1", "boot-1"
				var last *struct {
					epoch uint64
					seq   uint64
					rx    uint64
					tx    uint64
				}
				for step := 0; step < 70; step++ {
					before, _, err := store.GetAgentPlan(ctx, agentID)
					if err != nil {
						t.Fatal(err)
					}
					states, err := store.ListStates(ctx)
					if err != nil {
						t.Fatal(err)
					}
					var previous State
					existed := len(states) == 1
					if existed {
						previous = states[0]
					}
					kind := rng.Intn(10)
					received := now
					accepted := true
					switch kind {
					case 0, 1, 2, 3: // growth
						rx += uint64(rng.Intn(5000))
						tx += uint64(rng.Intn(5000))
						now = now.Add(time.Duration(rng.Intn(90)+1) * time.Second)
						received = now
						seq++
					case 4: // the Agent restarted: new epoch, counters start low
						epoch++
						session, boot = fmt.Sprintf("session-%d", epoch), fmt.Sprintf("boot-%d", epoch)
						rx, tx, seq = uint64(rng.Intn(200)), uint64(rng.Intn(200)), 1
						now = now.Add(time.Minute)
						received = now
					case 5: // clock rollback into an earlier cycle
						rx += uint64(rng.Intn(3000))
						tx += uint64(rng.Intn(3000))
						seq++
						received = now.AddDate(0, 0, -(rng.Intn(25) + 1))
					case 6: // the first report of a later cycle
						rx += uint64(rng.Intn(3000))
						tx += uint64(rng.Intn(3000))
						seq++
						now = time.Date(now.Year(), now.Month()+1, 1, 0, rng.Intn(30), 0, 0, time.UTC)
						received = now
					case 7: // a duplicate of the previous report
						if last == nil {
							continue
						}
						received = now.Add(time.Second)
						if _, ok, _, err := store.ProcessReport(ctx, agentID, validReport(agentID, last.epoch, session, boot, last.seq, last.rx, last.tx), received); err != nil || ok {
							t.Fatalf("duplicate accepted=%t err=%v", ok, err)
						}
						accepted = false
					case 8: // calibration or a harmless edit
						edited := plan
						edited.PurchasePrice = fmt.Sprint(rng.Intn(100))
						var calibration *uint64
						if rng.Intn(2) == 0 {
							value := uint64(rng.Intn(1 << 20))
							calibration = &value
						}
						now = now.Add(time.Second)
						if _, err := store.PutAgentPlan(ctx, edited, calibration, now); err != nil {
							t.Fatal(err)
						}
						got, _, _ := store.GetAgentPlan(ctx, agentID)
						assertDirectionInvariant(t, got, fmt.Sprintf("step %d edit", step))
						continue
					case 9: // the billing mode changes: a policy change
						other := []string{"rx", "tx", "sum"}[rng.Intn(3)]
						plan.TrafficMode = other
						now = now.Add(time.Second)
						if _, err := store.PutAgentPlan(ctx, plan, nil, now); err != nil {
							t.Fatal(err)
						}
						got, _, _ := store.GetAgentPlan(ctx, agentID)
						assertDirectionInvariant(t, got, fmt.Sprintf("step %d mode change", step))
						continue
					}
					if accepted {
						report := validReport(agentID, epoch, session, boot, seq, rx, tx)
						state, ok, _, err := store.ProcessReport(ctx, agentID, report, received)
						if err != nil {
							t.Fatalf("step %d: %v", step, err)
						}
						if !ok {
							continue // rejected as stale: nothing is accounted
						}
						last = &struct {
							epoch uint64
							seq   uint64
							rx    uint64
							tx    uint64
						}{epoch, seq, rx, tx}
						got, _, _ := store.GetAgentPlan(ctx, agentID)
						assertDirectionInvariant(t, got, fmt.Sprintf("step %d kind %d", step, kind))
						if want := legacyUsageAfterReport(before, previous, state, existed, received); got.UsageBytes != want {
							t.Fatalf("step %d kind %d: usage %d, the v1.0.6 accounting gives %d", step, kind, got.UsageBytes, want)
						}
						continue
					}
					got, _, _ := store.GetAgentPlan(ctx, agentID)
					if got.UsageBytes != before.UsageBytes || got.CycleRXBytes != before.CycleRXBytes || got.CycleTXBytes != before.CycleTXBytes {
						t.Fatalf("step %d: a rejected report changed the plan: %+v -> %+v", step, before, got)
					}
				}
			})
		}
	}
}
