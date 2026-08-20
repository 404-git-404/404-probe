package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	_ "modernc.org/sqlite"
)

func claimRequest(epoch uint64, session string, supported ...protocol.ProbeType) protocol.ClaimRequest {
	return protocol.ClaimRequest{
		ProtocolVersion:     protocol.JobProtocolVersion,
		AgentEpoch:          epoch,
		SessionID:           session,
		SupportedProbeTypes: supported,
	}
}

func oneShot(id, agentID string, at time.Time, probeType protocol.ProbeType) CreateOneShotJobParams {
	params := CreateOneShotJobParams{
		ID: id, AgentID: agentID, ProbeType: probeType, TimeoutMS: 5000,
		CreatedAt: at.UnixMilli(), NotBefore: at.UnixMilli(), ExpiresAt: at.Add(10 * time.Minute).UnixMilli(),
	}
	switch probeType {
	case protocol.ProbeTypeICMPPing:
		params.Config = protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "1.1.1.1", Count: 4}}
	case protocol.ProbeTypeTCPConnect:
		params.Config = protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}}
	case protocol.ProbeTypeHTTP:
		params.Config = protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com/health", Method: "GET"}}
	}
	return params
}

func resultFor(job *protocol.Job, success bool) protocol.JobResult {
	result := protocol.JobResult{
		ProtocolVersion: protocol.JobProtocolVersion,
		LeaseToken:      job.LeaseToken,
		Attempt:         job.Attempt,
		AgentEpoch:      1,
		SessionID:       "session-1",
		StartedAt:       1_000,
		FinishedAt:      1_025,
		DurationMS:      25,
		Success:         success,
		ResolvedIP:      "192.0.2.1",
	}
	if !success {
		result.ErrorCategory = "connection_refused"
		result.ErrorMessage = "connection refused"
	}
	switch job.ProbeType {
	case protocol.ProbeTypeICMPPing:
		result.Result = protocol.ProbeResult{ICMPPing: &protocol.ICMPPingResult{Sent: 4, Received: 4, LatencyMinMS: 1, LatencyAvgMS: 2, LatencyMaxMS: 3}}
	case protocol.ProbeTypeTCPConnect:
		result.Result = protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{ConnectMS: 20}}
	case protocol.ProbeTypeHTTP:
		result.Result = protocol.ProbeResult{HTTP: &protocol.HTTPResult{DNSMS: 1, ConnectMS: 2, TLSMS: 3, TTFBMS: 20, TotalMS: 25, StatusCode: 200}}
	}
	return result
}

