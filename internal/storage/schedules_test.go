package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

func scheduleParams(id, agentID string, now time.Time) PutScheduleParams {
	return PutScheduleParams{
		ID: id, AgentID: agentID, Name: "homepage", ProbeType: protocol.ProbeTypeHTTP,
		Config:    protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com/health", Method: "GET"}},
		TimeoutMS: 5000, IntervalSeconds: 60, Enabled: true, Now: now.UnixMilli(),
	}
}

func TestPutProbeScheduleLifecycle(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	createdAt := time.Unix(1000, 0)
	params := scheduleParams("schedule", agentID, createdAt)
	created, wasCreated, err := store.PutProbeSchedule(ctx, params)
	if err != nil || !wasCreated {
		t.Fatalf("create=%+v created=%t err=%v", created, wasCreated, err)
	}
	if created.CreatedAt != params.Now || created.UpdatedAt != params.Now || created.NextRunAt != params.Now || !created.Enabled {
		t.Fatalf("created timestamps=%+v", created)
	}

	replayParams := params
	replayParams.Now = createdAt.Add(time.Minute).UnixMilli()
	replayed, wasCreated, err := store.PutProbeSchedule(ctx, replayParams)
	if err != nil || wasCreated || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("replay=%+v created=%t err=%v", replayed, wasCreated, err)
	}

	nameUpdate := replayParams
	nameUpdate.Name = "homepage renamed"
	nameUpdate.Now = createdAt.Add(2 * time.Minute).UnixMilli()
	renamed, wasCreated, err := store.PutProbeSchedule(ctx, nameUpdate)
	if err != nil || wasCreated || renamed.UpdatedAt != nameUpdate.Now || renamed.NextRunAt != created.NextRunAt {
		t.Fatalf("rename=%+v created=%t err=%v", renamed, wasCreated, err)
	}

	disabledParams := nameUpdate
	disabledParams.Enabled = false
	disabledParams.Now = createdAt.Add(3 * time.Minute).UnixMilli()
	disabled, _, err := store.PutProbeSchedule(ctx, disabledParams)
	if err != nil || disabled.Enabled || disabled.NextRunAt != created.NextRunAt || disabled.UpdatedAt != disabledParams.Now {
		t.Fatalf("disable=%+v err=%v", disabled, err)
	}
	if revoked, err := store.RevokeAgent(ctx, agentID, createdAt.Add(4*time.Minute)); err != nil || !revoked {
		t.Fatalf("revoke=%t err=%v", revoked, err)
	}
	revokedReplay := disabledParams
	revokedReplay.Now = createdAt.Add(5 * time.Minute).UnixMilli()
	got, _, err := store.PutProbeSchedule(ctx, revokedReplay)
	if err != nil || !reflect.DeepEqual(got, disabled) {
		t.Fatalf("revoked replay=%+v err=%v", got, err)
	}
	reenable := revokedReplay
	reenable.Enabled = true
	if _, _, err := store.PutProbeSchedule(ctx, reenable); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("re-enable error=%v", err)
	}
	if err := store.DeleteProbeSchedule(ctx, params.ID); err != nil {
		t.Fatalf("delete revoked schedule: %v", err)
	}
	if _, err := store.GetProbeSchedule(ctx, params.ID); !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("deleted read error=%v", err)
	}
}

func TestPutProbeScheduleUpdateAndAgentRules(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	_, otherHash, _ := auth.NewToken()
	otherID, _ := auth.NewID()
	if err := store.AddAgent(ctx, otherID, "other", otherHash, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1000, 0)
	params := scheduleParams("schedule", agentID, at)
	initial, _, err := store.PutProbeSchedule(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	conflict := params
	conflict.AgentID = otherID
	if _, _, err := store.PutProbeSchedule(ctx, conflict); !errors.Is(err, ErrScheduleConflict) {
		t.Fatalf("agent conflict error=%v", err)
	}
	missing := scheduleParams("missing", "missing-agent", at)
	if _, _, err := store.PutProbeSchedule(ctx, missing); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("missing agent error=%v", err)
	}

	changed := params
	changed.IntervalSeconds = 120
	changed.Now = at.Add(time.Minute).UnixMilli()
	updated, _, err := store.PutProbeSchedule(ctx, changed)
	if err != nil || updated.NextRunAt != changed.Now || updated.UpdatedAt != changed.Now {
		t.Fatalf("enabled operational update=%+v err=%v initial=%+v", updated, err, initial)
	}
	changed.Config = protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "127.0.0.1", Port: 443}}
	changed.ProbeType = protocol.ProbeTypeTCPConnect
	changed.Now = at.Add(2 * time.Minute).UnixMilli()
	updated, _, err = store.PutProbeSchedule(ctx, changed)
	if err != nil || updated.ProbeType != protocol.ProbeTypeTCPConnect || updated.NextRunAt != changed.Now {
		t.Fatalf("typed update=%+v err=%v", updated, err)
	}
}

