package storage

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestMaterializeScheduleBoundaryCoalescingAndSnapshot(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	created := time.Unix(1_000, 0)
	params := scheduleParams("schedule", agentID, created)
	params.Config = protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com/private", Method: "HEAD"}}
	schedule, _, err := store.PutProbeSchedule(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.DueScheduleIDs(ctx, created.Add(-time.Millisecond), 64)
	if err != nil || len(before) != 0 {
		t.Fatalf("before boundary ids=%v err=%v", before, err)
	}
	at := created.Add(3*time.Minute + 15*time.Second)
	ids, err := store.DueScheduleIDs(ctx, at, 64)
	if err != nil || !reflect.DeepEqual(ids, []string{"schedule"}) {
		t.Fatalf("due ids=%v err=%v", ids, err)
	}
	outcome, err := store.MaterializeSchedule(ctx, schedule.ID, at, 90*time.Second)
	if err != nil || outcome.Result != MaterializeCreated {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	wantSlot := created.Add(3 * time.Minute).UnixMilli()
	if outcome.Slot != wantSlot || outcome.JobID != ScheduledJobID(schedule.ID, wantSlot) {
		t.Fatalf("outcome=%+v", outcome)
	}
	job, err := store.GetProbeJob(ctx, outcome.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.ScheduleID != schedule.ID || job.ScheduledFor != wantSlot || job.CreatedAt != at.UnixMilli() ||
		job.NotBefore != at.UnixMilli() || job.ExpiresAt != at.Add(90*time.Second).UnixMilli() ||
		job.Status != JobStatusQueued || job.Attempt != 0 || !reflect.DeepEqual(job.Config, params.Config) {
		t.Fatalf("job=%+v", job)
	}
	after, err := store.GetProbeSchedule(ctx, schedule.ID)
	if err != nil || after.NextRunAt != created.Add(4*time.Minute).UnixMilli() || after.UpdatedAt != schedule.UpdatedAt {
		t.Fatalf("schedule=%+v err=%v", after, err)
	}

	// Simulate replay after an ACK/commit ambiguity. The existing slot bypasses
	// capacity and advances without creating another job.
	if _, err := store.db.ExecContext(ctx, `UPDATE probe_schedules SET next_run_at=? WHERE id=?`, schedule.NextRunAt, schedule.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := store.MaterializeSchedule(ctx, schedule.ID, at, 90*time.Second)
	if err != nil || replay.Result != MaterializeAlreadyMaterialized || replay.JobID != job.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_jobs WHERE schedule_id=? AND scheduled_for=?`, schedule.ID, wantSlot).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestMaterializeScheduleBackpressureAndExpiredCleanup(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Unix(2_000, 0)
	params := scheduleParams("schedule", agentID, at)
	if _, _, err := store.PutProbeSchedule(ctx, params); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxOutstandingJobsPerAgent; index++ {
		job := oneShot("capacity-"+time.Unix(int64(index), 0).Format("150405.000"), agentID, at, protocol.ProbeTypeTCPConnect)
		if err := store.CreateOneShotJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	outcome, err := store.MaterializeSchedule(ctx, "schedule", at, time.Minute)
	if err != nil || outcome.Result != MaterializeBackpressured {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	schedule, _ := store.GetProbeSchedule(ctx, "schedule")
	if schedule.NextRunAt != at.Add(time.Minute).UnixMilli() {
		t.Fatalf("schedule=%+v", schedule)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE probe_jobs SET expires_at=? WHERE id=(SELECT id FROM probe_jobs LIMIT 1)`, at.Add(30*time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	outcome, err = store.MaterializeSchedule(ctx, "schedule", at.Add(time.Minute), time.Minute)
	if err != nil || outcome.Result != MaterializeCreated {
		t.Fatalf("cleanup outcome=%+v err=%v", outcome, err)
	}
}

func TestMaterializeScheduleRevokedCorruptAndTimeRange(t *testing.T) {
	ctx := context.Background()
	t.Run("revoked", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		at := time.Unix(3_000, 0)
		original, _, _ := store.PutProbeSchedule(ctx, scheduleParams("schedule", agentID, at))
		if changed, err := store.RevokeAgent(ctx, agentID, at); err != nil || !changed {
			t.Fatal(err)
		}
		outcome, err := store.MaterializeSchedule(ctx, "schedule", at, time.Minute)
		if err != nil || outcome.Result != MaterializeAgentRevoked {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
		after, _ := store.GetProbeSchedule(ctx, "schedule")
		if after.Enabled || after.NextRunAt != original.NextRunAt || after.UpdatedAt != at.UnixMilli() {
			t.Fatalf("after=%+v", after)
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		at := time.Unix(4_000, 0)
		_, _, _ = store.PutProbeSchedule(ctx, scheduleParams("schedule", agentID, at))
		_, _ = store.db.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`)
		_, _ = store.db.ExecContext(ctx, `UPDATE probe_schedules SET config_json='{}' WHERE id='schedule'`)
		outcome, err := store.MaterializeSchedule(ctx, "schedule", at, time.Minute)
		if outcome.Result != MaterializeCorruptDisabled || !errors.Is(err, ErrCorruptProbeData) {
			t.Fatalf("outcome=%+v err=%v", outcome, err)
		}
		var enabled int
		if err := store.db.QueryRowContext(ctx, `SELECT enabled FROM probe_schedules WHERE id='schedule'`).Scan(&enabled); err != nil || enabled != 0 {
			t.Fatalf("enabled=%d err=%v", enabled, err)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		at := time.UnixMilli(math.MaxInt64 - int64(time.Minute/time.Millisecond) + 1)
		params := scheduleParams("schedule", agentID, at)
		_, _, _ = store.PutProbeSchedule(ctx, params)
		before, _ := store.GetProbeSchedule(ctx, "schedule")
		if _, err := store.MaterializeSchedule(ctx, "schedule", at, time.Minute); !errors.Is(err, ErrScheduleTimeRange) {
			t.Fatalf("error=%v", err)
		}
		after, _ := store.GetProbeSchedule(ctx, "schedule")
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("before=%+v after=%+v", before, after)
		}
	})
}

func TestMaterializeScheduleConcurrentSingleJob(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Unix(5_000, 0)
	_, _, _ = store.PutProbeSchedule(ctx, scheduleParams("schedule", agentID, at))
	var wg sync.WaitGroup
	results := make(chan MaterializeOutcome, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome, err := store.MaterializeSchedule(ctx, "schedule", at, time.Minute)
			results <- outcome
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	created := 0
	for outcome := range results {
		if outcome.Result == MaterializeCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
	var count int
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_jobs WHERE schedule_id='schedule'`).Scan(&count)
	if count != 1 {
		t.Fatalf("jobs=%d", count)
	}
}

func TestMaterializeScheduleRejectsDeterministicIDCollision(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Unix(8_000, 0)
	created, _, err := store.PutProbeSchedule(ctx, scheduleParams("schedule", agentID, at))
	if err != nil {
		t.Fatal(err)
	}
	collision := oneShot(ScheduledJobID(created.ID, at.UnixMilli()), agentID, at, protocol.ProbeTypeTCPConnect)
	if err := store.CreateOneShotJob(ctx, collision); err != nil {
		t.Fatal(err)
	}
	outcome, err := store.MaterializeSchedule(ctx, created.ID, at, time.Minute)
	if outcome.Result != "" || !errors.Is(err, ErrCorruptProbeData) {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	after, err := store.GetProbeSchedule(ctx, created.ID)
	if err != nil || after.NextRunAt != created.NextRunAt {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	var scheduled int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM probe_jobs WHERE schedule_id=?`, created.ID).Scan(&scheduled); err != nil || scheduled != 0 {
		t.Fatalf("scheduled=%d err=%v", scheduled, err)
	}
}