func TestCreateOneShotJob(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(context.Background(), oneShot("job-1", agentID, at, protocol.ProbeTypeICMPPing)); err != nil {
		t.Fatal(err)
	}
	job, err := store.GetProbeJob(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobStatusQueued || job.Attempt != 0 || job.ScheduleID != "" || job.Config.ICMPPing == nil || job.Config.ICMPPing.Count != 4 {
		t.Fatalf("stored job=%+v", job)
	}
}

func TestClaimAndDuplicateClaimReplay(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(ctx, oneShot("job-1", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	request := claimRequest(7, "session-7", protocol.ProbeTypeTCPConnect)
	first, err := store.ClaimJob(ctx, agentID, request, at, 30*time.Second)
	if err != nil || first == nil {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	second, err := store.ClaimJob(ctx, agentID, request, at.Add(time.Second), 30*time.Second)
	if err != nil || second == nil {
		t.Fatalf("replayed claim=%+v err=%v", second, err)
	}
	if second.JobID != first.JobID || second.LeaseToken != first.LeaseToken || second.Attempt != 1 || second.LeaseExpiresAt != first.LeaseExpiresAt {
		t.Fatalf("claim replay changed lease: first=%+v second=%+v", first, second)
	}
	record, err := store.GetProbeJob(ctx, first.JobID)
	if err != nil || record.LeaseEpoch != 7 || record.LeaseSessionID != "session-7" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestConcurrentClaimsAndOneActiveLease(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)
	for _, id := range []string{"job-1", "job-2"} {
		if err := store.CreateOneShotJob(ctx, oneShot(id, agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
			t.Fatal(err)
		}
	}
	type response struct {
		job *protocol.Job
		err error
	}
	start := make(chan struct{})
	responses := make(chan response, 2)
	var wait sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wait.Add(1)
		go func(epoch uint64) {
			defer wait.Done()
			<-start
			job, err := store.ClaimJob(ctx, agentID, claimRequest(epoch, fmt.Sprintf("session-%d", epoch), protocol.ProbeTypeTCPConnect), at, time.Minute)
			responses <- response{job: job, err: err}
		}(uint64(i))
	}
	close(start)
	wait.Wait()
	close(responses)
	claimed := 0
	for response := range responses {
		if response.err != nil {
			t.Fatal(response.err)
		}
		if response.job != nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent claimed=%d want 1", claimed)
	}
	var leases int
	if err := store.db.QueryRow(`SELECT count(*) FROM probe_jobs WHERE agent_id=? AND status='leased'`, agentID).Scan(&leases); err != nil || leases != 1 {
		t.Fatalf("active leases=%d err=%v", leases, err)
	}
}

func TestDifferentSessionCannotClaimWhileLeaseIsActive(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)
	for _, id := range []string{"job-1", "job-2"} {
		if err := store.CreateOneShotJob(ctx, oneShot(id, agentID, at, protocol.ProbeTypeHTTP)); err != nil {
			t.Fatal(err)
		}
	}
	if job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "first", protocol.ProbeTypeHTTP), at, time.Minute); err != nil || job == nil {
		t.Fatalf("first claim=%+v err=%v", job, err)
	}
	if job, err := store.ClaimJob(ctx, agentID, claimRequest(2, "second", protocol.ProbeTypeHTTP), at.Add(time.Second), time.Minute); err != nil || job != nil {
		t.Fatalf("second session claim=%+v err=%v", job, err)
	}
}

func TestClaimOnlyReturnsSupportedProbeType(t *testing.T) {
	for name, capabilities := range map[string][]protocol.ProbeType{
		"tcp-first": {protocol.ProbeTypeTCPConnect, protocol.ProbeTypeHTTP},
		"tcp-last":  {protocol.ProbeTypeHTTP, protocol.ProbeTypeTCPConnect},
	} {
		t.Run(name, func(t *testing.T) {
			store, agentID, _ := testStore(t, ":memory:")
			defer store.Close()
			ctx := context.Background()
			at := time.Unix(1000, 0)
			if err := store.CreateOneShotJob(ctx, oneShot("a-ping", agentID, at, protocol.ProbeTypeICMPPing)); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateOneShotJob(ctx, oneShot("b-tcp", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
				t.Fatal(err)
			}
			job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "capable", capabilities...), at, time.Minute)
			if err != nil || job == nil {
				t.Fatalf("claim=%+v err=%v", job, err)
			}
			if job.JobID != "b-tcp" || job.ProbeType != protocol.ProbeTypeTCPConnect {
				t.Fatalf("unsupported probe was claimed: %+v", job)
			}
			ping, err := store.GetProbeJob(ctx, "a-ping")
			if err != nil || ping.Status != JobStatusQueued {
				t.Fatalf("unsupported queued job changed: %+v err=%v", ping, err)
			}
		})
	}
}

