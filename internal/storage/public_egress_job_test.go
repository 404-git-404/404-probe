package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

const egressOffline = 30 * time.Second

var egressSequence atomic.Uint64

func nextEgressSequence() uint64 { return egressSequence.Add(1) + 1000 }

func egressReport(t *testing.T, store *Store, agentID string, at time.Time, session string, epoch uint64, telemetry *protocol.PublicEgress) {
	t.Helper()
	report := validReport(agentID, epoch, session, "boot", 1, 10, 20)
	report.CollectedAt = at.UnixMilli()
	report.PublicEgress = telemetry
	report.Sequence = nextEgressSequence()
	if _, accepted, reason, err := store.ProcessReport(context.Background(), agentID, report, at); err != nil || !accepted {
		t.Fatalf("report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
}

// negotiateEgress registers a session for the Agent and records that it supports public_egress jobs.
func negotiateEgress(t *testing.T, store *Store, agentID string, at time.Time) {
	t.Helper()
	egressReport(t, store, agentID, at, "session-1", 1, nil)
	if ok, err := store.NegotiatePublicEgressCapability(context.Background(), agentID, true, 1, "session-1", at); err != nil || !ok {
		t.Fatalf("negotiate=%t err=%v", ok, err)
	}
}

func egressControl(t *testing.T, store *Store, agentID string, at time.Time) PublicEgressControl {
	t.Helper()
	control, err := store.GetPublicEgressControl(context.Background(), agentID, at)
	if err != nil {
		t.Fatal(err)
	}
	return control
}

func egressObservation(family, status, ip, reason string) protocol.EgressObservation {
	return protocol.EgressObservation{Family: family, Status: status, IP: ip, Reason: reason}
}

func egressOutcome(v4, v6 bool) protocol.PublicEgressResult {
	result := protocol.PublicEgressResult{IPv4: egressObservation("ipv4", "unknown", "", "timeout"), IPv6: egressObservation("ipv6", "unavailable", "", "family_unavailable")}
	if v4 {
		result.IPv4 = egressObservation("ipv4", "available", "8.8.8.8", "verified")
	}
	if v6 {
		result.IPv6 = egressObservation("ipv6", "available", "2606:4700:4700::1111", "verified")
	}
	return result
}

// finishEgressJob claims the pending job as the Agent (session-1) and returns its result.
func finishEgressJob(t *testing.T, store *Store, agentID string, at time.Time, outcome protocol.PublicEgressResult) {
	t.Helper()
	job, err := store.ClaimJob(context.Background(), agentID, claimRequest(1, "session-1", protocol.ProbeTypePublicEgress), at, time.Minute)
	if err != nil || job == nil || job.ProbeType != protocol.ProbeTypePublicEgress || job.Config.PublicEgress == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	result := resultFor(job, outcome.Succeeded())
	result.ResolvedIP = ""
	if !outcome.Succeeded() {
		result.ErrorCategory, result.ErrorMessage = "no_public_address", "no public address could be verified"
	}
	result.Result = protocol.ProbeResult{PublicEgress: &outcome}
	if _, err := store.SubmitJobResult(context.Background(), agentID, job.JobID, result, at); err != nil {
		t.Fatal(err)
	}
}

func egressState(t *testing.T, store *Store, agentID string, at time.Time) EgressState {
	t.Helper()
	record, err := store.GetAgentSnapshot(context.Background(), agentID, at, egressOffline)
	if err != nil || record.State == nil {
		t.Fatalf("snapshot=%+v err=%v", record, err)
	}
	return record.State.PublicEgress
}

func ensureEgress(t *testing.T, store *Store, agentID string, at time.Time, session string) bool {
	t.Helper()
	created, err := store.EnsurePublicEgressJob(context.Background(), agentID, 1, session, at)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestPublicEgressMigrationLeavesExistingAgentsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.db")
	store, agentID, _ := testStore(t, path)
	ctx := context.Background()
	at := time.Unix(10_000, 0)
	egressReport(t, store, agentID, at, "session-1", 1, nil)
	// Put the database back where a v1.0.3 Server left it (schema 29, no control table).
	for _, statement := range []string{
		`DROP INDEX idx_probe_jobs_one_pending_public_egress`,
		`DROP TABLE agent_public_egress_control`,
		`DELETE FROM schema_migrations WHERE version>=30`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version, err := store.SchemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema=%d err=%v", version, err)
	}
	if control := egressControl(t, store, agentID, at); control.Enabled || control.Supported {
		t.Fatalf("migrated Agent is not disabled: %+v", control)
	}
	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_public_egress_control`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("migration created %d rows err=%v", rows, err)
	}
}

func TestPublicEgressDisabledDispatchesNothingAndDropsLegacyTelemetry(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Unix(20_000, 0)
	negotiateEgress(t, store, agentID, at)
	if ensureEgress(t, store, agentID, at, "session-1") || ensureEgress(t, store, agentID, at, "session-2") {
		t.Fatal("a job was dispatched while the detection is off")
	}
	// A v1.0.3 Agent keeps attaching its own result; it must be neither stored nor shown.
	telemetry := &protocol.PublicEgress{Version: 1,
		IPv4: protocol.EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", CheckedAt: at.UnixMilli(), ValidUntil: at.Add(40 * time.Minute).UnixMilli(), Reason: "verified"},
		IPv6: protocol.UnknownEgress("ipv6", "not_checked")}
	report := validReport(agentID, 1, "session-1", "boot", 2, 10, 20)
	report.Sequence = nextEgressSequence()
	report.CollectedAt = at.Add(time.Second).UnixMilli()
	report.PublicEgress = telemetry
	state, accepted, _, err := store.ProcessReport(context.Background(), agentID, report, at.Add(time.Second))
	if err != nil || !accepted {
		t.Fatalf("accepted=%t err=%v", accepted, err)
	}
	if state.PublicEgress.IPv4.Status != "unknown" || state.PublicEgress.IPv4.IP != "" || state.PublicEgress.Supported {
		t.Fatalf("telemetry leaked into the reported state: %+v", state.PublicEgress)
	}
	if shown := egressState(t, store, agentID, at.Add(2*time.Second)); shown.IPv4.IP != "" || shown.IPv4.Status != "unknown" {
		t.Fatalf("telemetry stored while disabled: %+v", shown)
	}
	// Switched on, the same telemetry is accepted.
	if _, err := store.SetPublicEgressEnabled(context.Background(), agentID, true, at.Add(3*time.Second), egressOffline); err != nil {
		t.Fatal(err)
	}
	report.Sequence = nextEgressSequence()
	report.CollectedAt = at.Add(4 * time.Second).UnixMilli()
	if _, accepted, _, err := store.ProcessReport(context.Background(), agentID, report, at.Add(4*time.Second)); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	if shown := egressState(t, store, agentID, at.Add(5*time.Second)); shown.IPv4.IP != "8.8.8.8" {
		t.Fatalf("telemetry rejected while enabled: %+v", shown)
	}
}

func TestPublicEgressEnableDispatchesOnceAndCountsAsManual(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(30_000, 0)
	negotiateEgress(t, store, agentID, at)
	created, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline)
	if err != nil || !created {
		t.Fatalf("enable created=%t err=%v", created, err)
	}
	control := egressControl(t, store, agentID, at)
	if !control.Enabled || !control.Pending || control.LastManualAt != at.UnixMilli() || control.CooldownUntil != at.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("control=%+v", control)
	}
	if created, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil || created {
		t.Fatalf("second enable created=%t err=%v", created, err)
	}
	if ensureEgress(t, store, agentID, at, "session-1") {
		t.Fatal("scheduler overlapped the pending job")
	}
	finishEgressJob(t, store, agentID, at.Add(time.Second), egressOutcome(true, false))
	// Off and on again inside the cooldown: no immediate check.
	if _, err := store.SetPublicEgressEnabled(ctx, agentID, false, at.Add(time.Minute), egressOffline); err != nil {
		t.Fatal(err)
	}
	if created, err := store.SetPublicEgressEnabled(ctx, agentID, true, at.Add(2*time.Minute), egressOffline); err != nil || created {
		t.Fatalf("re-enable in cooldown created=%t err=%v", created, err)
	}
	if control := egressControl(t, store, agentID, at.Add(2*time.Minute)); control.Pending {
		t.Fatalf("pending after re-enable in cooldown: %+v", control)
	}
	// Disabling cancels a queued job.
	if err := store.CreateManualPublicEgressJob(ctx, agentID, at.Add(25*time.Hour), egressOffline); err == nil {
		t.Fatal("offline Agent accepted a manual check") // last report is 25h old
	}
}

func TestPublicEgressDisableCancelsQueuedJob(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(35_000, 0)
	negotiateEgress(t, store, agentID, at)
	if created, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil || !created {
		t.Fatal(created, err)
	}
	if _, err := store.SetPublicEgressEnabled(ctx, agentID, false, at.Add(time.Second), egressOffline); err != nil {
		t.Fatal(err)
	}
	if job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypePublicEgress), at.Add(2*time.Second), time.Minute); err != nil || job != nil {
		t.Fatalf("a cancelled job was claimed: %+v err=%v", job, err)
	}
	if ensureEgress(t, store, agentID, at.Add(time.Hour), "session-2") {
		t.Fatal("job dispatched after switching off")
	}
	// A result that arrives after the switch was turned off is dropped.
	if _, err := store.SetPublicEgressEnabled(ctx, agentID, true, at.Add(48*time.Hour), egressOffline); err != nil {
		t.Fatal(err)
	}
}

func TestPublicEgressSevenDayCycleAndReconnect(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(40_000, 0)
	negotiateEgress(t, store, agentID, at)
	if created, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil || !created {
		t.Fatal(created, err)
	}
	done := at.Add(5 * time.Second)
	finishEgressJob(t, store, agentID, done, egressOutcome(true, true))
	control := egressControl(t, store, agentID, done)
	if control.NextCheckAt != done.Add(7*24*time.Hour).UnixMilli() || control.FailureStep != 0 || control.Pending {
		t.Fatalf("control after success=%+v", control)
	}
	if ensureEgress(t, store, agentID, done.Add(7*24*time.Hour-time.Second), "session-1") {
		t.Fatal("dispatched before the seven days were over")
	}
	// Reconnect: a new session triggers one check immediately, and only one.
	if ok, err := store.NegotiatePublicEgressCapability(ctx, agentID, true, 1, "session-1", done); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !ensureEgress(t, store, agentID, done.Add(time.Hour), "session-2") {
		t.Fatal("reconnect did not dispatch a check")
	}
	if ensureEgress(t, store, agentID, done.Add(time.Hour), "session-2") {
		t.Fatal("reconnect dispatched twice")
	}
	// Reconnecting with a check in flight does not queue another one.
	if ensureEgress(t, store, agentID, done.Add(time.Hour+time.Minute), "session-3") {
		t.Fatal("overlapping check on reconnect")
	}
	job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypePublicEgress), done.Add(time.Hour+time.Minute), time.Minute)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	result := resultFor(job, true)
	outcome := egressOutcome(true, false)
	result.ResolvedIP = ""
	result.Result = protocol.ProbeResult{PublicEgress: &outcome}
	finished := done.Add(time.Hour + time.Minute + time.Second)
	if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, result, finished); err != nil {
		t.Fatal(err)
	}
	// The cycle restarts from this check: due seven days after it.
	if ensureEgress(t, store, agentID, finished.Add(6*24*time.Hour), "session-3") {
		t.Fatal("dispatched inside the new cycle")
	}
	if !ensureEgress(t, store, agentID, finished.Add(7*24*time.Hour), "session-3") {
		t.Fatal("seven-day check missing")
	}
}

func TestPublicEgressFailureBackoffThenSevenDays(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(50_000, 0)
	negotiateEgress(t, store, agentID, at)
	if created, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil || !created {
		t.Fatal(created, err)
	}
	now := at.Add(time.Second)
	for attempt, want := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 7 * 24 * time.Hour} {
		finishEgressJob(t, store, agentID, now, egressOutcome(false, false))
		control := egressControl(t, store, agentID, now)
		if got := time.Duration(control.NextCheckAt-now.UnixMilli()) * time.Millisecond; got != want {
			t.Fatalf("failure %d: next check in %v, want %v", attempt+1, got, want)
		}
		if ensureEgress(t, store, agentID, now.Add(want-time.Second), "session-1") {
			t.Fatalf("failure %d: retried early", attempt+1)
		}
		now = now.Add(want)
		if !ensureEgress(t, store, agentID, now, "session-1") {
			t.Fatalf("failure %d: retry missing", attempt+1)
		}
	}
	// A success resets the sequence.
	finishEgressJob(t, store, agentID, now, egressOutcome(true, false))
	if control := egressControl(t, store, agentID, now); control.FailureStep != 0 || control.NextCheckAt != now.Add(7*24*time.Hour).UnixMilli() {
		t.Fatalf("control=%+v", control)
	}
}

func TestPublicEgressExpiredJobCountsAsFailure(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(55_000, 0)
	negotiateEgress(t, store, agentID, at)
	if created, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil || !created {
		t.Fatal(created, err)
	}
	late := at.Add(11 * time.Minute) // the job life is ten minutes and nobody claimed it
	if ensureEgress(t, store, agentID, late, "session-1") {
		t.Fatal("retried before the backoff")
	}
	if control := egressControl(t, store, agentID, late); control.FailureStep != 1 || control.NextCheckAt != late.Add(time.Minute).UnixMilli() {
		t.Fatalf("control=%+v", control)
	}
	if !ensureEgress(t, store, agentID, late.Add(time.Minute), "session-1") {
		t.Fatal("backoff retry missing")
	}
}

func TestPublicEgressAutomaticChecksIgnoreTheManualCooldown(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(60_000, 0)
	negotiateEgress(t, store, agentID, at)
	if created, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil || !created {
		t.Fatal(created, err)
	}
	finishEgressJob(t, store, agentID, at.Add(time.Second), egressOutcome(false, false)) // failure: retry in 1 minute
	retry := at.Add(2 * time.Minute)
	if control := egressControl(t, store, agentID, retry); control.CooldownUntil <= retry.UnixMilli() {
		t.Fatalf("cooldown is not active: %+v", control)
	}
	if !ensureEgress(t, store, agentID, retry, "session-1") {
		t.Fatal("failure retry was blocked by the manual cooldown")
	}
	finishEgressJob(t, store, agentID, retry, egressOutcome(true, false))
	if !ensureEgress(t, store, agentID, retry.Add(time.Minute), "session-2") {
		t.Fatal("reconnect was blocked by the manual cooldown")
	}
}

func TestPublicEgressManualCooldownSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress.db")
	store, agentID, _ := testStore(t, path)
	ctx := context.Background()
	at := time.Unix(70_000, 0)
	negotiateEgress(t, store, agentID, at)
	if _, err := store.SetPublicEgressEnabled(ctx, agentID, true, at.Add(48*time.Hour), egressOffline); err == nil {
		// The Agent is long offline, so nothing is dispatched, but the switch is stored.
		t.Log("enabled while offline")
	}
	// Online again.
	start := at.Add(48 * time.Hour)
	egressReport(t, store, agentID, start, "session-1", 1, nil)
	if err := store.CreateManualPublicEgressJob(ctx, agentID, start, egressOffline); err != nil {
		t.Fatalf("first manual check: %v", err)
	}
	finishEgressJob(t, store, agentID, start.Add(time.Second), egressOutcome(true, false))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	second := start.Add(time.Hour)
	egressReport(t, store, agentID, second, "session-1", 1, nil)
	var cooldown *PublicEgressCooldownError
	err = store.CreateManualPublicEgressJob(ctx, agentID, second, egressOffline)
	if !errors.As(err, &cooldown) || cooldown.Until != start.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("second manual check err=%v", err)
	}
	// Exactly 24 hours after the first job's creation it is allowed again, whatever the first outcome.
	later := start.Add(24 * time.Hour)
	egressReport(t, store, agentID, later, "session-1", 1, nil)
	if err := store.CreateManualPublicEgressJob(ctx, agentID, later.Add(-time.Millisecond), egressOffline); !errors.As(err, &cooldown) {
		t.Fatalf("one millisecond early err=%v", err)
	}
	if err := store.CreateManualPublicEgressJob(ctx, agentID, later, egressOffline); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
}

func TestPublicEgressManualCheckRejections(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(80_000, 0)
	egressReport(t, store, agentID, at, "session-1", 1, nil)
	manual := func(now time.Time) error { return store.CreateManualPublicEgressJob(ctx, agentID, now, egressOffline) }
	if err := manual(at); !errors.Is(err, ErrPublicEgressDisabled) {
		t.Fatalf("switch off err=%v", err)
	}
	if _, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil {
		t.Fatal(err)
	}
	if err := manual(at); !errors.Is(err, ErrPublicEgressUnsupported) {
		t.Fatalf("legacy Agent err=%v", err)
	}
	if ok, err := store.NegotiatePublicEgressCapability(ctx, agentID, true, 1, "session-1", at); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := manual(at.Add(time.Minute)); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("offline err=%v", err)
	}
	if err := manual(at); err != nil {
		t.Fatalf("manual: %v", err)
	}
	// The enable above could not dispatch (not supported yet); this manual job is the first and is pending.
	if err := manual(at); !errors.Is(err, ErrPublicEgressPending) {
		t.Fatalf("pending err=%v", err)
	}
	if changed, err := store.DisableAgent(ctx, agentID, at); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if err := manual(at); !errors.Is(err, ErrAgentDisabled) {
		t.Fatalf("paused err=%v", err)
	}
	if err := store.CreateManualPublicEgressJob(ctx, "missing-agent", at, egressOffline); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("unknown err=%v", err)
	}
}

func TestPublicEgressManualCheckQueueFull(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(85_000, 0)
	negotiateEgress(t, store, agentID, at)
	if _, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil {
		t.Fatal(err)
	}
	finishEgressJob(t, store, agentID, at.Add(time.Second), egressOutcome(true, false))
	for i := 0; i < MaxOutstandingJobsPerAgent; i++ {
		params := oneShot("fill-"+string(rune('a'+i%26))+string(rune('a'+i/26)), agentID, at.Add(25*time.Hour), protocol.ProbeTypeTCPConnect)
		if err := store.CreateOneShotJob(ctx, params); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	later := at.Add(25 * time.Hour)
	egressReport(t, store, agentID, later, "session-1", 1, nil)
	if err := store.CreateManualPublicEgressJob(ctx, agentID, later, egressOffline); !errors.Is(err, ErrOutstandingJobsFull) {
		t.Fatalf("queue full err=%v", err)
	}
}

func TestPublicEgressResultValidityIsEightDays(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(90_000, 0)
	negotiateEgress(t, store, agentID, at)
	if _, err := store.SetPublicEgressEnabled(ctx, agentID, true, at, egressOffline); err != nil {
		t.Fatal(err)
	}
	done := at.Add(time.Second)
	finishEgressJob(t, store, agentID, done, egressOutcome(true, false))
	state := egressState(t, store, agentID, done)
	if !state.Supported || state.IPv4.Status != "available" || state.IPv4.IP != "8.8.8.8" || state.IPv4.CheckedAt != done.UnixMilli() {
		t.Fatalf("state=%+v", state)
	}
	if state.IPv4.ValidUntil-state.IPv4.CheckedAt != (8 * 24 * time.Hour).Milliseconds() {
		t.Fatalf("success validity %v", time.Duration(state.IPv4.ValidUntil-state.IPv4.CheckedAt)*time.Millisecond)
	}
	if state.IPv6.ValidUntil-state.IPv6.CheckedAt != protocol.EgressFailureTTL.Milliseconds() {
		t.Fatalf("failure validity %v", time.Duration(state.IPv6.ValidUntil-state.IPv6.CheckedAt)*time.Millisecond)
	}
	// Ordinary reports from a current Agent (no telemetry) do not erase the stored result.
	egressReport(t, store, agentID, done.Add(time.Minute), "session-1", 1, nil)
	if state := egressState(t, store, agentID, done.Add(time.Minute)); state.IPv4.IP != "8.8.8.8" {
		t.Fatalf("a plain report erased the result: %+v", state)
	}
	// Switching off hides it; the stored result is not shown while off.
	if _, err := store.SetPublicEgressEnabled(ctx, agentID, false, done.Add(2*time.Minute), egressOffline); err != nil {
		t.Fatal(err)
	}
	if state := egressState(t, store, agentID, done.Add(2*time.Minute)); state.IPv4.IP != "" {
		t.Fatalf("result still visible while off: %+v", state)
	}
}

func TestPublicEgressResultRejectsNonPublicAddresses(t *testing.T) {
	for _, ip := range []string{"10.0.0.8", "192.168.1.1", "127.0.0.1", "100.64.0.1", "169.254.1.1", "203.0.113.9", ""} {
		result := protocol.PublicEgressResult{IPv4: egressObservation("ipv4", "available", ip, "verified"), IPv6: egressObservation("ipv6", "unavailable", "", "no_route")}
		if err := result.Validate(); err == nil {
			t.Errorf("address %q was accepted", ip)
		}
	}
}
