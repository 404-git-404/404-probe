package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func m11StoredResult(region, reason string) protocol.GoogleStatusResult {
	y := protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: reason}}
	if region != "" {
		cn := region == "CN"
		y = protocol.YouTubeResult{Status: protocol.YouTubeNotCN, Region: region, SentToChina: &cn}
		if cn {
			y.Status = protocol.YouTubeCN
		}
	}
	return protocol.GoogleStatusResult{YouTube: y, Search: protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown}, SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}, Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown}}
}

func TestM11YouTubeOrderingDiagnosticsRestartAndSchema28(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "youtube.db")
	s, id, _ := testStore(t, path)
	at := time.Now()
	negotiateGoogleStatus(t, s, id, at)
	save := func(result protocol.GoogleStatusResult, when time.Time, diagnostic string) {
		t.Helper()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = saveGoogleStatusTx(ctx, tx, id, result, when.UnixMilli(), diagnostic); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	save(m11StoredResult("US", ""), at, "")
	save(m11StoredResult("", "unrecognized_response"), at.Add(time.Second), "youtube_consent_page")
	save(m11StoredResult("JP", ""), at.Add(-time.Second), "")
	view, exists, err := s.GetGoogleStatus(ctx, id)
	if err != nil || !exists || view.Result.YouTube.Status != protocol.YouTubeUnknown || view.Result.YouTube.Region != "" || view.Result.YouTube.Error.Category != "consent_page" || view.UnknownStreak != 1 {
		t.Fatal(view, err)
	}
	if version, err := s.SchemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatal(version, err)
	}
	states, err := s.ListStates(ctx)
	if err != nil || len(states) != 1 || states[0].PublicEgress.IPv4.Status != "unknown" {
		t.Fatal(states, err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, _, err := s.GetGoogleStatus(ctx, id)
	if err != nil || after.CheckedAt != view.CheckedAt || after.Result.YouTube.Error.Category != "consent_page" {
		t.Fatal(after, err)
	}
}

func TestM11YouTubeExpiredJobUnknownAndBoundedRetry(t *testing.T) {
	ctx := context.Background()
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	at := time.Now()
	negotiateGoogleStatus(t, s, id, at)
	if _, err := s.CreateManualGoogleStatusJob(ctx, id, at, time.Minute); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob(ctx, id, claimRequest(1, "session-1", protocol.ProbeTypeGoogleStatus), at, time.Minute)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if _, err = s.SubmitJobResult(ctx, id, job.JobID, resultFor(job, true), at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	manualAt := at.Add(2 * time.Second)
	if _, err = s.CreateManualGoogleStatusJob(ctx, id, manualAt, time.Minute); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		if _, err = s.CreateManualGoogleStatusJob(ctx, id, manualAt, time.Minute); err != ErrGoogleStatusPending {
			t.Fatal("duplicate", err)
		}
	}
	expires := manualAt.Add(googleStatusJobLife)
	view, exists, err := s.GetGoogleStatusAt(ctx, id, expires)
	if err != nil || !exists || view.Result.YouTube.Status != protocol.YouTubeUnknown || view.Result.YouTube.Error.Category != "job_expired" || view.CheckedAt != expires.UnixMilli() {
		t.Fatal(view, err)
	}
	// The Agent's next ordinary claim/ensure persists the same projection and
	// does not immediately issue another external request during the backoff.
	if created, err := s.EnsureGoogleStatusJob(ctx, id, expires); err != nil || created {
		t.Fatal(created, err)
	}
	persisted, _, err := s.GetGoogleStatus(ctx, id)
	if err != nil || persisted.CheckedAt != view.CheckedAt || persisted.NextDueAt != view.NextDueAt {
		t.Fatal(persisted, view, err)
	}
	if created, err := s.EnsureGoogleStatusJob(ctx, id, expires.Add(10*time.Minute)); err != nil || !created {
		t.Fatal(created, err)
	}
}
