package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

func TestQueryAgentsPaginationStatusAndDetail(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	created := time.Unix(1_000, 0)
	now := time.Unix(2_000, 0)
	offlineTimeout := time.Minute
	for _, value := range []struct {
		id   string
		name string
		at   time.Time
	}{
		{"agent-a", "alpha", created},
		{"agent-b", "beta", created.Add(time.Second)},
		{"agent-c", "charlie", created.Add(time.Second)},
		{"agent-d", "delta", created.Add(2 * time.Second)},
		{"agent-e", "echo", created.Add(3 * time.Second)},
	} {
		addQueryAgent(t, store, value.id, value.name, value.at)
	}
	writeQueryAgentState(t, store, "agent-a", now.Add(-offlineTimeout-time.Millisecond))
	writeQueryAgentState(t, store, "agent-b", now.Add(-offlineTimeout))
	writeQueryAgentState(t, store, "agent-c", now.Add(-30*time.Second))
	writeQueryAgentState(t, store, "agent-d", now.Add(-time.Second))
	if changed, err := store.RevokeAgent(ctx, "agent-d", now); err != nil || !changed {
		t.Fatalf("revoke changed=%t err=%v", changed, err)
	}

	first, next, err := store.QueryAgents(ctx, AgentQuery{Limit: 2, Now: now, OfflineTimeout: offlineTimeout})
	if err != nil || agentSnapshotIDs(first) != "agent-e,agent-d" || next == nil || next.ID != "agent-d" {
		t.Fatalf("first=%s next=%+v err=%v", agentSnapshotIDs(first), next, err)
	}
	second, next, err := store.QueryAgents(ctx, AgentQuery{Limit: 2, After: next, Now: now, OfflineTimeout: offlineTimeout})
	if err != nil || agentSnapshotIDs(second) != "agent-c,agent-b" || next == nil || next.ID != "agent-b" {
		t.Fatalf("second=%s next=%+v err=%v", agentSnapshotIDs(second), next, err)
	}
	third, next, err := store.QueryAgents(ctx, AgentQuery{Limit: 2, After: next, Now: now, OfflineTimeout: offlineTimeout})
	if err != nil || agentSnapshotIDs(third) != "agent-a" || next != nil {
		t.Fatalf("third=%s next=%+v err=%v", agentSnapshotIDs(third), next, err)
	}

	assertAgentQueryIDs(t, store, AgentQuery{Status: AgentQueryStatusOnline, Limit: 10, Now: now, OfflineTimeout: offlineTimeout}, "agent-c,agent-b")
	assertAgentQueryIDs(t, store, AgentQuery{Status: AgentQueryStatusOffline, Limit: 10, Now: now, OfflineTimeout: offlineTimeout}, "agent-e,agent-a")
	assertAgentQueryIDs(t, store, AgentQuery{Status: AgentQueryStatusActive, Limit: 10, Now: now, OfflineTimeout: offlineTimeout}, "agent-e,agent-c,agent-b,agent-a")
	assertAgentQueryIDs(t, store, AgentQuery{Status: AgentQueryStatusRevoked, Limit: 10, Now: now, OfflineTimeout: offlineTimeout}, "agent-d")

	detail, err := store.GetAgentSnapshot(ctx, "agent-d", now, offlineTimeout)
	if err != nil || !detail.Agent.Revoked || detail.Online || detail.State == nil || detail.State.LastSeen != now.Add(-time.Second).UnixMilli() {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	neverReported, err := store.GetAgentSnapshot(ctx, "agent-e", now, offlineTimeout)
	if err != nil || neverReported.State != nil || neverReported.Online {
		t.Fatalf("never reported=%+v err=%v", neverReported, err)
	}
	if _, err := store.GetAgentSnapshot(ctx, "missing", now, offlineTimeout); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("missing detail error=%v", err)
	}

	for _, query := range []AgentQuery{
		{Limit: 0, Now: now, OfflineTimeout: offlineTimeout},
		{Limit: 1, Now: now, OfflineTimeout: 0},
		{Limit: 1, Now: time.UnixMilli(0), OfflineTimeout: offlineTimeout},
		{Status: "unknown", Limit: 1, Now: now, OfflineTimeout: offlineTimeout},
		{Limit: 1, After: &CollectionPageKey{CreatedAt: 0, ID: "agent-a"}, Now: now, OfflineTimeout: offlineTimeout},
	} {
		if _, _, err := store.QueryAgents(ctx, query); err == nil {
			t.Fatalf("invalid query accepted: %+v", query)
		}
	}
}

