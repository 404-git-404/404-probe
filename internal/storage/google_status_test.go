package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func negotiateGoogleStatus(t *testing.T, store *Store, agentID string, at time.Time) {
	t.Helper()
	report := validReport(agentID, 1, "session-1", "boot", 1, 10, 20)
	report.CollectedAt = at.UnixMilli()
	if _, accepted, _, err := store.ProcessReport(context.Background(), agentID, report, at); err != nil || !accepted {
		t.Fatalf("report accepted=%t err=%v", accepted, err)
	}
	if ok, err := store.NegotiateGoogleStatusCapability(context.Background(), agentID, true, 1, "session-1", at); err != nil || !ok {
		t.Fatalf("negotiate=%t err=%v", ok, err)
	}
}

func TestGoogleStatusFirstDailyAndRestartPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "google.db")
	store, agentID, _ := testStore(t, path)
	ctx := context.Background()
	at := time.Unix(10_000, 0)
	negotiateGoogleStatus(t, store, agentID, at)
	if created, err := store.EnsureGoogleStatusJob(ctx, agentID, at); err != nil || !created {
		t.Fatalf("first created=%t err=%v", created, err)
	}
	if created, err := store.EnsureGoogleStatusJob(ctx, agentID, at); err != nil || created {
		t.Fatalf("duplicate created=%t err=%v", created, err)
	}
	job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypeGoogleStatus), at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, resultFor(job, true), at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	snapshot, exists, err := store.GetGoogleStatus(ctx, agentID)
	if err != nil || !exists || snapshot.Result.YouTube.Region != "JP" || snapshot.CheckedAt != at.Add(time.Second).UnixMilli() {
		t.Fatalf("snapshot=%+v exists=%t err=%v", snapshot, exists, err)
	}
	if created, err := store.EnsureGoogleStatusJob(ctx, agentID, at.Add(23*time.Hour)); err != nil || created {
		t.Fatalf("early created=%t err=%v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, exists, err := store.GetGoogleStatus(ctx, agentID); err != nil || !exists {
		t.Fatalf("restart exists=%t err=%v", exists, err)
	}
	if created, err := store.EnsureGoogleStatusJob(ctx, agentID, at.Add(24*time.Hour+2*time.Second)); err != nil || !created {
		t.Fatalf("daily created=%t err=%v", created, err)
	}
}

func TestGoogleStatusUnknownBackoff(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(20_000, 0)
	negotiateGoogleStatus(t, store, agentID, at)
	var lastUnknownJobID string
	for attempt, delay := range []time.Duration{10 * time.Minute, 30 * time.Minute, time.Hour, time.Hour} {
		if created, err := store.EnsureGoogleStatusJob(ctx, agentID, at); err != nil || !created {
			t.Fatalf("attempt %d create=%t err=%v", attempt, created, err)
		}
		job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypeGoogleStatus), at, time.Minute)
		if err != nil || job == nil {
			t.Fatalf("attempt %d claim=%+v err=%v", attempt, job, err)
		}
		result := resultFor(job, true)
		lastUnknownJobID = job.JobID
		result.Result.GoogleStatus.Gemini = protocol.GeminiResult{Status: protocol.GeminiUnknown, Error: protocol.GoogleServiceError{Category: "timeout"}}
		result.Result.GoogleStatus.YouTube = protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: "timeout"}}
		checked := at.Add(time.Second)
		if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, result, checked); err != nil {
			t.Fatal(err)
		}
		snapshot, _, err := store.GetGoogleStatus(ctx, agentID)
		if err != nil || snapshot.NextDueAt != checked.Add(delay).UnixMilli() || snapshot.UnknownStreak != attempt+1 {
			t.Fatalf("attempt %d snapshot=%+v err=%v", attempt, snapshot, err)
		}
		at = checked.Add(delay)
	}
	if created, err := store.EnsureGoogleStatusJob(ctx, agentID, at); err != nil || !created {
		t.Fatalf("recovery create=%t err=%v", created, err)
	}
	job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypeGoogleStatus), at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("recovery claim=%+v err=%v", job, err)
	}
	result := resultFor(job, true)
	result.Result.GoogleStatus.Search = protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown}
	result.Result.GoogleStatus.SignIn = protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}
	checked := at.Add(time.Second)
	if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, result, checked); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := store.GetGoogleStatus(ctx, agentID)
	if err != nil || snapshot.UnknownStreak != 0 || snapshot.NextDueAt != checked.Add(GoogleStatusInterval).UnixMilli() {
		t.Fatalf("recovery snapshot=%+v err=%v", snapshot, err)
	}
	_, old, err := store.GetProbeJobSnapshot(ctx, lastUnknownJobID, checked)
	if err != nil || old == nil || old.Result.Result.GoogleStatus.Gemini.Status != protocol.GeminiUnknown {
		t.Fatalf("older history lost: result=%+v err=%v", old, err)
	}
}

