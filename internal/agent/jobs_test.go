package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

type fakeExecutor struct {
	capabilities []protocol.ProbeType
	execute      func(context.Context, protocol.Job) (Execution, error)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (f fakeExecutor) SupportedProbeTypes() []protocol.ProbeType {
	return f.capabilities
}

func (f fakeExecutor) Execute(ctx context.Context, job protocol.Job) (Execution, error) {
	return f.execute(ctx, job)
}

func TestJobWorkerNoJobWaitsForNextRound(t *testing.T) {
	var claims atomic.Int32
	secondClaim := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/jobs/claim" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if claims.Add(1) == 2 {
			close(secondClaim)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	runner := newJobTestRunner(t, server.URL, 100*time.Millisecond, 10*time.Millisecond, fakeExecutor{
		capabilities: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
		execute: func(context.Context, protocol.Job) (Execution, error) {
			t.Fatal("executor called for an empty claim")
			return Execution{}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runJobWorker(ctx)
		close(done)
	}()
	waitSignal(t, secondClaim, "second claim")
	cancel()
	waitSignal(t, done, "worker shutdown")
}

func TestJobWorkerExecutesAndSubmitsResult(t *testing.T) {
	job := validWorkerJob()
	resultReceived := make(chan protocol.JobResult, 1)
	var claimCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/jobs/claim":
			if claimCount.Add(1) == 1 {
				writeTestJSON(t, w, job)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/jobs/job-1/result":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			result, err := protocol.DecodeJobResult(body, job.ProbeType)
			if err != nil {
				t.Errorf("decode submitted result: %v", err)
				return
			}
			resultReceived <- result
			writeTestJSON(t, w, map[string]any{"accepted": true, "duplicate": true, "job_status": "finished"})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var executions atomic.Int32
	runner := newJobTestRunner(t, server.URL, 100*time.Millisecond, time.Second, fakeExecutor{
		capabilities: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
		execute: func(_ context.Context, got protocol.Job) (Execution, error) {
			executions.Add(1)
			if got.JobID != job.JobID {
				t.Errorf("job ID = %q", got.JobID)
			}
			return Execution{Success: true, Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{ConnectMS: 2.5}}}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runJobWorker(ctx)
		close(done)
	}()
	var result protocol.JobResult
	select {
	case result = <-resultReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("result was not submitted")
	}
	cancel()
	waitSignal(t, done, "worker shutdown")
	if executions.Load() != 1 {
		t.Fatalf("executions = %d", executions.Load())
	}
	if result.LeaseToken != job.LeaseToken || result.Attempt != job.Attempt || result.AgentEpoch != runner.epoch || result.SessionID != runner.sessionID {
		t.Fatalf("result fencing metadata = %+v", result)
	}
}

func TestJobWorkerCancellationStopsHTTPRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	runner := newJobTestRunner(t, "http://example.test", 5*time.Second, time.Second, successfulFakeExecutor())
	runner.client = &http.Client{Timeout: runner.config.Timeout, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-request.Context().Done()
		close(requestCanceled)
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runJobWorker(ctx)
		close(done)
	}()
	waitSignal(t, requestStarted, "HTTP request start")
	cancel()
	waitSignal(t, requestCanceled, "HTTP request cancellation")
	waitSignal(t, done, "worker shutdown")
}

func TestJobWorkerHTTPTimeoutContinuesNextRound(t *testing.T) {
	var claims atomic.Int32
	secondClaim := make(chan struct{})
	runner := newJobTestRunner(t, "http://example.test", 20*time.Millisecond, 5*time.Millisecond, successfulFakeExecutor())
	runner.client = &http.Client{Timeout: runner.config.Timeout, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		claim := claims.Add(1)
		if claim == 1 {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		if claim == 2 {
			close(secondClaim)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runJobWorker(ctx)
		close(done)
	}()
	waitSignal(t, secondClaim, "claim after HTTP timeout")
	cancel()
	waitSignal(t, done, "worker shutdown")
}

func TestJobWorkerLeaseLostContinues(t *testing.T) {
	job := validWorkerJob()
	var resultCount atomic.Int32
	twoResults := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/jobs/claim" {
			writeTestJSON(t, w, job)
			return
		}
		if r.URL.Path == "/api/v1/agent/jobs/job-1/result" {
			if resultCount.Add(1) == 2 {
				close(twoResults)
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"lease_lost","message":"lost"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	runner := newJobTestRunner(t, server.URL, 100*time.Millisecond, 5*time.Millisecond, successfulFakeExecutor())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runJobWorker(ctx)
		close(done)
	}()
	waitSignal(t, twoResults, "second result after lease_lost")
	cancel()
	waitSignal(t, done, "worker shutdown")
}

func TestUnsupportedExecutorReturnsStableFailure(t *testing.T) {
	job := validWorkerJob()
	executor := UnsupportedExecutor{}
	if capabilities := executor.SupportedProbeTypes(); len(capabilities) != 0 {
		t.Fatalf("unsupported executor capabilities = %v", capabilities)
	}
	if _, err := executor.Execute(context.Background(), job); !errors.Is(err, ErrUnsupportedProbeType) {
		t.Fatalf("error = %v", err)
	}

	runner := &Runner{epoch: 1, sessionID: "session", executor: executor}
	result := runner.executeJob(context.Background(), job)
	if result.Success || result.ErrorCategory != "unsupported_probe_type" {
		t.Fatalf("unsupported result = %+v", result)
	}
	if err := result.Validate(job.ProbeType); err != nil {
		t.Fatalf("unsupported result is not protocol-valid: %v", err)
	}
}

func TestRunnerExecutesVerifiedSelectorSwitchAndPublishesSnapshot(t *testing.T) {
	current := "hk"
	puts, snapshots := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			_, _ = io.WriteString(w, `{"proxies":{"proxy":{"type":"Selector","name":"proxy","now":"`+current+`","all":["hk","jp"]}}}`)
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/proxy":
			puts++
			current = "jp"
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/outbounds":
			snapshots++
			writeTestJSON(t, w, map[string]any{"accepted": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, time.Second, UnsupportedExecutor{})
	runner.config.ClashAPIURL = server.URL
	runner.client = server.Client()
	job := protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion, JobID: "switch-job", ProbeType: protocol.ProbeTypeSelectorSwitch,
		Config:    protocol.ProbeConfig{SelectorSwitch: &protocol.SelectorSwitchConfig{Selector: "proxy", Choice: "jp"}},
		CreatedAt: 1000, NotBefore: 1000, ExpiresAt: 10000, TimeoutMS: 1000, Attempt: 1,
		LeaseToken: "lease", LeaseExpiresAt: 9000,
	}
	result := runner.executeJob(context.Background(), job)
	if !result.Success || result.Result.SelectorSwitch == nil || result.Result.SelectorSwitch.Current != "jp" || !result.Result.SelectorSwitch.Changed || puts != 1 || snapshots != 1 {
		t.Fatalf("result=%+v puts=%d snapshots=%d", result, puts, snapshots)
	}
}

func TestRunnerSelectorSwitchReportsStaleChoiceWithoutMutation(t *testing.T) {
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		_, _ = io.WriteString(w, `{"proxies":{"proxy":{"type":"Selector","name":"proxy","now":"hk","all":["hk"]}}}`)
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, time.Second, UnsupportedExecutor{})
	runner.config.ClashAPIURL = server.URL
	runner.client = server.Client()
	job := protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion, JobID: "switch-job", ProbeType: protocol.ProbeTypeSelectorSwitch,
		Config:    protocol.ProbeConfig{SelectorSwitch: &protocol.SelectorSwitchConfig{Selector: "proxy", Choice: "jp"}},
		CreatedAt: 1000, NotBefore: 1000, ExpiresAt: 10000, TimeoutMS: 1000, Attempt: 1,
		LeaseToken: "lease", LeaseExpiresAt: 9000,
	}
	result := runner.executeJob(context.Background(), job)
	if result.Success || result.ErrorCategory != "choice_not_found" || puts != 0 {
		t.Fatalf("result=%+v puts=%d", result, puts)
	}
}

func TestRunnerSelectorSwitchSuccessSurvivesSnapshotPublicationRace(t *testing.T) {
	current := "hk"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			_, _ = io.WriteString(w, `{"proxies":{"proxy":{"type":"Selector","name":"proxy","now":"`+current+`","all":["hk","jp"]}}}`)
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/proxy":
			current = "jp"
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/outbounds":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, time.Second, UnsupportedExecutor{})
	runner.config.ClashAPIURL = server.URL
	runner.client = server.Client()
	job := protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion, JobID: "switch-race", ProbeType: protocol.ProbeTypeSelectorSwitch,
		Config:    protocol.ProbeConfig{SelectorSwitch: &protocol.SelectorSwitchConfig{Selector: "proxy", Choice: "jp"}},
		CreatedAt: 1000, NotBefore: 1000, ExpiresAt: 10000, TimeoutMS: 1000, Attempt: 1,
		LeaseToken: "lease", LeaseExpiresAt: 9000,
	}
	result := runner.executeJob(context.Background(), job)
	if !result.Success || result.Result.SelectorSwitch == nil || result.Result.SelectorSwitch.Current != "jp" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRunnerSerializesSameSelectorLeaseAcrossClaimLanes(t *testing.T) {
	var currentMu sync.Mutex
	current := "hk"
	puts, submits := 0, 0
	firstRead := make(chan struct{})
	releaseFirstRead := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		currentMu.Lock()
		defer currentMu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			if current == "hk" {
				blocked := false
				select {
				case <-firstRead:
				default:
					close(firstRead)
					blocked = true
				}
				if blocked {
					<-releaseFirstRead
				}
			}
			_, _ = io.WriteString(w, `{"proxies":{"proxy":{"type":"Selector","name":"proxy","now":"`+current+`","all":["hk","jp"]}}}`)
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/proxy":
			puts++
			current = "jp"
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/outbounds":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/result"):
			submits++
			_, _ = io.WriteString(w, `{"accepted":true,"duplicate":false,"job_status":"finished"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, time.Second, UnsupportedExecutor{})
	runner.config.ClashAPIURL = server.URL
	runner.client = server.Client()
	job := validWorkerJob()
	job.JobID = "same-lease"
	job.ProbeType = protocol.ProbeTypeSelectorSwitch
	job.Config = protocol.ProbeConfig{SelectorSwitch: &protocol.SelectorSwitchConfig{Selector: "proxy", Choice: "jp"}}
	client := jobHTTPClient{baseURL: server.URL, token: "token", client: server.Client()}
	done := make(chan struct{}, 2)
	go func() { runner.runSelectorJob(context.Background(), client, job); done <- struct{}{} }()
	<-firstRead
	go func() { runner.runSelectorJob(context.Background(), client, job); done <- struct{}{} }()
	close(releaseFirstRead)
	<-done
	<-done
	if puts != 1 || submits != 1 || !runner.selectorJobSubmitted || !runner.selectorJobResultReady ||
		runner.selectorJobResult.Result.SelectorSwitch == nil || !runner.selectorJobResult.Result.SelectorSwitch.Changed {
		t.Fatalf("puts=%d submits=%d result=%+v submitted=%t", puts, submits, runner.selectorJobResult, runner.selectorJobSubmitted)
	}
}

func TestSelectorReplayRetriesCachedResultWithoutReexecution(t *testing.T) {
	current := "hk"
	discovers, puts, submits := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			discovers++
			_, _ = io.WriteString(w, `{"proxies":{"proxy":{"type":"Selector","name":"proxy","now":"`+current+`","all":["hk","jp"]}}}`)
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/proxy":
			puts++
			current = "jp"
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/outbounds":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/result"):
			submits++
			if submits == 1 {
				http.Error(w, "temporary", http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, `{"accepted":true,"duplicate":false,"job_status":"finished"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, time.Second, UnsupportedExecutor{})
	runner.config.ClashAPIURL = server.URL
	runner.client = server.Client()
	job := validWorkerJob()
	job.JobID = "retry-same-lease"
	job.ProbeType = protocol.ProbeTypeSelectorSwitch
	job.Config = protocol.ProbeConfig{SelectorSwitch: &protocol.SelectorSwitchConfig{Selector: "proxy", Choice: "jp"}}
	client := jobHTTPClient{baseURL: server.URL, token: "token", client: server.Client()}
	runner.runSelectorJob(context.Background(), client, job)
	if runner.selectorJobSubmitted {
		t.Fatal("failed result upload marked submitted")
	}
	runner.runSelectorJob(context.Background(), client, job)
	if !runner.selectorJobSubmitted || discovers != 3 || puts != 1 || submits != 2 {
		t.Fatalf("submitted=%t discovers=%d puts=%d submits=%d", runner.selectorJobSubmitted, discovers, puts, submits)
	}
}

func TestConfiguredClashDiscoveryAdvertisesSelectorSwitchCapability(t *testing.T) {
	claimReceived := make(chan protocol.ClaimRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var claim protocol.ClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&claim); err != nil {
			t.Error(err)
		}
		claimReceived <- claim
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, time.Hour, UnsupportedExecutor{})
	runner.config.ClashAPIURL = server.URL
	runner.clashControlReady.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runner.runJobWorker(ctx); close(done) }()
	claim := <-claimReceived
	cancel()
	waitSignal(t, done, "selector worker shutdown")
	if len(claim.SupportedProbeTypes) != 1 || claim.SupportedProbeTypes[0] != protocol.ProbeTypeSelectorSwitch {
		t.Fatalf("capabilities=%v", claim.SupportedProbeTypes)
	}
}

func TestGoogleStatusClaimRequiresCurrentNegotiation(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			claims := make(chan protocol.ClaimRequest, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var claim protocol.ClaimRequest
				if err := json.NewDecoder(r.Body).Decode(&claim); err != nil {
					t.Error(err)
				}
				claims <- claim
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			runner := newJobTestRunner(t, server.URL, time.Second, time.Hour, NewProbeExecutor())
			runner.googleStatusSupported.Store(supported)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { runner.runJobWorker(ctx); close(done) }()
			select {
			case claim := <-claims:
				found := false
				for _, kind := range claim.SupportedProbeTypes {
					found = found || kind == protocol.ProbeTypeGoogleStatus
				}
				if found != supported {
					t.Errorf("capabilities=%v negotiated=%t", claim.SupportedProbeTypes, supported)
				}
			case <-time.After(2 * time.Second):
				t.Error("no claim")
			}
			cancel()
			waitSignal(t, done, "google worker shutdown")
		})
	}
}