func TestQueryProbeSchedulesPaginationAndFilters(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	created := time.Unix(3_000, 0)
	addQueryAgent(t, store, "agent-a", "alpha", created)
	addQueryAgent(t, store, "agent-b", "beta", created)

	putQuerySchedule(t, store, "schedule-a", "agent-a", protocol.ProbeTypeHTTP, true, created)
	putQuerySchedule(t, store, "schedule-b", "agent-a", protocol.ProbeTypeTCPConnect, true, created.Add(time.Second))
	putQuerySchedule(t, store, "schedule-c", "agent-a", protocol.ProbeTypeHTTP, false, created.Add(time.Second))
	putQuerySchedule(t, store, "schedule-d", "agent-b", protocol.ProbeTypeICMPPing, true, created.Add(2*time.Second))

	first, next, err := store.QueryProbeSchedules(ctx, ProbeScheduleQuery{Limit: 2})
	if err != nil || scheduleRecordIDs(first) != "schedule-d,schedule-c" || next == nil || next.ID != "schedule-c" {
		t.Fatalf("first=%s next=%+v err=%v", scheduleRecordIDs(first), next, err)
	}
	second, next, err := store.QueryProbeSchedules(ctx, ProbeScheduleQuery{Limit: 2, After: next})
	if err != nil || scheduleRecordIDs(second) != "schedule-b,schedule-a" || next != nil {
		t.Fatalf("second=%s next=%+v err=%v", scheduleRecordIDs(second), next, err)
	}
	assertScheduleQueryIDs(t, store, ProbeScheduleQuery{AgentID: "agent-a", Limit: 10}, "schedule-c,schedule-b,schedule-a")
	disabled := false
	assertScheduleQueryIDs(t, store, ProbeScheduleQuery{Enabled: &disabled, Limit: 10}, "schedule-c")
	assertScheduleQueryIDs(t, store, ProbeScheduleQuery{ProbeType: protocol.ProbeTypeHTTP, Limit: 10}, "schedule-c,schedule-a")
	enabled := true
	assertScheduleQueryIDs(t, store, ProbeScheduleQuery{AgentID: "agent-a", Enabled: &enabled, ProbeType: protocol.ProbeTypeTCPConnect, Limit: 10}, "schedule-b")
	empty, next, err := store.QueryProbeSchedules(ctx, ProbeScheduleQuery{AgentID: "missing", Limit: 10})
	if err != nil || empty == nil || len(empty) != 0 || next != nil {
		t.Fatalf("empty=%v next=%+v err=%v", empty, next, err)
	}
	for _, query := range []ProbeScheduleQuery{
		{Limit: 0},
		{Limit: MaxCollectionPageLimit + 1},
		{AgentID: "", ProbeType: "invalid", Limit: 1},
		{Limit: 1, After: &CollectionPageKey{CreatedAt: created.UnixMilli(), ID: ""}},
	} {
		if _, _, err := store.QueryProbeSchedules(ctx, query); err == nil {
			t.Fatalf("invalid query accepted: %+v", query)
		}
	}
}