func TestLeaseTimeoutRequeuesOrExpires(t *testing.T) {
	t.Run("requeue", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
			t.Fatal(err)
		}
		first, err := store.ClaimJob(ctx, agentID, claimRequest(1, "first", protocol.ProbeTypeTCPConnect), at, time.Second)
		if err != nil || first == nil {
			t.Fatal(err)
		}
		second, err := store.ClaimJob(ctx, agentID, claimRequest(2, "second", protocol.ProbeTypeTCPConnect), at.Add(2*time.Second), time.Second)
		if err != nil || second == nil {
			t.Fatalf("second=%+v err=%v", second, err)
		}
		if second.JobID != first.JobID || second.Attempt != 2 || second.LeaseToken == first.LeaseToken {
			t.Fatalf("lease was not renewed correctly: first=%+v second=%+v", first, second)
		}
	})

	t.Run("expire", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		params := oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)
		params.ExpiresAt = at.Add(1500 * time.Millisecond).UnixMilli()
		if err := store.CreateOneShotJob(ctx, params); err != nil {
			t.Fatal(err)
		}
		first, err := store.ClaimJob(ctx, agentID, claimRequest(1, "first", protocol.ProbeTypeTCPConnect), at, time.Second)
		if err != nil || first == nil {
			t.Fatal(err)
		}
		job, err := store.ClaimJob(ctx, agentID, claimRequest(2, "second", protocol.ProbeTypeTCPConnect), at.Add(2*time.Second), time.Second)
		if err != nil || job != nil {
			t.Fatalf("expired claim=%+v err=%v", job, err)
		}
		record, err := store.GetProbeJob(ctx, "job")
		if err != nil || record.Status != JobStatusExpired {
			t.Fatalf("record=%+v err=%v", record, err)
		}
		if _, err := store.SubmitJobResult(ctx, agentID, first.JobID, resultFor(first, true), at.Add(2*time.Second)); !errors.Is(err, ErrJobExpired) {
			t.Fatalf("expired result error=%v", err)
		}
	})
}

func TestSubmitSuccessAndProbeFailureAreFinished(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "probe_failure"}[success], func(t *testing.T) {
			store, agentID, _ := testStore(t, ":memory:")
			defer store.Close()
			ctx := context.Background()
			at := time.Unix(1000, 0)
			if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
				t.Fatal(err)
			}
			job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypeTCPConnect), at, time.Minute)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			ack, err := store.SubmitJobResult(ctx, agentID, job.JobID, resultFor(job, success), at.Add(time.Second))
			if err != nil || ack.Duplicate {
				t.Fatalf("ack=%+v err=%v", ack, err)
			}
			record, err := store.GetProbeJob(ctx, job.JobID)
			if err != nil || record.Status != JobStatusFinished {
				t.Fatalf("job=%+v err=%v", record, err)
			}
			stored, err := store.GetProbeResult(ctx, job.JobID)
			if err != nil || stored.Result.Success != success {
				t.Fatalf("result=%+v err=%v", stored, err)
			}
		})
	}
}

func TestResultDuplicateAndConflict(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session-1", protocol.ProbeTypeTCPConnect), at, time.Minute)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	result := resultFor(job, true)
	if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, result, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	equivalent := result
	equivalent.AgentEpoch = 999
	equivalent.SessionID = "diagnostic-metadata-changed"
	ack, err := store.SubmitJobResult(ctx, agentID, job.JobID, equivalent, at.Add(2*time.Second))
	if err != nil || !ack.Duplicate {
		t.Fatalf("duplicate ack=%+v err=%v", ack, err)
	}
	conflict := result
	conflict.Result.TCPConnect.ConnectMS++
	if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, conflict, at.Add(2*time.Second)); !errors.Is(err, ErrResultConflict) {
		t.Fatalf("conflicting result error=%v", err)
	}
}