func TestInteractiveControlWorkerReconnectsAfterServerFailure(t *testing.T) {
	var claims atomic.Int32
	current := "hk"
	resultReceived := make(chan protocol.JobResult, 1)
	now := time.Now()
	job := protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion, JobID: "interactive-reconnect", ProbeType: protocol.ProbeTypeSelectorSwitch,
		Config:    protocol.ProbeConfig{SelectorSwitch: &protocol.SelectorSwitchConfig{Selector: "proxy", Choice: "jp"}},
		CreatedAt: now.UnixMilli(), NotBefore: now.UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli(), TimeoutMS: 1000,
		Attempt: 1, LeaseToken: "lease", LeaseExpiresAt: now.Add(time.Minute).UnixMilli(),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/control/claim":
			if claims.Add(1) == 1 {
				http.Error(w, "restarting", http.StatusServiceUnavailable)
				return
			}
			writeTestJSON(t, w, job)
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			_, _ = io.WriteString(w, `{"proxies":{"proxy":{"type":"Selector","name":"proxy","now":"`+current+`","all":["hk","jp"]}}}`)
		case r.Method == http.MethodPut && r.URL.Path == "/proxies/proxy":
			current = "jp"
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/outbounds":
			writeTestJSON(t, w, map[string]any{"accepted": true})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/agent/jobs/"):
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			result, err := protocol.DecodeJobResult(raw, protocol.ProbeTypeSelectorSwitch)
			if err != nil {
				t.Error(err)
			}
			writeTestJSON(t, w, map[string]any{"accepted": true, "duplicate": false, "job_status": "finished"})
			resultReceived <- result
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, time.Second, UnsupportedExecutor{})
	runner.config.ClashAPIURL = server.URL
	runner.client = server.Client()
	runner.clashControlReady.Store(true)
	runner.interactiveControlSupported.Store(true)
	runner.interactiveControlOnce.Do(func() { close(runner.interactiveControlReady) })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { runner.runControlWorker(ctx); close(done) }()
	select {
	case result := <-resultReceived:
		if !result.Success || result.Result.SelectorSwitch == nil || result.Result.SelectorSwitch.Current != "jp" || claims.Load() < 2 {
			t.Fatalf("result=%+v claims=%d", result, claims.Load())
		}
		cancel()
	case <-ctx.Done():
		t.Fatal("control worker did not reconnect")
	}
	<-done
}