func TestQueryProbeJobsPaginationFiltersSummaryAndExpiry(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	now := time.Unix(10_000, 0)

	finishQueryJob(t, store, agentID, "job-success", now.Add(-5*time.Minute), protocol.ProbeTypeTCPConnect, true, now.Add(-4*time.Minute))
	finishQueryJob(t, store, agentID, "job-failure", now.Add(-4*time.Minute), protocol.ProbeTypeTCPConnect, false, now.Add(-3*time.Minute+time.Second))

	scheduleAt := now.Add(-3 * time.Minute)
	putQuerySchedule(t, store, "schedule-job", agentID, protocol.ProbeTypeHTTP, true, scheduleAt)
	outcome, err := store.MaterializeSchedule(ctx, "schedule-job", scheduleAt, time.Minute)
	if err != nil || outcome.Result != MaterializeCreated {
		t.Fatalf("materialize=%+v err=%v", outcome, err)
	}
	scheduledJob, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypeHTTP), scheduleAt, time.Minute)
	if err != nil || scheduledJob == nil || scheduledJob.JobID != outcome.JobID {
		t.Fatalf("scheduled claim=%+v outcome=%+v err=%v", scheduledJob, outcome, err)
	}
	if _, err := store.SubmitJobResult(ctx, agentID, scheduledJob.JobID, resultFor(scheduledJob, true), scheduleAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	leasedAt := now.Add(-2 * time.Minute)
	if err := store.CreateOneShotJob(ctx, oneShot("job-leased", agentID, leasedAt, protocol.ProbeTypeICMPPing)); err != nil {
		t.Fatal(err)
	}
	leased, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-lease", protocol.ProbeTypeICMPPing), leasedAt, 10*time.Minute)
	if err != nil || leased == nil || leased.JobID != "job-leased" {
		t.Fatalf("leased=%+v err=%v", leased, err)
	}
	queuedAt := now.Add(-time.Minute)
	for _, id := range []string{"job-queued-a", "job-queued-b"} {
		if err := store.CreateOneShotJob(ctx, oneShot(id, agentID, queuedAt, protocol.ProbeTypeHTTP)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateOneShotJob(ctx, oneShot("job-expired", agentID, now.Add(-20*time.Minute), protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}

	first, next, err := store.QueryProbeJobs(ctx, ProbeJobQuery{Limit: 3}, now)
	if err != nil || jobListRecordIDs(first) != "job-queued-b,job-queued-a,job-leased" || next == nil || next.ID != "job-leased" {
		t.Fatalf("first=%s next=%+v err=%v", jobListRecordIDs(first), next, err)
	}
	second, next, err := store.QueryProbeJobs(ctx, ProbeJobQuery{Limit: 3, After: next}, now)
	if err != nil || jobListRecordIDs(second) != outcome.JobID+",job-failure,job-success" || next == nil || next.ID != "job-success" {
		t.Fatalf("second=%s next=%+v err=%v", jobListRecordIDs(second), next, err)
	}
	third, next, err := store.QueryProbeJobs(ctx, ProbeJobQuery{Limit: 3, After: next}, now)
	if err != nil || jobListRecordIDs(third) != "job-expired" || next != nil || third[0].Job.Status != JobStatusExpired {
		t.Fatalf("third=%s next=%+v err=%v", jobListRecordIDs(third), next, err)
	}
	for _, record := range append(append(first, second...), third...) {
		if record.Job.Status == JobStatusFinished {
			if record.ResultSummary == nil || record.ResultSummary.ReceivedAt != record.Job.FinishedAt || record.ResultSummary.FinishedAt != 1_025 {
				t.Fatalf("finished summary=%+v job=%+v", record.ResultSummary, record.Job)
			}
		} else if record.ResultSummary != nil {
			t.Fatalf("unfinished summary=%+v job=%+v", record.ResultSummary, record.Job)
		}
	}

	assertJobQueryIDs(t, store, ProbeJobQuery{AgentID: agentID, Limit: 20}, now,
		"job-queued-b,job-queued-a,job-leased,"+outcome.JobID+",job-failure,job-success,job-expired")
	assertJobQueryIDs(t, store, ProbeJobQuery{ScheduleID: "schedule-job", Limit: 20}, now, outcome.JobID)
	assertJobQueryIDs(t, store, ProbeJobQuery{ProbeType: protocol.ProbeTypeHTTP, Limit: 20}, now,
		"job-queued-b,job-queued-a,"+outcome.JobID)
	assertJobQueryIDs(t, store, ProbeJobQuery{Status: JobStatusLeased, Limit: 20}, now, "job-leased")
	success := true
	assertJobQueryIDs(t, store, ProbeJobQuery{Success: &success, Limit: 20}, now, outcome.JobID+",job-success")
	success = false
	failures, _, err := store.QueryProbeJobs(ctx, ProbeJobQuery{Success: &success, Limit: 20}, now)
	if err != nil || jobListRecordIDs(failures) != "job-failure" || failures[0].ResultSummary == nil || failures[0].ResultSummary.ErrorCategory != "connection_refused" {
		t.Fatalf("failures=%+v err=%v", failures, err)
	}
	assertJobQueryIDs(t, store, ProbeJobQuery{
		CreatedAfter: now.Add(-5 * time.Minute).UnixMilli(), CreatedBefore: now.Add(-2 * time.Minute).UnixMilli(), Limit: 20,
	}, now, outcome.JobID+",job-failure")
	assertJobQueryIDs(t, store, ProbeJobQuery{
		FinishedAfter: now.Add(-4 * time.Minute).UnixMilli(), FinishedBefore: now.Add(-2 * time.Minute).UnixMilli(), Limit: 20,
	}, now, outcome.JobID+",job-failure")

	invalidSuccess := true
	for _, query := range []ProbeJobQuery{
		{Limit: 0},
		{Limit: 1, Status: "invalid"},
		{Limit: 1, ProbeType: "invalid"},
		{Limit: 1, Status: JobStatusQueued, Success: &invalidSuccess},
		{Limit: 1, CreatedAfter: 20, CreatedBefore: 10},
		{Limit: 1, FinishedAfter: 20, FinishedBefore: 20},
		{Limit: 1, After: &CollectionPageKey{CreatedAt: -1, ID: "job"}},
	} {
		if _, _, err := store.QueryProbeJobs(ctx, query, now); err == nil {
			t.Fatalf("invalid query accepted: %+v", query)
		}
	}
	if _, _, err := store.QueryProbeJobs(ctx, ProbeJobQuery{Limit: 1}, time.UnixMilli(0)); err == nil {
		t.Fatal("invalid snapshot time accepted")
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE probe_results SET result_hash=zeroblob(32) WHERE job_id='job-failure'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.QueryProbeJobs(ctx, ProbeJobQuery{Status: JobStatusFinished, Limit: 20}, now); !errors.Is(err, ErrCorruptProbeData) {
		t.Fatalf("corrupt result error=%v", err)
	}
}

func TestQueryProbeJobsNormalizesExpiredLeases(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	now := time.Unix(20_000, 0)

	expiredAt := now.Add(-20 * time.Minute)
	if err := store.CreateOneShotJob(ctx, oneShot("job-expired-lease", agentID, expiredAt, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	if job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-expired", protocol.ProbeTypeTCPConnect), expiredAt, time.Minute); err != nil || job == nil {
		t.Fatalf("expired lease=%+v err=%v", job, err)
	}
	requeueAt := now.Add(-2 * time.Minute)
	if err := store.CreateOneShotJob(ctx, oneShot("job-requeue", agentID, requeueAt, protocol.ProbeTypeHTTP)); err != nil {
		t.Fatal(err)
	}
	if job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-requeue", protocol.ProbeTypeHTTP), requeueAt, time.Minute); err != nil || job == nil || job.JobID != "job-requeue" {
		t.Fatalf("requeue lease=%+v err=%v", job, err)
	}

	records, next, err := store.QueryProbeJobs(ctx, ProbeJobQuery{AgentID: agentID, Limit: 10}, now)
	if err != nil || next != nil || jobListRecordIDs(records) != "job-requeue,job-expired-lease" {
		t.Fatalf("records=%+v next=%+v err=%v", records, next, err)
	}
	if records[0].Job.Status != JobStatusQueued || records[0].Job.Attempt != 1 || records[0].Job.LeaseToken != "" || records[0].Job.LeasedAt != 0 || records[0].Job.LeaseUntil != 0 {
		t.Fatalf("requeued job=%+v", records[0].Job)
	}
	if records[1].Job.Status != JobStatusExpired || records[1].Job.LeaseToken != "" || records[1].ResultSummary != nil {
		t.Fatalf("expired job=%+v summary=%+v", records[1].Job, records[1].ResultSummary)
	}
}

func addQueryAgent(t *testing.T, store *Store, id, name string, created time.Time) {
	t.Helper()
	if err := store.AddAgent(context.Background(), id, name, auth.Hash("token-"+id), created); err != nil {
		t.Fatal(err)
	}
}

func writeQueryAgentState(t *testing.T, store *Store, agentID string, received time.Time) {
	t.Helper()
	report := validReport(agentID, 1, "session-"+agentID, "boot-"+agentID, 1, 100, 200)
	report.CollectedAt = received.Add(-time.Second).UnixMilli()
	if _, accepted, _, err := store.ProcessReport(context.Background(), agentID, report, received); err != nil || !accepted {
		t.Fatalf("agent=%s accepted=%t err=%v", agentID, accepted, err)
	}
}

func putQuerySchedule(t *testing.T, store *Store, id, agentID string, probeType protocol.ProbeType, enabled bool, created time.Time) {
	t.Helper()
	params := scheduleParams(id, agentID, created)
	params.ProbeType = probeType
	params.Enabled = enabled
	switch probeType {
	case protocol.ProbeTypeHTTP:
		params.Config = protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com/", Method: "GET"}}
	case protocol.ProbeTypeTCPConnect:
		params.Config = protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}}
	case protocol.ProbeTypeICMPPing:
		params.Config = protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "192.0.2.1", Count: 2}}
	}
	if _, wasCreated, err := store.PutProbeSchedule(context.Background(), params); err != nil || !wasCreated {
		t.Fatalf("schedule=%s created=%t err=%v", id, wasCreated, err)
	}
}