func TestStaleLeaseAndWrongAgentResultsAreRejected(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	otherID, _ := auth.NewID()
	_, otherHash, _ := auth.NewToken()
	if err := store.AddAgent(ctx, otherID, "other", otherHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimJob(ctx, agentID, claimRequest(1, "first", protocol.ProbeTypeTCPConnect), at, time.Second)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitJobResult(ctx, otherID, first.JobID, resultFor(first, true), at.Add(500*time.Millisecond)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong agent error=%v", err)
	}
	second, err := store.ClaimJob(ctx, agentID, claimRequest(2, "second", protocol.ProbeTypeTCPConnect), at.Add(2*time.Second), time.Minute)
	if err != nil || second == nil {
		t.Fatal(err)
	}
	committed := resultFor(second, true)
	if _, err := store.SubmitJobResult(ctx, agentID, second.JobID, committed, at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitJobResult(ctx, agentID, first.JobID, resultFor(first, true), at.Add(3*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale identical result after newer commit error=%v", err)
	}
	wrongToken := committed
	wrongToken.LeaseToken = "wrong-lease-token"
	if _, err := store.SubmitJobResult(ctx, agentID, second.JobID, wrongToken, at.Add(3*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("same-attempt wrong-token duplicate error=%v", err)
	}
}

func TestClaimDoesNotReplayFinishedOrExpiredLease(t *testing.T) {
	t.Run("finished lease advances", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		for _, id := range []string{"a-first", "b-next"} {
			if err := store.CreateOneShotJob(ctx, oneShot(id, agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
				t.Fatal(err)
			}
		}
		request := claimRequest(1, "session-1", protocol.ProbeTypeTCPConnect)
		first, err := store.ClaimJob(ctx, agentID, request, at, time.Minute)
		if err != nil || first == nil || first.JobID != "a-first" {
			t.Fatalf("first=%+v err=%v", first, err)
		}
		if _, err := store.SubmitJobResult(ctx, agentID, first.JobID, resultFor(first, true), at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		next, err := store.ClaimJob(ctx, agentID, request, at.Add(2*time.Second), time.Minute)
		if err != nil || next == nil || next.JobID != "b-next" {
			t.Fatalf("claim after finish=%+v err=%v", next, err)
		}
	})

	t.Run("expired lease gets new attempt", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
			t.Fatal(err)
		}
		request := claimRequest(1, "same-session", protocol.ProbeTypeTCPConnect)
		first, err := store.ClaimJob(ctx, agentID, request, at, time.Second)
		if err != nil || first == nil {
			t.Fatalf("first=%+v err=%v", first, err)
		}
		second, err := store.ClaimJob(ctx, agentID, request, at.Add(time.Second), time.Second)
		if err != nil || second == nil {
			t.Fatalf("second=%+v err=%v", second, err)
		}
		if second.Attempt != first.Attempt+1 || second.LeaseToken == first.LeaseToken {
			t.Fatalf("expired lease replayed: first=%+v second=%+v", first, second)
		}
	})
}

func TestClaimCleansExpiredLeaseBeforeLeasingNextJob(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)
	jobA := oneShot("a-expired", agentID, at, protocol.ProbeTypeTCPConnect)
	jobA.ExpiresAt = at.Add(1500 * time.Millisecond).UnixMilli()
	if err := store.CreateOneShotJob(ctx, jobA); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimJob(ctx, agentID, claimRequest(1, "first", protocol.ProbeTypeTCPConnect), at, time.Second)
	if err != nil || first == nil || first.JobID != "a-expired" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if err := store.CreateOneShotJob(ctx, oneShot("b-queued", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimJob(ctx, agentID, claimRequest(2, "second", protocol.ProbeTypeTCPConnect), at.Add(2*time.Second), time.Minute)
	if err != nil || second == nil || second.JobID != "b-queued" {
		t.Fatalf("claim after cleanup=%+v err=%v", second, err)
	}
	expired, err := store.GetProbeJob(ctx, "a-expired")
	if err != nil || expired.Status != JobStatusExpired || expired.LeaseToken != "" || expired.LeaseUntil != 0 {
		t.Fatalf("expired lease not cleaned: %+v err=%v", expired, err)
	}
}

func TestJobAndLeaseExpiryBoundaries(t *testing.T) {
	t.Run("result after job expiry before lease expiry", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		params := oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)
		params.ExpiresAt = at.Add(10 * time.Second).UnixMilli()
		if err := store.CreateOneShotJob(ctx, params); err != nil {
			t.Fatal(err)
		}
		job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session", protocol.ProbeTypeTCPConnect), at, 40*time.Second)
		if err != nil || job == nil {
			t.Fatalf("claim=%+v err=%v", job, err)
		}
		if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, resultFor(job, true), at.Add(20*time.Second)); err != nil {
			t.Fatalf("result inside lease rejected: %v", err)
		}
	})

	t.Run("result exactly at lease expiry", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
			t.Fatal(err)
		}
		job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session", protocol.ProbeTypeTCPConnect), at, 30*time.Second)
		if err != nil || job == nil {
			t.Fatalf("claim=%+v err=%v", job, err)
		}
		if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, resultFor(job, true), at.Add(30*time.Second)); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("result at lease boundary error=%v", err)
		}
	})

	t.Run("claim exactly at job expiry", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		params := oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)
		params.ExpiresAt = at.Add(10 * time.Second).UnixMilli()
		if err := store.CreateOneShotJob(ctx, params); err != nil {
			t.Fatal(err)
		}
		job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session", protocol.ProbeTypeTCPConnect), at.Add(10*time.Second), time.Minute)
		if err != nil || job != nil {
			t.Fatalf("claim at expiry=%+v err=%v", job, err)
		}
		record, err := store.GetProbeJob(ctx, "job")
		if err != nil || record.Status != JobStatusExpired {
			t.Fatalf("record=%+v err=%v", record, err)
		}
	})
}