func TestPutProbeScheduleLimitAndConcurrency(t *testing.T) {
	t.Run("identical ID", func(t *testing.T) {
		ctx := context.Background()
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		params := scheduleParams("concurrent", agentID, time.Unix(1000, 0))
		const workers = 24
		created := 0
		errorsFound := make(chan error, workers)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, wasCreated, err := store.PutProbeSchedule(ctx, params)
				if err != nil {
					errorsFound <- err
					return
				}
				if wasCreated {
					mu.Lock()
					created++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		close(errorsFound)
		for err := range errorsFound {
			t.Fatal(err)
		}
		if created != 1 {
			t.Fatalf("created count=%d", created)
		}
	})

	t.Run("distinct IDs compete for capacity", func(t *testing.T) {
		ctx := context.Background()
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		at := time.Unix(1000, 0)
		const extra = 16
		type outcome struct {
			id      string
			created bool
			err     error
		}
		outcomes := make(chan outcome, MaxSchedulesPerAgent+extra)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for index := range MaxSchedulesPerAgent + extra {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				id := fmt.Sprintf("schedule-%03d", index)
				params := scheduleParams(id, agentID, at)
				params.Enabled = index%2 == 0
				_, created, err := store.PutProbeSchedule(ctx, params)
				outcomes <- outcome{id: id, created: created, err: err}
			}(index)
		}
		close(start)
		wg.Wait()
		close(outcomes)
		createdIDs := make([]string, 0, MaxSchedulesPerAgent)
		limits := 0
		for result := range outcomes {
			switch {
			case result.err == nil && result.created:
				createdIDs = append(createdIDs, result.id)
			case errors.Is(result.err, ErrScheduleLimit) && !result.created:
				limits++
			default:
				t.Fatalf("unexpected outcome=%+v", result)
			}
		}
		if len(createdIDs) != MaxSchedulesPerAgent || limits != extra {
			t.Fatalf("created=%d limits=%d", len(createdIDs), limits)
		}
		var total, disabled int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(CASE WHEN enabled=0 THEN 1 ELSE 0 END) FROM probe_schedules WHERE agent_id=?`, agentID).Scan(&total, &disabled); err != nil {
			t.Fatal(err)
		}
		if total != MaxSchedulesPerAgent || disabled == 0 {
			t.Fatalf("total=%d disabled=%d", total, disabled)
		}
		replay, err := store.GetProbeSchedule(ctx, createdIDs[0])
		if err != nil {
			t.Fatal(err)
		}
		replayParams := PutScheduleParams{
			ID: replay.ID, AgentID: replay.AgentID, Name: replay.Name, ProbeType: replay.ProbeType, Config: replay.Config,
			TimeoutMS: replay.TimeoutMS, IntervalSeconds: replay.IntervalSeconds, Enabled: replay.Enabled, Now: at.Add(time.Minute).UnixMilli(),
		}
		if _, created, err := store.PutProbeSchedule(ctx, replayParams); err != nil || created {
			t.Fatalf("replay at limit created=%t err=%v", created, err)
		}
		if err := store.DeleteProbeSchedule(ctx, createdIDs[0]); err != nil {
			t.Fatal(err)
		}
		if _, created, err := store.PutProbeSchedule(ctx, scheduleParams("replacement", agentID, at)); err != nil || !created {
			t.Fatalf("replacement created=%t err=%v", created, err)
		}
	})
}

func TestGetProbeScheduleRejectsCorruption(t *testing.T) {
	tests := []struct {
		name      string
		statement string
	}{
		{name: "probe type", statement: `UPDATE probe_schedules SET probe_type='shell' WHERE id='schedule'`},
		{name: "config", statement: `UPDATE probe_schedules SET config_json='{}' WHERE id='schedule'`},
		{name: "interval", statement: `UPDATE probe_schedules SET interval_seconds=1 WHERE id='schedule'`},
		{name: "enabled", statement: `UPDATE probe_schedules SET enabled=2 WHERE id='schedule'`},
		{name: "created", statement: `UPDATE probe_schedules SET created_at=0 WHERE id='schedule'`},
		{name: "updated", statement: `UPDATE probe_schedules SET updated_at=1 WHERE id='schedule'`},
		{name: "next run", statement: `UPDATE probe_schedules SET next_run_at=1 WHERE id='schedule'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, agentID, _ := testStore(t, ":memory:")
			defer store.Close()
			if _, _, err := store.PutProbeSchedule(ctx, scheduleParams("schedule", agentID, time.Unix(1000, 0))); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, test.statement); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetProbeSchedule(ctx, "schedule"); !errors.Is(err, ErrCorruptProbeData) {
				t.Fatalf("corruption error=%v", err)
			}
		})
	}
}

func TestDeleteProbeSchedulePreservesJobsAndResults(t *testing.T) {
	for _, state := range []JobStatus{JobStatusQueued, JobStatusLeased, JobStatusFinished} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			store, agentID, _ := testStore(t, ":memory:")
			defer store.Close()
			at := time.Unix(1000, 0)
			if _, _, err := store.PutProbeSchedule(ctx, scheduleParams("schedule", agentID, at)); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE probe_jobs SET schedule_id='schedule' WHERE id='job'`); err != nil {
				t.Fatal(err)
			}
			if state != JobStatusQueued {
				job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypeTCPConnect), at, time.Minute)
				if err != nil || job == nil {
					t.Fatalf("claim=%+v err=%v", job, err)
				}
				if state == JobStatusFinished {
					if _, err := store.SubmitJobResult(ctx, agentID, "job", resultFor(job, true), at.Add(time.Second)); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, beforeResult, err := store.GetProbeJobSnapshot(ctx, "job", at)
			if err != nil {
				t.Fatal(err)
			}
			if before.Status != state || before.ScheduleID != "schedule" {
				t.Fatalf("before=%+v", before)
			}
			if err := store.DeleteProbeSchedule(ctx, "schedule"); err != nil {
				t.Fatal(err)
			}
			after, afterResult, err := store.GetProbeJobSnapshot(ctx, "job", at)
			if err != nil {
				t.Fatal(err)
			}
			before.ScheduleID = ""
			if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterResult, beforeResult) {
				t.Fatalf("before=%+v result=%+v after=%+v result=%+v", before, beforeResult, after, afterResult)
			}
		})
	}
}