func finishQueryJob(t *testing.T, store *Store, agentID, jobID string, created time.Time, probeType protocol.ProbeType, success bool, received time.Time) {
	t.Helper()
	if err := store.CreateOneShotJob(context.Background(), oneShot(jobID, agentID, created, probeType)); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimJob(context.Background(), agentID, claimRequest(1, "session-1", probeType), created, 2*time.Minute)
	if err != nil || job == nil || job.JobID != jobID {
		t.Fatalf("claim job=%+v want=%s err=%v", job, jobID, err)
	}
	if _, err := store.SubmitJobResult(context.Background(), agentID, jobID, resultFor(job, success), received); err != nil {
		t.Fatal(err)
	}
}

func assertAgentQueryIDs(t *testing.T, store *Store, query AgentQuery, want string) {
	t.Helper()
	records, next, err := store.QueryAgents(context.Background(), query)
	if err != nil || agentSnapshotIDs(records) != want || next != nil {
		t.Fatalf("agents=%s want=%s next=%+v err=%v", agentSnapshotIDs(records), want, next, err)
	}
}

func assertScheduleQueryIDs(t *testing.T, store *Store, query ProbeScheduleQuery, want string) {
	t.Helper()
	records, next, err := store.QueryProbeSchedules(context.Background(), query)
	if err != nil || scheduleRecordIDs(records) != want || next != nil {
		t.Fatalf("schedules=%s want=%s next=%+v err=%v", scheduleRecordIDs(records), want, next, err)
	}
}

func assertJobQueryIDs(t *testing.T, store *Store, query ProbeJobQuery, now time.Time, want string) {
	t.Helper()
	records, next, err := store.QueryProbeJobs(context.Background(), query, now)
	if err != nil || jobListRecordIDs(records) != want || next != nil {
		t.Fatalf("jobs=%s want=%s next=%+v err=%v", jobListRecordIDs(records), want, next, err)
	}
}

func agentSnapshotIDs(records []AgentSnapshot) string {
	values := make([]string, len(records))
	for i, record := range records {
		values[i] = record.Agent.ID
	}
	return strings.Join(values, ",")
}

func scheduleRecordIDs(records []ProbeScheduleRecord) string {
	values := make([]string, len(records))
	for i, record := range records {
		values[i] = record.ID
	}
	return strings.Join(values, ",")
}

func jobListRecordIDs(records []ProbeJobListRecord) string {
	values := make([]string, len(records))
	for i, record := range records {
		values[i] = record.Job.ID
	}
	return strings.Join(values, ",")
}
