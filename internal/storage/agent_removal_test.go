package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func reportRemovalCapability(t *testing.T, store *Store, agentID string, epoch uint64, session string, sequence uint64, at time.Time) {
	t.Helper()
	report := validReport(agentID, epoch, session, "boot", sequence, sequence*10, sequence*20)
	report.CollectedAt = at.UnixMilli()
	report.Management = &protocol.AgentManagementCapabilities{RemoteRemoval: true, CountryCodeLookup: true}
	if _, accepted, reason, err := store.ProcessReport(context.Background(), agentID, report, at); err != nil || !accepted {
		t.Fatalf("capability report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
}

func TestAgentRemovalRequiresCapabilityConflictsWithUpgradeAndStopsNewWork(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Now()
	if _, _, err := store.CreateAgentRemovalOperation(ctx, agentID, "11111111111111111111111111111111", at); !errors.Is(err, ErrAgentRemovalUnsupported) {
		t.Fatalf("unsupported Agent removal error=%v", err)
	}
	reportRemovalCapability(t, store, agentID, 1, "session-1", 1, at)
	if _, err := store.CreateUpgrade(ctx, UpgradeOperation{OperationID: "upgrade-1", AgentID: agentID, FromVersion: "v1.0.0", TargetVersion: "v1.0.1", CreatedAt: at.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateAgentRemovalOperation(ctx, agentID, "11111111111111111111111111111111", at.Add(time.Second)); !errors.Is(err, ErrAgentRemovalUpgradeConflict) {
		t.Fatalf("active upgrade removal conflict=%v", err)
	}
	if operation, err := store.ClaimUpgrade(ctx, agentID, at.Add(1500*time.Millisecond)); err != nil || operation == nil || operation.Status != UpgradeClaimed {
		t.Fatalf("claim upgrade=%+v err=%v", operation, err)
	}
	if _, err := store.UpdateUpgradeStatus(ctx, agentID, "upgrade-1", UpgradeFailed, "test", "test", at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	jobID := "22222222222222222222222222222222"
	job, created, err := store.CreateOneShotJobIdempotent(ctx, CreateOneShotJobParams{
		ID: jobID, AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect,
		Config:    protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}},
		TimeoutMS: 1000, CreatedAt: at.Add(3 * time.Second).UnixMilli(),
		NotBefore: at.Add(3 * time.Second).UnixMilli(), ExpiresAt: at.Add(time.Minute).UnixMilli(),
	})
	if err != nil || !created || job.Status != JobStatusQueued {
		t.Fatalf("pre-removal job=%+v created=%t err=%v", job, created, err)
	}

	operation, created, err := store.CreateAgentRemovalOperation(ctx, agentID, "11111111111111111111111111111111", at.Add(4*time.Second))
	if err != nil || !created || operation.Status != AgentRemovalRequested {
		t.Fatalf("operation=%+v created=%t err=%v", operation, created, err)
	}
	replayed, created, err := store.CreateAgentRemovalOperation(ctx, agentID, operation.OperationID, at.Add(5*time.Second))
	if err != nil || created || replayed.OperationID != operation.OperationID || replayed.Status != AgentRemovalRequested {
		t.Fatalf("idempotent replay=%+v created=%t err=%v", replayed, created, err)
	}
	if _, _, err := store.CreateOneShotJobIdempotent(ctx, CreateOneShotJobParams{
		ID: "33333333333333333333333333333333", AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect,
		Config:    protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}},
		TimeoutMS: 1000, CreatedAt: at.Add(6 * time.Second).UnixMilli(),
		NotBefore: at.Add(6 * time.Second).UnixMilli(), ExpiresAt: at.Add(time.Minute).UnixMilli(),
	}); !errors.Is(err, ErrAgentRemovalPending) {
		t.Fatalf("new probe job while removal pending error=%v", err)
	}
	if _, err := store.CreateUpgrade(ctx, UpgradeOperation{OperationID: "upgrade-2", AgentID: agentID, FromVersion: "v1.0.0", TargetVersion: "v1.0.1", CreatedAt: at.Add(6 * time.Second).UnixMilli()}); !errors.Is(err, ErrAgentRemovalPending) {
		t.Fatalf("new upgrade while removal pending error=%v", err)
	}
	claim, err := store.ClaimJob(ctx, agentID, protocol.ClaimRequest{
		ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: 1, SessionID: "session-1", SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
	}, at.Add(7*time.Second), time.Minute)
	if err != nil || claim != nil {
		t.Fatalf("normal claim while removal pending job=%+v err=%v", claim, err)
	}
	job, _, err = store.GetProbeJobSnapshot(ctx, jobID, at.Add(8*time.Second))
	if err != nil || job.Status != JobStatusExpired {
		t.Fatalf("preexisting queued job not expired: job=%+v err=%v", job, err)
	}
}

func TestAgentRemovalResumesAcrossSessionAndReceiptTransactionDeletesBusinessData(t *testing.T) {
	ctx := context.Background()
	store, agentID, oldToken := testStore(t, ":memory:")
	defer store.Close()
	// Keep the synthetic long replay window behind wall-clock time so the
	// newly added B/C management reports do not appear to come from the future.
	at := time.Now().Add(-AgentRemovalReceiptReplayTTL - 2*time.Hour)
	reportRemovalCapability(t, store, agentID, 1, "session-1", 1, at)
	operationID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	operation, created, err := store.CreateAgentRemovalOperation(ctx, agentID, operationID, at.Add(time.Second))
	if err != nil || !created {
		t.Fatalf("create operation=%+v created=%t err=%v", operation, created, err)
	}
	delivery, err := store.ClaimAgentRemoval(ctx, agentID, 1, "session-1", at.Add(2*time.Second))
	if err != nil || delivery == nil || delivery.Operation.Status != AgentRemovalDelivered || delivery.ReceiptToken == "" {
		t.Fatalf("first delivery=%+v err=%v", delivery, err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operationID, delivery.ReceiptToken, "agent_uninstalled", at.Add(3*time.Second)); !errors.Is(err, ErrAgentRemovalTransition) {
		t.Fatalf("receipt before uninstalling error=%v", err)
	}
	if _, err := store.MarkAgentRemovalUninstalling(ctx, agentID, operationID, 1, "wrong-session", at.Add(3*time.Second)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale session transition error=%v", err)
	}
	if status, err := store.MarkAgentRemovalUninstalling(ctx, agentID, operationID, 1, "session-1", at.Add(3*time.Second)); err != nil || status.Status != AgentRemovalUninstalling {
		t.Fatalf("mark uninstalling=%+v err=%v", status, err)
	}
	if _, err := store.PutAgentPlan(ctx, AgentPlan{AgentID: agentID, CountryCodeOverride: "US"}, nil, at.Add(4*time.Second)); err != nil {
		t.Fatalf("prepare plan for cascade cleanup: %v", err)
	}

	// A newly reporting process can take over a pending operation and rotates
	// the receipt credential, so a token from the old session cannot delete it.
	reportRemovalCapability(t, store, agentID, 2, "session-2", 1, at.Add(5*time.Second))
	if _, err := store.CompleteAgentRemoval(ctx, operationID, delivery.ReceiptToken, "agent_uninstalled", at.Add(5500*time.Millisecond)); !errors.Is(err, ErrAgentRemovalCredential) {
		t.Fatalf("old-session receipt must be fenced immediately after report acceptance, before takeover claim: %v", err)
	}
	recovery, err := store.ClaimAgentRemoval(ctx, agentID, 2, "session-2", at.Add(6*time.Second))
	if err != nil || recovery == nil || recovery.Operation.Status != AgentRemovalUninstalling || recovery.ReceiptToken == delivery.ReceiptToken {
		t.Fatalf("recovery delivery=%+v err=%v", recovery, err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operationID, delivery.ReceiptToken, "agent_uninstalled", at.Add(7*time.Second)); !errors.Is(err, ErrAgentRemovalCredential) {
		t.Fatalf("superseded receipt credential error=%v", err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operationID, recovery.ReceiptToken, "revoke", at.Add(7*time.Second)); !errors.Is(err, ErrAgentRemovalCredential) {
		t.Fatalf("non-fixed receipt kind error=%v", err)
	}
	if id, err := store.Authenticate(ctx, recovery.ReceiptToken); !errors.Is(err, ErrUnauthorized) || id != "" {
		t.Fatalf("receipt credential must not authenticate other Agent APIs: id=%q err=%v", id, err)
	}
	completedAt := at.Add(8 * time.Second)
	ack, err := store.CompleteAgentRemoval(ctx, operationID, recovery.ReceiptToken, "agent_uninstalled", completedAt)
	if err != nil || !ack.Completed || ack.Duplicate {
		t.Fatalf("receipt ack=%+v err=%v", ack, err)
	}
	if _, err := store.Authenticate(ctx, oldToken); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("business credential remains usable after removal: %v", err)
	}
	if _, err := store.GetAgentSnapshot(ctx, agentID, completedAt, time.Minute); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("Agent business record remains after removal: %v", err)
	}
	for _, table := range []string{"agents", "agent_state", "agent_sessions", "probe_jobs", "agent_plans", "agent_removal_operations", "agent_management_capabilities"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+removalBusinessPredicate(table)+``, agentID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("business rows remain in %s: count=%d err=%v", table, count, err)
		}
	}
	var receipts int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_removal_receipts WHERE operation_id=? AND receipt_kind='agent_uninstalled'`, operationID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("minimal receipt count=%d err=%v", receipts, err)
	}
	ack, err = store.CompleteAgentRemoval(ctx, operationID, recovery.ReceiptToken, "agent_uninstalled", completedAt.Add(AgentRemovalReceiptTTL+time.Minute))
	if err != nil || !ack.Completed || !ack.Duplicate {
		t.Fatalf("lost success response replay after credential TTL ack=%+v err=%v", ack, err)
	}

	// Creating another Agent's removal after A's short bearer credential TTL
	// must not prune A's still-live detached replay receipt.
	afterTokenTTL := completedAt.Add(AgentRemovalReceiptTTL + time.Minute)
	agentB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	if err := store.AddAgent(ctx, agentB, "agent-b", []byte("agent-b-token-hash"), afterTokenTTL); err != nil {
		t.Fatal(err)
	}
	reportRemovalCapability(t, store, agentB, 1, "session-b", 1, afterTokenTTL)
	if _, created, err := store.CreateAgentRemovalOperation(ctx, agentB, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", afterTokenTTL.Add(time.Second)); err != nil || !created {
		t.Fatalf("create Agent B removal after Agent A credential TTL created=%t err=%v", created, err)
	}
	ack, err = store.CompleteAgentRemoval(ctx, operationID, recovery.ReceiptToken, "agent_uninstalled", afterTokenTTL.Add(2*time.Second))
	if err != nil || !ack.Completed || !ack.Duplicate {
		t.Fatalf("Agent A replay after Agent B cleanup ack=%+v err=%v", ack, err)
	}
	if _, err := store.GetAgentSnapshot(ctx, agentB, afterTokenTTL.Add(2*time.Second), time.Minute); err != nil {
		t.Fatalf("Agent B business record was unexpectedly removed: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_removal_receipts WHERE operation_id=?`, operationID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("Agent A replay receipt before replay expiry count=%d err=%v", receipts, err)
	}

	// The exact replay-expiry boundary is inclusive. A later removal request
	// runs global cleanup at that timestamp, after which A's old token is denied.
	replayExpires := completedAt.Add(AgentRemovalReceiptReplayTTL)
	agentC := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	if err := store.AddAgent(ctx, agentC, "agent-c", []byte("agent-c-token-hash"), replayExpires.Add(-2*time.Second)); err != nil {
		t.Fatal(err)
	}
	reportRemovalCapability(t, store, agentC, 1, "session-c", 1, replayExpires.Add(-time.Second))
	if _, created, err := store.CreateAgentRemovalOperation(ctx, agentC, "cccccccccccccccccccccccccccccccc", replayExpires); err != nil || !created {
		t.Fatalf("create Agent C removal at replay expiry created=%t err=%v", created, err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operationID, recovery.ReceiptToken, "agent_uninstalled", replayExpires); !errors.Is(err, ErrAgentRemovalCredential) {
		t.Fatalf("Agent A replay at exact detached replay expiry error=%v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_removal_receipts WHERE operation_id=?`, operationID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("Agent A replay receipt at expiry count=%d err=%v", receipts, err)
	}
	if _, err := store.PruneExpiredAgentRemovalReceipts(ctx, completedAt.Add(AgentRemovalReceiptReplayTTL+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operationID, recovery.ReceiptToken, "agent_uninstalled", completedAt.Add(AgentRemovalReceiptReplayTTL+time.Minute)); !errors.Is(err, ErrAgentRemovalCredential) {
		t.Fatalf("expired detached replay credential error=%v", err)
	}
}

func TestAgentRemovalReceiptExpiresWithoutAnotherRemovalRequestAndOnRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "expired-receipt.db")
	store, agentID, _ := testStore(t, path)
	at := time.Now().Add(-AgentRemovalReceiptTTL - AgentRemovalReceiptReplayTTL - time.Minute)
	operationID := "ffffffffffffffffffffffffffffffff"
	reportRemovalCapability(t, store, agentID, 1, "session-expiry", 1, at)
	if _, _, err := store.CreateAgentRemovalOperation(ctx, agentID, operationID, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.ClaimAgentRemoval(ctx, agentID, 1, "session-expiry", at.Add(2*time.Second))
	if err != nil || delivery == nil {
		t.Fatalf("claim=%+v err=%v", delivery, err)
	}
	if _, err := store.MarkAgentRemovalUninstalling(ctx, agentID, operationID, 1, "session-expiry", at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operationID, delivery.ReceiptToken, "agent_uninstalled", at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening the long-lived database is sufficient to prune the detached
	// protocol receipt; no later removal request is needed to trigger cleanup.
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_removal_receipts WHERE operation_id=?`, operationID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired detached receipt after restart count=%d err=%v", count, err)
	}
}

func removalBusinessPredicate(table string) string {
	switch table {
	case "agents":
		return "id=?"
	case "agent_state", "agent_sessions", "probe_jobs", "agent_plans", "agent_removal_operations", "agent_management_capabilities":
		return "agent_id=?"
	default:
		panic("unexpected test table")
	}
}

func TestAgentRemovalReceiptRequiresCurrentCredentialAndSurvivesRestartFailurePending(t *testing.T) {
	ctx := context.Background()
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Now()
	reportRemovalCapability(t, store, agentID, 5, "session-a", 1, at)
	operation, _, err := store.CreateAgentRemovalOperation(ctx, agentID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimAgentRemoval(ctx, agentID, 5, "session-a", at.Add(2*time.Second))
	if err != nil || first == nil {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if _, err := store.MarkAgentRemovalUninstalling(ctx, agentID, operation.OperationID, 5, "session-a", at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimAgentRemoval(ctx, agentID, 5, "session-a", at.Add(4*time.Second))
	if err != nil || second == nil || second.ReceiptToken == first.ReceiptToken || second.Operation.Status != AgentRemovalUninstalling {
		t.Fatalf("retry claim=%+v err=%v", second, err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operation.OperationID, first.ReceiptToken, "agent_uninstalled", at.Add(5*time.Second)); !errors.Is(err, ErrAgentRemovalCredential) {
		t.Fatalf("rotated credential should fail: %v", err)
	}
	current, exists, err := store.GetAgentRemovalOperation(ctx, agentID)
	if err != nil || !exists || current.Status != AgentRemovalUninstalling {
		t.Fatalf("operation must remain pending after no receipt: %+v exists=%t err=%v", current, exists, err)
	}
	if _, err := store.CompleteAgentRemoval(ctx, operation.OperationID, second.ReceiptToken, "agent_uninstalled", at.Add(AgentRemovalReceiptTTL+10*time.Second)); !errors.Is(err, ErrAgentRemovalCredential) {
		t.Fatalf("expired credential error=%v", err)
	}
}

func TestAgentRemovalMigrationAndReceiptSchemaKeepAuditDetached(t *testing.T) {
	store, _, _ := testStore(t, ":memory:")
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	rows, err := store.db.Query(`PRAGMA table_info(agent_removal_receipts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "agent_id" {
			t.Fatal("audit receipt must not retain Agent business identity")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRemovalIdempotencyKeyCannotBeReusedAcrossAgents(t *testing.T) {
	ctx := context.Background()
	store, firstID, _ := testStore(t, ":memory:")
	defer store.Close()
	secondID := "cccccccccccccccccccccccccccccccc"
	if err := store.AddAgent(ctx, secondID, "second", []byte("a-token-hash-placeholder"), time.Now()); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	reportRemovalCapability(t, store, firstID, 1, "first-session", 1, at)
	reportRemovalCapability(t, store, secondID, 1, "second-session", 1, at)
	operationID := "dddddddddddddddddddddddddddddddd"
	if _, created, err := store.CreateAgentRemovalOperation(ctx, firstID, operationID, at.Add(time.Second)); err != nil || !created {
		t.Fatalf("create first operation created=%t err=%v", created, err)
	}
	if _, _, err := store.CreateAgentRemovalOperation(ctx, secondID, operationID, at.Add(2*time.Second)); !errors.Is(err, ErrJobIDConflict) {
		t.Fatalf("cross-Agent operation ID reuse error=%v", err)
	}
}

func TestAgentRemovalOperationPersistsAcrossStoreReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "removal.db")
	store, agentID, _ := testStore(t, path)
	at := time.Now()
	reportRemovalCapability(t, store, agentID, 3, "session-restart", 1, at)
	if _, _, err := store.CreateAgentRemovalOperation(ctx, agentID, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.ClaimAgentRemoval(ctx, agentID, 3, "session-restart", at.Add(2*time.Second))
	if err != nil || delivery == nil || delivery.Operation.Status != AgentRemovalDelivered {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operation, exists, err := store.GetAgentRemovalOperation(ctx, agentID)
	if err != nil || !exists || operation.Status != AgentRemovalDelivered {
		t.Fatalf("reopened operation=%+v exists=%t err=%v", operation, exists, err)
	}
	resume, err := store.ClaimAgentRemoval(ctx, agentID, 3, "session-restart", at.Add(3*time.Second))
	if err != nil || resume == nil || resume.Operation.Status != AgentRemovalDelivered || resume.ReceiptToken == delivery.ReceiptToken {
		t.Fatalf("resumed delivery=%+v err=%v", resume, err)
	}
}
