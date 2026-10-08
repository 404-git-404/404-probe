package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestCleanupScheduledProbeJobsHonorsRetentionBoundaryAndScope(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()

	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scheduleID := "retention-schedule"
	if _, _, err := store.PutProbeSchedule(ctx, scheduleParams(scheduleID, agentID, cutoff.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	cutoffMS := cutoff.UnixMilli()
	notBefore := cutoff.Add(-48 * time.Hour).UnixMilli()
	createdAt := cutoff.Add(-60 * 24 * time.Hour).UnixMilli()
	tests := []struct {
		id           string
		scheduleID   string
		status       JobStatus
		expiresAt    int64
		finishedAt   any
		scheduledFor int64
		wantRetained bool
		withResult   bool
	}{
		{id: "scheduled-finished-before", scheduleID: scheduleID, status: JobStatusFinished, expiresAt: cutoff.Add(time.Hour).UnixMilli(), finishedAt: cutoffMS - 1, scheduledFor: 1, withResult: true},
		{id: "scheduled-finished-boundary", scheduleID: scheduleID, status: JobStatusFinished, expiresAt: cutoff.Add(time.Hour).UnixMilli(), finishedAt: cutoffMS, scheduledFor: 2, wantRetained: true},
		{id: "scheduled-finished-after", scheduleID: scheduleID, status: JobStatusFinished, expiresAt: cutoff.Add(time.Hour).UnixMilli(), finishedAt: cutoffMS + 1, scheduledFor: 3, wantRetained: true},
		{id: "scheduled-expired-before", scheduleID: scheduleID, status: JobStatusExpired, expiresAt: cutoffMS - 1, scheduledFor: 4},
		{id: "scheduled-expired-boundary", scheduleID: scheduleID, status: JobStatusExpired, expiresAt: cutoffMS, scheduledFor: 5, wantRetained: true},
		{id: "scheduled-expired-after", scheduleID: scheduleID, status: JobStatusExpired, expiresAt: cutoffMS + 1, scheduledFor: 6, wantRetained: true},
		{id: "scheduled-queued-before", scheduleID: scheduleID, status: JobStatusQueued, expiresAt: cutoffMS - 1, scheduledFor: 7, wantRetained: true},
		{id: "scheduled-leased-before", scheduleID: scheduleID, status: JobStatusLeased, expiresAt: cutoffMS - 1, scheduledFor: 8, wantRetained: true},
		{id: "manual-finished-before", status: JobStatusFinished, expiresAt: cutoff.Add(time.Hour).UnixMilli(), finishedAt: cutoffMS - 1, scheduledFor: 9, wantRetained: true},
		{id: "manual-expired-before", status: JobStatusExpired, expiresAt: cutoffMS - 1, scheduledFor: 10, wantRetained: true},
	}
	for _, item := range tests {
		insertScheduledRetentionJob(t, store, agentID, item.id, item.scheduleID, item.status, createdAt, item.scheduledFor, notBefore, item.expiresAt, item.finishedAt)
		if item.withResult {
			insertScheduledRetentionResult(t, store, item.id, cutoffMS-1)
		}
	}
	insertScheduledRetentionResult(t, store, "scheduled-finished-boundary", cutoffMS)

	deleted, more, err := store.CleanupScheduledProbeJobsBatch(ctx, cutoff)
	if err != nil || deleted != 2 || more {
		t.Fatalf("deleted=%d more=%t err=%v, want 2 deleted and no full batch", deleted, more, err)
	}
	for _, item := range tests {
		wantExists := item.wantRetained
		if got := retentionRowExists(t, store, "probe_jobs", item.id); got != wantExists {
			t.Errorf("job %q exists=%t, want %t", item.id, got, wantExists)
		}
	}
	if got := retentionRowExists(t, store, "probe_results", "scheduled-finished-before"); got {
		t.Fatal("probe_result for deleted scheduled job survived cascade")
	}
	if got := retentionRowExists(t, store, "probe_results", "scheduled-finished-boundary"); !got {
		t.Fatal("probe_result for boundary-retained job was deleted")
	}
}

func TestCleanupScheduledProbeJobsBatchCapsAndConverges(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scheduleID := "bulk-retention-schedule"
	if _, _, err := store.PutProbeSchedule(ctx, scheduleParams(scheduleID, agentID, cutoff.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	insertRetentionBatchFixture(t, store, agentID, scheduleID, cutoff, 620, 580)

	deleted, more, err := store.CleanupScheduledProbeJobsBatch(ctx, cutoff)
	if err != nil || deleted != 1000 || !more {
		t.Fatalf("first batch deleted=%d more=%t err=%v, want 1000 and more", deleted, more, err)
	}
	if finished, expired := countScheduledRetentionCandidates(t, store); finished != 120 || expired != 80 {
		t.Fatalf("after first batch finished=%d expired=%d, want 120 and 80", finished, expired)
	}
	deleted, more, err = store.CleanupScheduledProbeJobsBatch(ctx, cutoff)
	if err != nil || deleted != 200 || more {
		t.Fatalf("second batch deleted=%d more=%t err=%v, want 200 and exhausted", deleted, more, err)
	}
	deleted, more, err = store.CleanupScheduledProbeJobsBatch(ctx, cutoff)
	if err != nil || deleted != 0 || more {
		t.Fatalf("converged batch deleted=%d more=%t err=%v, want zero and exhausted", deleted, more, err)
	}
}

func TestCleanupScheduledProbeJobsSurvivesScheduleDeletionAndUsesStableOrigin(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	createdAt := cutoff.Add(-10 * 24 * time.Hour)
	scheduleID := "deleted-retention-schedule"
	if _, _, err := store.PutProbeSchedule(ctx, scheduleParams(scheduleID, agentID, createdAt)); err != nil {
		t.Fatal(err)
	}
	outcome, err := store.MaterializeSchedule(ctx, scheduleID, createdAt, time.Minute)
	if err != nil || outcome.Result != MaterializeCreated {
		t.Fatalf("materialize outcome=%+v err=%v", outcome, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE probe_jobs SET status='finished',finished_at=? WHERE id=?`, cutoff.UnixMilli()-1, outcome.JobID); err != nil {
		t.Fatal(err)
	}
	insertScheduledRetentionResult(t, store, outcome.JobID, cutoff.UnixMilli()-1)
	if err := store.DeleteProbeSchedule(ctx, scheduleID); err != nil {
		t.Fatal(err)
	}
	var detachedScheduleID sql.NullString
	var origin string
	if err := store.db.QueryRowContext(ctx, `SELECT schedule_id,origin FROM probe_jobs WHERE id=?`, outcome.JobID).Scan(&detachedScheduleID, &origin); err != nil {
		t.Fatal(err)
	}
	if detachedScheduleID.Valid || origin != "scheduled" {
		t.Fatalf("deleted schedule job schedule_id=%+v origin=%q", detachedScheduleID, origin)
	}

	manual := oneShot("manual-origin-after-detach", agentID, createdAt, protocol.ProbeTypeTCPConnect)
	if err := store.CreateOneShotJob(ctx, manual); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE probe_jobs SET scheduled_for=scheduled_for+1,status='finished',finished_at=? WHERE id=?`, cutoff.UnixMilli()-1, manual.ID); err != nil {
		t.Fatal(err)
	}
	var manualOrigin string
	if err := store.db.QueryRowContext(ctx, `SELECT origin FROM probe_jobs WHERE id=?`, manual.ID).Scan(&manualOrigin); err != nil || manualOrigin != "manual" {
		t.Fatalf("manual origin=%q err=%v", manualOrigin, err)
	}

	deleted, more, err := store.CleanupScheduledProbeJobsBatch(ctx, cutoff)
	if err != nil || deleted != 1 || more {
		t.Fatalf("scheduled cleanup deleted=%d more=%t err=%v", deleted, more, err)
	}
	if retentionRowExists(t, store, "probe_jobs", outcome.JobID) || retentionRowExists(t, store, "probe_results", outcome.JobID) {
		t.Fatal("detached scheduled job or its result survived cleanup")
	}
	deleted, more, err = store.CleanupScheduledProbeJobsBatch(ctx, cutoff)
	if err != nil || deleted != 0 || more || !retentionRowExists(t, store, "probe_jobs", manual.ID) {
		t.Fatalf("manual cleanup deleted=%d more=%t err=%v, manual job retained=%t", deleted, more, err, retentionRowExists(t, store, "probe_jobs", manual.ID))
	}
}

func TestMigration21BackfillsStableProbeJobOrigin(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "origin-migration.db")
	store, agentID, _ := testStore(t, path)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scheduleID := "origin-migration-schedule"
	if _, _, err := store.PutProbeSchedule(ctx, scheduleParams(scheduleID, agentID, base)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	createdAt := base.UnixMilli()
	insertScheduledRetentionJob(t, store, agentID, "migration-current-schedule", scheduleID, JobStatusQueued, createdAt, createdAt, createdAt, createdAt+1000, nil)
	insertScheduledRetentionJob(t, store, agentID, "migration-detached-schedule", "", JobStatusQueued, createdAt, createdAt+1, createdAt, createdAt+1000, nil)
	insertScheduledRetentionJob(t, store, agentID, "migration-manual", "", JobStatusQueued, createdAt, createdAt, createdAt, createdAt+1000, nil)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	dropQualityFixtureSchema(t, db)
	for _, statement := range []string{
		`ALTER TABLE agent_state DROP COLUMN network_counters_json`,
		`ALTER TABLE agent_state DROP COLUMN network_counters_version`,
		`DELETE FROM schema_migrations WHERE version=22`,
		`DROP INDEX idx_probe_jobs_scheduled_finished_retention`,
		`DROP INDEX idx_probe_jobs_scheduled_expired_retention`,
		`ALTER TABLE probe_jobs DROP COLUMN origin`,
		`DELETE FROM schema_migrations WHERE version=21`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			t.Fatalf("prepare schema v20 fixture with %q: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for id, want := range map[string]string{
		"migration-current-schedule":  "scheduled",
		"migration-detached-schedule": "scheduled",
		"migration-manual":            "manual",
	} {
		var got string
		if err := store.db.QueryRowContext(ctx, `SELECT origin FROM probe_jobs WHERE id=?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("job %q origin=%q err=%v, want %q", id, got, err, want)
		}
	}
}

func TestScheduledProbeJobRetentionIndexesExistAndAreUsed(t *testing.T) {
	ctx := context.Background()
	store, _, _ := testStore(t, ":memory:")
	defer store.Close()
	cutoff := time.Now().UnixMilli()
	checks := []struct {
		index string
		query string
	}{
		{
			index: "idx_probe_jobs_scheduled_finished_retention",
			query: selectFinishedScheduledProbeJobsBatch,
		},
		{
			index: "idx_probe_jobs_scheduled_expired_retention",
			query: selectExpiredScheduledProbeJobsBatch,
		},
	}
	for _, check := range checks {
		t.Run(check.index, func(t *testing.T) {
			var count int
			if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, check.index).Scan(&count); err != nil || count != 1 {
				t.Fatalf("index count=%d err=%v", count, err)
			}
			rows, err := store.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+check.query, cutoff, scheduledProbeJobRetentionBatchSize)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var details []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(details, "\n"), check.index) {
				t.Fatalf("query plan does not use %s: %s", check.index, strings.Join(details, "\n"))
			}
		})
	}
}

func insertScheduledRetentionJob(t *testing.T, store *Store, agentID, id, scheduleID string, status JobStatus, createdAt, scheduledFor, notBefore, expiresAt int64, finishedAt any) {
	t.Helper()
	var scheduleValue any
	origin := "manual"
	if scheduleID != "" {
		scheduleValue = scheduleID
		origin = "scheduled"
	}
	if _, err := store.db.Exec(`INSERT INTO probe_jobs(
		id,origin,schedule_id,agent_id,probe_type,config_json,timeout_ms,created_at,scheduled_for,not_before,expires_at,status,attempt,finished_at
	) VALUES(?,?,?,?,?,?,100,?,?,?,?,?,0,?)`, id, origin, scheduleValue, agentID, "http", "{}", createdAt, scheduledFor, notBefore, expiresAt, string(status), finishedAt); err != nil {
		t.Fatalf("insert probe job %s: %v", id, err)
	}
}

func insertScheduledRetentionResult(t *testing.T, store *Store, jobID string, at int64) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO probe_results(
		job_id,attempt,received_at,agent_epoch,session_id,agent_started_at,agent_finished_at,duration_ms,success,payload_json,result_hash
	) VALUES(?,1,?,1,'retention-session',?,?,0,1,'{}',zeroblob(32))`, jobID, at, at, at); err != nil {
		t.Fatalf("insert probe result %s: %v", jobID, err)
	}
}

func insertRetentionBatchFixture(t *testing.T, store *Store, agentID, scheduleID string, cutoff time.Time, finishedCount, expiredCount int) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO probe_jobs(
		id,origin,schedule_id,agent_id,probe_type,config_json,timeout_ms,created_at,scheduled_for,not_before,expires_at,status,attempt,finished_at
	) VALUES(?,'scheduled',?,?,?,?,100,?,?,?,?,?,0,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	cutoffMS := cutoff.UnixMilli()
	notBefore := cutoff.Add(-48 * time.Hour).UnixMilli()
	createdAt := cutoff.Add(-60 * 24 * time.Hour).UnixMilli()
	insert := func(id string, status JobStatus, scheduledFor, expiresAt int64, finishedAt any) {
		t.Helper()
		if _, err := statement.ExecContext(ctx, id, scheduleID, agentID, "http", "{}", createdAt, scheduledFor, notBefore, expiresAt, string(status), finishedAt); err != nil {
			t.Fatalf("insert probe job %s: %v", id, err)
		}
	}
	for i := 0; i < finishedCount; i++ {
		insert(fmt.Sprintf("finished-%04d", i), JobStatusFinished, int64(i+1), cutoffMS+1, cutoffMS-1)
	}
	for i := 0; i < expiredCount; i++ {
		insert(fmt.Sprintf("expired-%04d", i), JobStatusExpired, int64(finishedCount+i+1), cutoffMS-1, nil)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func countScheduledRetentionCandidates(t *testing.T, store *Store) (finished, expired int) {
	t.Helper()
	if err := store.db.QueryRow(`SELECT count(*) FROM probe_jobs WHERE origin='scheduled' AND status='finished'`).Scan(&finished); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM probe_jobs WHERE origin='scheduled' AND status='expired'`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	return finished, expired
}

func retentionRowExists(t *testing.T, store *Store, table, id string) bool {
	t.Helper()
	var exists int
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s=?", table, map[string]string{"probe_jobs": "id", "probe_results": "job_id"}[table])
	if err := store.db.QueryRow(query, id).Scan(&exists); err != nil {
		t.Fatalf("query %s row %q: %v", table, id, err)
	}
	return exists != 0
}