func TestGoogleStatusRetainedSchedulingAndLegacyHistory(t *testing.T) {
	for _, mode := range []string{"new_compat_unknown", "legacy", "removed_unknown", "gemini_unknown", "gemini_blocked", "youtube_unknown"} {
		t.Run(mode, func(t *testing.T) {
			store, id, _ := testStore(t, ":memory:")
			defer store.Close()
			ctx := context.Background()
			at := time.Unix(25_000, 0)
			negotiateGoogleStatus(t, store, id, at)
			if created, err := store.EnsureGoogleStatusJob(ctx, id, at); err != nil || !created {
				t.Fatalf("create=%t err=%v", created, err)
			}
			job, err := store.ClaimJob(ctx, id, claimRequest(1, "session-1", protocol.ProbeTypeGoogleStatus), at, time.Minute)
			if err != nil || job == nil {
				t.Fatalf("claim=%+v err=%v", job, err)
			}
			result := resultFor(job, true)
			if mode == "new_compat_unknown" || mode == "gemini_unknown" {
				result.Result.GoogleStatus.Gemini = protocol.GeminiResult{Status: protocol.GeminiUnknown, Error: protocol.GoogleServiceError{Category: "timeout"}}
			}
			if mode == "gemini_blocked" {
				result.Result.GoogleStatus.Gemini = protocol.GeminiResult{Status: protocol.GeminiBlocked, Error: protocol.GoogleServiceError{Category: "unrecognized_response"}}
			}
			if mode == "new_compat_unknown" || mode == "removed_unknown" {
				result.Result.GoogleStatus.Search = protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown}
				result.Result.GoogleStatus.SignIn = protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}
			}
			if mode == "removed_unknown" {
				result.Result.GoogleStatus.Search.Error.Category = "timeout"
				result.Result.GoogleStatus.SignIn.Status = protocol.GoogleSignInBlocked
			}
			if mode == "youtube_unknown" {
				result.Result.GoogleStatus.YouTube = protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: "timeout"}}
			}
			checked := at.Add(time.Second)
			if _, err := store.SubmitJobResult(ctx, id, job.JobID, result, checked); err != nil {
				t.Fatal(err)
			}
			snapshot, exists, err := store.GetGoogleStatus(ctx, id)
			if err != nil || !exists || snapshot.Result.Validate() != nil {
				t.Fatalf("snapshot=%+v exists=%t err=%v", snapshot, exists, err)
			}
			wantDelay, wantStreak := GoogleStatusInterval, 0
			if mode == "youtube_unknown" {
				wantDelay, wantStreak = 10*time.Minute, 1
			}
			if snapshot.NextDueAt != checked.Add(wantDelay).UnixMilli() || snapshot.UnknownStreak != wantStreak {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			if snapshot.Result.Search != result.Result.GoogleStatus.Search || snapshot.Result.SignIn != result.Result.GoogleStatus.SignIn || snapshot.Result.Gemini != result.Result.GoogleStatus.Gemini {
				t.Fatalf("legacy fields altered: %+v", snapshot)
			}
			// Persisted per-job history remains readable, including original legacy slots.
			_, stored, err := store.GetProbeJobSnapshot(ctx, job.JobID, checked)
			if err != nil || stored == nil || stored.Result.Result.GoogleStatus.Search != result.Result.GoogleStatus.Search || stored.Result.Result.GoogleStatus.SignIn != result.Result.GoogleStatus.SignIn || stored.Result.Result.GoogleStatus.Gemini != result.Result.GoogleStatus.Gemini {
				t.Fatalf("history=%+v err=%v", stored, err)
			}
		})
	}
}