func newJobTestRunner(t *testing.T, serverURL string, timeout, interval time.Duration, executor Executor) *Runner {
	t.Helper()
	runner, err := NewWithExecutor(Config{
		ServerURL:         serverURL,
		AgentID:           "agent",
		Token:             "token",
		Interval:          time.Hour,
		JobInterval:       interval,
		Timeout:           timeout,
		AllowInsecureHTTP: true,
		StatePath:         filepath.Join(t.TempDir(), "epoch"),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), executor)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func successfulFakeExecutor() Executor {
	return fakeExecutor{
		capabilities: []protocol.ProbeType{protocol.ProbeTypeTCPConnect},
		execute: func(context.Context, protocol.Job) (Execution, error) {
			return Execution{Success: true, Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{ConnectMS: 1}}}, nil
		},
	}
}

func validWorkerJob() protocol.Job {
	now := time.Now().UnixMilli()
	return protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion,
		JobID:           "job-1",
		ProbeType:       protocol.ProbeTypeTCPConnect,
		Config:          protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "example.com", Port: 443}},
		CreatedAt:       now,
		NotBefore:       now,
		ExpiresAt:       now + 60_000,
		TimeoutMS:       protocol.MinProbeTimeoutMS,
		Attempt:         1,
		LeaseToken:      "lease-token",
		LeaseExpiresAt:  now + 30_000,
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