func TestAgentCannotClaimAnotherAgentsJob(t *testing.T) {
	store, agentA, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	agentB, _ := auth.NewID()
	_, hashB, _ := auth.NewToken()
	if err := store.AddAgent(ctx, agentB, "agent-b", hashB, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(ctx, oneShot("b-job", agentB, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimJob(ctx, agentA, claimRequest(1, "agent-a", protocol.ProbeTypeTCPConnect), at, time.Minute)
	if err != nil || job != nil {
		t.Fatalf("agent A claim=%+v err=%v", job, err)
	}
	record, err := store.GetProbeJob(ctx, "b-job")
	if err != nil || record.AgentID != agentB || record.Status != JobStatusQueued || record.Attempt != 0 {
		t.Fatalf("agent B job changed: %+v err=%v", record, err)
	}
}

func TestJobStateSurvivesStorageRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	store, agentID, _ := testStore(t, path)
	ctx := context.Background()
	at := time.Unix(1000, 0)
	expiredParams := oneShot("c-expired", agentID, at, protocol.ProbeTypeTCPConnect)
	expiredParams.ExpiresAt = at.Add(time.Second).UnixMilli()
	if err := store.CreateOneShotJob(ctx, expiredParams); err != nil {
		t.Fatal(err)
	}
	if job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "expire-sweep", protocol.ProbeTypeTCPConnect), at.Add(time.Second), time.Minute); err != nil || job != nil {
		t.Fatalf("expiry sweep claim=%+v err=%v", job, err)
	}
	for _, id := range []string{"a-finished", "b-leased", "d-queued"} {
		if err := store.CreateOneShotJob(ctx, oneShot(id, agentID, at.Add(2*time.Second), protocol.ProbeTypeTCPConnect)); err != nil {
			t.Fatal(err)
		}
	}
	leased, err := store.ClaimJob(ctx, agentID, claimRequest(2, "lease-finished", protocol.ProbeTypeTCPConnect), at.Add(2*time.Second), time.Minute)
	if err != nil || leased.JobID != "a-finished" {
		t.Fatalf("first claimed=%+v err=%v", leased, err)
	}
	if _, err := store.SubmitJobResult(ctx, agentID, leased.JobID, resultFor(leased, true), at.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	leased, err = store.ClaimJob(ctx, agentID, claimRequest(3, "lease-active", protocol.ProbeTypeTCPConnect), at.Add(3*time.Second), time.Minute)
	if err != nil || leased == nil {
		t.Fatal(err)
	}
	if leased.JobID != "b-leased" {
		t.Fatalf("second claimed=%+v", leased)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for id, want := range map[string]JobStatus{
		"a-finished": JobStatusFinished,
		"b-leased":   JobStatusLeased,
		"c-expired":  JobStatusExpired,
		"d-queued":   JobStatusQueued,
	} {
		record, err := store.GetProbeJob(ctx, id)
		if err != nil || record.Status != want {
			t.Fatalf("after restart job %s=%+v err=%v want=%s", id, record, err, want)
		}
	}
	if _, err := store.GetProbeResult(ctx, "a-finished"); err != nil {
		t.Fatalf("finished result lost after restart: %v", err)
	}
}

func TestQueuedJobExpiryAndRevokedAgent(t *testing.T) {
	t.Run("queued expiry", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		params := oneShot("job", agentID, at, protocol.ProbeTypeHTTP)
		params.ExpiresAt = at.Add(time.Second).UnixMilli()
		if err := store.CreateOneShotJob(ctx, params); err != nil {
			t.Fatal(err)
		}
		job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session", protocol.ProbeTypeHTTP), at.Add(time.Second), time.Minute)
		if err != nil || job != nil {
			t.Fatalf("claim=%+v err=%v", job, err)
		}
		record, _ := store.GetProbeJob(ctx, "job")
		if record.Status != JobStatusExpired {
			t.Fatalf("status=%s", record.Status)
		}
	})

	t.Run("revoked claim and submit", func(t *testing.T) {
		store, agentID, _ := testStore(t, ":memory:")
		defer store.Close()
		ctx := context.Background()
		at := time.Unix(1000, 0)
		if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
			t.Fatal(err)
		}
		job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session", protocol.ProbeTypeTCPConnect), at, time.Minute)
		if err != nil || job == nil {
			t.Fatal(err)
		}
		if changed, err := store.RevokeAgent(ctx, agentID, at); err != nil || !changed {
			t.Fatalf("revoke changed=%t err=%v", changed, err)
		}
		if _, err := store.ClaimJob(ctx, agentID, claimRequest(2, "new", protocol.ProbeTypeTCPConnect), at.Add(time.Second), time.Minute); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("revoked claim error=%v", err)
		}
		if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, resultFor(job, true), at.Add(time.Second)); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("revoked submit error=%v", err)
		}
	})
}

