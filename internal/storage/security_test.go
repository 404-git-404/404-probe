package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func securityBatchAt(id string, at time.Time) protocol.SecurityBatch {
	start := at.Add(-time.Hour).UnixMilli()
	end := at.Add(-time.Minute).UnixMilli()
	return protocol.SecurityBatch{BatchID: id, WindowStart: start, WindowEnd: end, CollectedAt: at.UnixMilli(), Status: protocol.SecurityStatusComplete,
		TotalEvents: 5, TrackedSources: 1, ScannedLines: 6, ScannedBytes: 500,
		Sources: []protocol.SecuritySource{{IP: "192.0.2.1", Count: 5, FirstSeen: start, LastSeen: end, DurationMS: end - start, Classifications: []protocol.SecurityClassification{protocol.SecurityRepeated}}}}
}

func TestSecuritySubmissionIdempotencyConflictFenceAndSessionRecovery(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	if _, accepted, reason, err := store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session-1", "boot", 1, 10, 20), now); err != nil || !accepted {
		t.Fatalf("report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
	batch := securityBatchAt("11111111111111111111111111111111", now)
	submission := protocol.SecuritySubmission{ProtocolVersion: protocol.SecurityProtocolVersion, AgentEpoch: 1, SessionID: "session-1", Status: batch.Status, Batch: &batch}
	if changed, err := store.SaveSecuritySubmission(ctx, agentID, submission, now); err != nil || !changed {
		t.Fatalf("first changed=%t err=%v", changed, err)
	}
	if changed, err := store.SaveSecuritySubmission(ctx, agentID, submission, now.Add(time.Second)); err != nil || changed {
		t.Fatalf("duplicate changed=%t err=%v", changed, err)
	}
	conflict := batch
	conflict.TotalEvents++
	conflict.ScannedLines++
	conflict.Sources = append([]protocol.SecuritySource(nil), batch.Sources...)
	conflict.Sources[0].Count++
	conflicting := submission
	conflicting.Batch = &conflict
	if _, err := store.SaveSecuritySubmission(ctx, agentID, conflicting, now.Add(time.Second)); !errors.Is(err, ErrSecurityConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	if _, accepted, reason, err := store.ProcessReport(ctx, agentID, validReport(agentID, 2, "session-2", "boot", 1, 20, 30), now.Add(2*time.Second)); err != nil || !accepted {
		t.Fatalf("restart report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
	if _, exists, err := store.SecurityCapability(ctx, agentID); err != nil || exists {
		t.Fatalf("old capability exists=%t err=%v", exists, err)
	}
	if _, err := store.SaveSecuritySubmission(ctx, agentID, submission, now.Add(3*time.Second)); !errors.Is(err, ErrSecurityFence) {
		t.Fatalf("old session err=%v", err)
	}
	replay := submission
	replay.AgentEpoch, replay.SessionID = 2, "session-2"
	if changed, err := store.SaveSecuritySubmission(ctx, agentID, replay, now.Add(3*time.Second)); err != nil || !changed {
		t.Fatalf("new-session replay changed=%t err=%v", changed, err)
	}
	capability, exists, err := store.SecurityCapability(ctx, agentID)
	if err != nil || !exists || capability.CurrentBatchID != batch.BatchID || capability.SessionID != "session-2" {
		t.Fatalf("capability=%+v exists=%t err=%v", capability, exists, err)
	}
	current, err := store.CurrentSecurity(ctx, agentID, capability, now.Add(3*time.Second))
	if err != nil || current == nil || current.Batch.BatchID != batch.BatchID {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}

func TestOlderSecurityBatchCannotRegressCurrentStatus(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	_, _, _, _ = store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", 1, 10, 20), now)
	latest := securityBatchAt("22222222222222222222222222222222", now)
	latestSubmission := protocol.SecuritySubmission{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", Status: latest.Status, Batch: &latest}
	if _, err := store.SaveSecuritySubmission(ctx, agentID, latestSubmission, now); err != nil {
		t.Fatal(err)
	}
	older := securityBatchAt("33333333333333333333333333333333", now.Add(-2*time.Hour))
	older.Status, older.Reason, older.Sources, older.TotalEvents, older.TrackedSources = protocol.SecurityStatusFailed, "journal_read_failed", []protocol.SecuritySource{}, 0, 0
	olderSubmission := protocol.SecuritySubmission{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", Status: older.Status, Reason: older.Reason, Batch: &older}
	if changed, err := store.SaveSecuritySubmission(ctx, agentID, olderSubmission, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("historical changed=%t err=%v", changed, err)
	}
	capability, _, _ := store.SecurityCapability(ctx, agentID)
	if capability.Status != protocol.SecurityStatusComplete || capability.CurrentBatchID != latest.BatchID {
		t.Fatalf("current regressed: %+v", capability)
	}
}

func TestCleanupSecurityHistoryUsesReceivedAtRetention(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	_, _, _, _ = store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", 1, 10, 20), now)

	oldBatch := securityBatchAt("44444444444444444444444444444444", now.Add(-40*24*time.Hour))
	oldSubmission := protocol.SecuritySubmission{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", Status: oldBatch.Status, Batch: &oldBatch}
	if _, err := store.SaveSecuritySubmission(ctx, agentID, oldSubmission, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	recentBatch := securityBatchAt("55555555555555555555555555555555", now.Add(-20*24*time.Hour))
	recentSubmission := protocol.SecuritySubmission{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", Status: recentBatch.Status, Batch: &recentBatch}
	if _, err := store.SaveSecuritySubmission(ctx, agentID, recentSubmission, now.Add(-20*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := store.CleanupSecurityHistory(ctx, now.Add(-SecurityRetention)); err != nil {
		t.Fatal(err)
	}
	records, err := store.SecurityHistory(ctx, agentID, now.Add(-60*24*time.Hour), 31, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Batch.BatchID != recentBatch.BatchID {
		t.Fatalf("retained security records=%+v", records)
	}
}

func TestSecurityDeliveryGapDoesNotChangeBatchIdempotency(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	_, _, _, _ = store.ProcessReport(ctx, agentID, validReport(agentID, 1, "session", "boot", 1, 10, 20), now)
	batch := securityBatchAt("66666666666666666666666666666666", now)
	submission := protocol.SecuritySubmission{ProtocolVersion: 1, AgentEpoch: 1, SessionID: "session", Status: batch.Status, Batch: &batch}
	if _, err := store.SaveSecuritySubmission(ctx, agentID, submission, now); err != nil {
		t.Fatal(err)
	}
	submission.Delivery = &protocol.SecurityDelivery{OutboxGap: true, PreviousCollectedAt: now.Add(-40 * 24 * time.Hour).UnixMilli()}
	if changed, err := store.SaveSecuritySubmission(ctx, agentID, submission, now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("gap duplicate changed=%t err=%v", changed, err)
	}
	capability, _, _ := store.SecurityCapability(ctx, agentID)
	current, err := store.CurrentSecurity(ctx, agentID, capability, now.Add(time.Second))
	if err != nil || current == nil || !current.DeliveryGap || current.PreviousCollectedAt != submission.Delivery.PreviousCollectedAt {
		t.Fatalf("current=%+v err=%v", current, err)
	}
}