func TestManualGoogleStatusGates(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(30_000, 0)
	if _, err := store.CreateManualGoogleStatusJob(ctx, agentID, at, time.Minute); !errors.Is(err, ErrGoogleStatusUnsupported) {
		t.Fatalf("unsupported err=%v", err)
	}
	negotiateGoogleStatus(t, store, agentID, at)
	if _, err := store.CreateManualGoogleStatusJob(ctx, agentID, at.Add(2*time.Minute), time.Minute); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline err=%v", err)
	}
	if created, err := store.CreateManualGoogleStatusJob(ctx, agentID, at, time.Minute); err != nil || !created {
		t.Fatalf("manual created=%t err=%v", created, err)
	}
	if _, err := store.CreateManualGoogleStatusJob(ctx, agentID, at, time.Minute); !errors.Is(err, ErrGoogleStatusPending) {
		t.Fatalf("pending err=%v", err)
	}
}

func TestGoogleStatusSessionFenceAndConcurrentCreation(t *testing.T) {
	store, id, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(40000, 0)
	report := func(epoch uint64, session string) {
		t.Helper()
		if _, accepted, _, err := store.ProcessReport(ctx, id, validReport(id, epoch, session, "boot", 1, 10, 20), at); err != nil || !accepted {
			t.Fatalf("report=%t err=%v", accepted, err)
		}
	}
	report(1, "session-1")
	if ok, err := store.NegotiateGoogleStatusCapability(ctx, id, true, 1, "session-1", at); !ok || err != nil {
		t.Fatalf("negotiate=%t err=%v", ok, err)
	}
	report(2, "session-2")
	if supported, err := store.GoogleStatusCapability(ctx, id); supported || err != nil {
		t.Fatalf("new session inherited capability=%t err=%v", supported, err)
	}
	if ok, err := store.NegotiateGoogleStatusCapability(ctx, id, true, 1, "session-1", at); ok || err != nil {
		t.Fatalf("stale negotiation=%t err=%v", ok, err)
	}
	if _, err := store.CreateManualGoogleStatusJob(ctx, id, at, time.Minute); !errors.Is(err, ErrGoogleStatusUnsupported) {
		t.Fatalf("before negotiation err=%v", err)
	}
	if ok, err := store.NegotiateGoogleStatusCapability(ctx, id, true, 2, "session-2", at); !ok || err != nil {
		t.Fatalf("new negotiation=%t err=%v", ok, err)
	}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func(manual bool) {
			defer group.Done()
			var err error
			if manual {
				_, err = store.CreateManualGoogleStatusJob(ctx, id, at, time.Minute)
			} else {
				_, err = store.EnsureGoogleStatusJob(ctx, id, at)
			}
			if err != nil && !errors.Is(err, ErrGoogleStatusPending) {
				t.Errorf("concurrent create: %v", err)
			}
		}(i%2 == 0)
	}
	group.Wait()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM probe_jobs WHERE agent_id=? AND probe_type='google_status'`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("jobs=%d err=%v", count, err)
	}
}

func TestGoogleStatusV8MigrationPreservesDualLaneAndData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v8.db")
	store, id, _ := testStore(t, path)
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(ctx, oneShot("old-job", id, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOutboundSnapshot(ctx, id, protocol.OutboundSnapshot{Available: false}, at); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"DROP INDEX idx_probe_jobs_one_pending_google_status", "DROP TABLE agent_google_status", "DROP TABLE agent_google_status_capabilities", "DELETE FROM schema_migrations WHERE version=9"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var before string
	if err := db.QueryRow(`SELECT group_concat(sql,';') FROM sqlite_master WHERE name IN ('idx_probe_jobs_one_active_probe_lease','idx_probe_jobs_one_active_selector_lease') ORDER BY name`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for attempt := 0; attempt < 2; attempt++ {
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if version, err := store.SchemaVersion(ctx); err != nil || version != currentSchemaVersion {
			t.Fatalf("version=%d err=%v", version, err)
		}
		if _, err := store.GetProbeJob(ctx, "old-job"); err != nil {
			t.Fatal(err)
		}
		if _, exists, err := store.GetOutboundSnapshot(ctx, id); err != nil || !exists {
			t.Fatalf("outbounds exists=%t err=%v", exists, err)
		}
		var after string
		if err := store.db.QueryRow(`SELECT group_concat(sql,';') FROM sqlite_master WHERE name IN ('idx_probe_jobs_one_active_probe_lease','idx_probe_jobs_one_active_selector_lease') ORDER BY name`).Scan(&after); err != nil || before != after {
			t.Fatalf("dual lane changed: %q => %q err=%v", before, after, err)
		}
		store.Close()
	}
}