func TestJobIntegerBoundaries(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	params := CreateOneShotJobParams{
		ID: "max", AgentID: agentID, ProbeType: protocol.ProbeTypeTCPConnect,
		Config:    protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}},
		TimeoutMS: 5000, CreatedAt: math.MaxInt64 - 2, NotBefore: math.MaxInt64 - 1, ExpiresAt: math.MaxInt64,
	}
	if err := store.CreateOneShotJob(ctx, params); err != nil {
		t.Fatalf("MaxInt64 timestamp rejected: %v", err)
	}
	request := claimRequest(math.MaxInt64, "session", protocol.ProbeTypeTCPConnect)
	if _, err := store.ClaimJob(ctx, agentID, request, time.UnixMilli(math.MaxInt64-1), 2*time.Millisecond); err == nil {
		t.Fatal("overflowing lease timestamp accepted")
	}
	request.AgentEpoch = uint64(math.MaxInt64) + 1
	if _, err := store.ClaimJob(ctx, agentID, request, time.Unix(1000, 0), time.Second); err == nil {
		t.Fatal("epoch above MaxInt64 accepted")
	}
}

func TestAttemptMaxInt64ExhaustionIsAtomicAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempt.db")
	store, agentID, _ := testStore(t, path)
	ctx := context.Background()
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE probe_jobs SET attempt=? WHERE id='job'`, int64(math.MaxInt64-1)); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "max", protocol.ProbeTypeTCPConnect), at, time.Second)
	if err != nil || job == nil || job.Attempt != math.MaxInt64 {
		t.Fatalf("max claim=%+v err=%v", job, err)
	}
	var attemptType string
	var attempt int64
	if err := store.db.QueryRow(`SELECT typeof(attempt),attempt FROM probe_jobs WHERE id='job'`).Scan(&attemptType, &attempt); err != nil || attemptType != "integer" || attempt != math.MaxInt64 {
		t.Fatalf("attempt type=%q value=%d err=%v", attemptType, attempt, err)
	}
	claimed, err := store.ClaimJob(ctx, agentID, claimRequest(2, "exhaust", protocol.ProbeTypeTCPConnect), at.Add(time.Second), time.Second)
	if claimed != nil || !errors.Is(err, ErrAttemptExhausted) {
		t.Fatalf("exhausted claim=%+v err=%v", claimed, err)
	}
	record, err := store.GetProbeJob(ctx, "job")
	if err != nil || record.Status != JobStatusExpired || record.Attempt != math.MaxInt64 || record.LeaseToken != "" || record.LeaseUntil != 0 {
		t.Fatalf("exhausted record=%+v err=%v", record, err)
	}
	if err := store.db.QueryRow(`SELECT typeof(attempt) FROM probe_jobs WHERE id='job'`).Scan(&attemptType); err != nil || attemptType != "integer" {
		t.Fatalf("exhausted attempt type=%q err=%v", attemptType, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err = store.GetProbeJob(ctx, "job")
	if err != nil || record.Status != JobStatusExpired || record.Attempt != math.MaxInt64 {
		t.Fatalf("exhaustion after restart=%+v err=%v", record, err)
	}
	beforeRetry := record
	claimed, err = store.ClaimJob(ctx, agentID, claimRequest(3, "after-exhaustion", protocol.ProbeTypeTCPConnect), at.Add(2*time.Second), time.Second)
	if err != nil || claimed != nil {
		t.Fatalf("claim after exhaustion=%+v err=%v", claimed, err)
	}
	afterRetry, err := store.GetProbeJob(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	if afterRetry.Status != JobStatusExpired || afterRetry.Attempt != math.MaxInt64 || afterRetry.LeaseToken != "" {
		t.Fatalf("exhausted job became claimable: %+v", afterRetry)
	}
	if afterRetry.Status != beforeRetry.Status || afterRetry.Attempt != beforeRetry.Attempt ||
		afterRetry.LeaseToken != beforeRetry.LeaseToken || afterRetry.LeaseEpoch != beforeRetry.LeaseEpoch ||
		afterRetry.LeaseSessionID != beforeRetry.LeaseSessionID || afterRetry.LeasedAt != beforeRetry.LeasedAt ||
		afterRetry.LeaseUntil != beforeRetry.LeaseUntil || afterRetry.FinishedAt != beforeRetry.FinishedAt {
		t.Fatalf("claim changed exhausted job: before=%+v after=%+v", beforeRetry, afterRetry)
	}
}

func TestGetProbeResultRejectsNegativeStoredAgentEpoch(t *testing.T) {
	store, agentID, _ := testStore(t, ":memory:")
	defer store.Close()
	ctx := context.Background()
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session", protocol.ProbeTypeTCPConnect), at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, resultFor(job, true), at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE probe_results SET agent_epoch=-1 WHERE job_id='job'`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=OFF`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := store.GetProbeResult(ctx, "job")
	if !errors.Is(err, ErrCorruptProbeData) || result.Result.AgentEpoch != 0 {
		t.Fatalf("corrupt result=%+v err=%v", result, err)
	}
}

func TestSubmitResultRollsBackWhenJobUpdateFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "submit-rollback.db")
	store, agentID, _ := testStore(t, path)
	ctx := context.Background()
	at := time.Unix(1000, 0)
	if err := store.CreateOneShotJob(ctx, oneShot("job", agentID, at, protocol.ProbeTypeTCPConnect)); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimJob(ctx, agentID, claimRequest(1, "session", protocol.ProbeTypeTCPConnect), at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	beforeFailure, err := store.GetProbeJob(ctx, job.JobID)
	if err != nil || beforeFailure.Status != JobStatusLeased || beforeFailure.Attempt != job.Attempt || beforeFailure.LeaseToken != job.LeaseToken {
		t.Fatalf("job before injected failure=%+v err=%v", beforeFailure, err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_job_finish BEFORE UPDATE OF status ON probe_jobs
		WHEN NEW.id='job' AND NEW.status='finished'
		BEGIN SELECT RAISE(ABORT,'injected job update failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitJobResult(ctx, agentID, job.JobID, resultFor(job, true), at.Add(time.Second)); err == nil {
		t.Fatal("injected job update failure was ignored")
	} else if !strings.Contains(err.Error(), "injected job update failure") {
		t.Fatalf("submit failed before injected job update: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var results int
	if err := store.db.QueryRow(`SELECT count(*) FROM probe_results WHERE job_id='job'`).Scan(&results); err != nil || results != 0 {
		t.Fatalf("partial result persisted: count=%d err=%v", results, err)
	}
	record, err := store.GetProbeJob(ctx, "job")
	if err != nil || record.Status != JobStatusLeased || record.FinishedAt != 0 ||
		record.Attempt != beforeFailure.Attempt || record.LeaseToken != beforeFailure.LeaseToken {
		t.Fatalf("job partially updated: %+v err=%v", record, err)
	}
}

func TestMigratesV2ToV3WithoutChangingV01Data(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	prepareV2Database(t, path)
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	version, err := store.SchemaVersion(context.Background())
	if err != nil || version != 3 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	states, err := store.ListStates(context.Background())
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%v err=%v", states, err)
	}
	state := states[0]
	if state.AgentID != "v2-agent" || state.Epoch != 9 || state.Sequence != 7 || state.RXTotal != 10 || state.TXTotal != 20 {
		t.Fatalf("V0.1 state changed during migration: %+v", state)
	}
	var samples int
	if err := store.db.QueryRow(`SELECT samples FROM minute_metrics WHERE agent_id='v2-agent' AND bucket=60000`).Scan(&samples); err != nil || samples != 2 {
		t.Fatalf("minute metrics changed: samples=%d err=%v", samples, err)
	}
	for _, name := range []string{
		"idx_probe_schedules_due", "idx_probe_jobs_schedule_slot", "idx_probe_jobs_claim",
		"idx_probe_jobs_lease_expiry", "idx_probe_jobs_one_active_lease", "idx_probe_jobs_expiry", "idx_probe_jobs_agent_finished",
		"idx_probe_jobs_schedule_finished",
	} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Errorf("index %s count=%d err=%v", name, count, err)
		}
	}
}

func prepareV2Database(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(1,1),(2,2)`,
		`CREATE TABLE agents (id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE, revoked INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE agent_sessions (agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE, session_id TEXT NOT NULL, started_at INTEGER NOT NULL, active INTEGER NOT NULL, first_seen INTEGER NOT NULL, epoch INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(agent_id,session_id))`,
		`CREATE TABLE agent_state (agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE, session_id TEXT NOT NULL, session_started_at INTEGER NOT NULL, sequence INTEGER NOT NULL, boot_id TEXT NOT NULL, hostname TEXT NOT NULL, os TEXT NOT NULL, arch TEXT NOT NULL, uptime INTEGER NOT NULL, cpu REAL NOT NULL, load1 REAL NOT NULL, load5 REAL NOT NULL, load15 REAL NOT NULL, ram_used INTEGER NOT NULL, ram_total INTEGER NOT NULL, ram_percent REAL NOT NULL, swap_used INTEGER NOT NULL, swap_total INTEGER NOT NULL, swap_percent REAL NOT NULL, disk_used INTEGER NOT NULL, disk_total INTEGER NOT NULL, disk_percent REAL NOT NULL, raw_rx INTEGER NOT NULL, raw_tx INTEGER NOT NULL, rx_rate REAL NOT NULL, tx_rate REAL NOT NULL, rx_total INTEGER NOT NULL, tx_total INTEGER NOT NULL, collected_at INTEGER NOT NULL, last_seen INTEGER NOT NULL, epoch INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE minute_metrics (agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE, bucket INTEGER NOT NULL, samples INTEGER NOT NULL, cpu_sum REAL NOT NULL, ram_sum REAL NOT NULL, swap_sum REAL NOT NULL, disk_sum REAL NOT NULL, load1_sum REAL NOT NULL, load5_sum REAL NOT NULL, load15_sum REAL NOT NULL, rx_rate_sum REAL NOT NULL, tx_rate_sum REAL NOT NULL, rx_total INTEGER NOT NULL, tx_total INTEGER NOT NULL, PRIMARY KEY(agent_id,bucket))`,
		`INSERT INTO agents(id,name,token_hash,created_at,updated_at) VALUES('v2-agent','existing',x'01',1,1)`,
		`INSERT INTO agent_sessions(agent_id,session_id,started_at,active,first_seen,epoch) VALUES('v2-agent','session',1,1,1,9)`,
		`INSERT INTO agent_state VALUES('v2-agent','session',1,7,'boot','host','linux','amd64',100,1,1,1,1,1,2,50,0,0,0,1,2,50,100,200,1,2,10,20,1000,1000,9)`,
		`INSERT INTO minute_metrics VALUES('v2-agent',60000,2,2,2,2,2,2,2,2,2,2,10,20)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("prepare v2: %v\n%s", err, statement)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
