package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"404-probe/internal/protocol"
)

type f1Log struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *f1Log) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *f1Log) text() string { b.mu.Lock(); defer b.mu.Unlock(); return b.Buffer.String() }

func f1Runner(t *testing.T, token string, logs *f1Log) *Runner {
	t.Helper()
	r, err := NewWithExecutor(Config{ServerURL: "https://server.test", AgentID: "agent", Token: token, Interval: 10 * time.Second, JobInterval: 10 * time.Second, Timeout: 5 * time.Second, StatePath: filepath.Join(t.TempDir(), "epoch")}, slog.New(slog.NewTextHandler(logs, nil)), UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	r.collector = reportCollectorFunc(staticReportCollector)
	r.quality.probe = func(context.Context, protocol.QualityTarget) qualityProbeResult {
		return qualityProbeResult{outcome: "unsupported"}
	}
	return r
}

func f1Response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type f1UnreadableBody struct{}

func (f1UnreadableBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (f1UnreadableBody) Close() error             { return nil }

type f1Entry struct {
	name string
	call func(*Runner, context.Context) error
}

func f1Entries() []f1Entry {
	job := func(r *Runner) jobHTTPClient {
		return jobHTTPClient{baseURL: r.config.ServerURL, token: r.config.Token, client: r.client}
	}
	upgrade := func(r *Runner) upgradeHTTPClient {
		return upgradeHTTPClient{baseURL: r.config.ServerURL, token: r.config.Token, client: r.client}
	}
	removal := func(r *Runner) agentRemovalHTTPClient {
		return agentRemovalHTTPClient{baseURL: r.config.ServerURL, token: r.config.Token, client: r.client}
	}
	country := func(r *Runner) countryCodeLookupHTTPClient { return countryCodeLookupHTTPClient{base: job(r)} }
	return []f1Entry{
		{"report", func(r *Runner, c context.Context) error { _, e := r.post(c, protocol.Report{}); return e }},
		{"job-claim", func(r *Runner, c context.Context) error { _, e := job(r).claim(c, protocol.ClaimRequest{}); return e }},
		{"control-claim", func(r *Runner, c context.Context) error {
			_, e := job(r).claimControl(c, protocol.ControlClaimRequest{})
			return e
		}},
		{"job-result", func(r *Runner, c context.Context) error {
			now := time.Now().UnixMilli()
			_, e := job(r).submit(c, "job", protocol.JobResult{ProtocolVersion: protocol.JobProtocolVersion, LeaseToken: "lease", Attempt: 1, AgentEpoch: r.epoch, SessionID: r.sessionID, StartedAt: now, FinishedAt: now, Success: true, Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{}}})
			return e
		}},
		{"upgrade-claim", func(r *Runner, c context.Context) error { _, e := upgrade(r).claim(c); return e }},
		{"upgrade-status", func(r *Runner, c context.Context) error { return upgrade(r).update(c, "operation", "claimed", "", "") }},
		{"removal-claim", func(r *Runner, c context.Context) error { _, e := removal(r).claim(c, r.epoch, r.sessionID); return e }},
		{"removal-status", func(r *Runner, c context.Context) error {
			return removal(r).markUninstalling(c, "operation", r.epoch, r.sessionID)
		}},
		{"outbound", func(r *Runner, c context.Context) error {
			return r.postOutboundSnapshot(c, protocol.OutboundSnapshot{Available: false})
		}},
		{"security", func(r *Runner, c context.Context) error { return r.postSecurity(c, protocol.SecuritySubmission{}) }},
		{"quality-config", func(r *Runner, c context.Context) error { _, e := r.fetchQualityConfig(c); return e }},
		{"quality-upload", func(r *Runner, c context.Context) error {
			return r.qualityHTTP(c, "/api/v1/agent/network-quality", []byte(`{}`), protocol.QualityResponseLimit, &protocol.QualityBatchResponse{})
		}},
		{"country-claim", func(r *Runner, c context.Context) error {
			_, e := country(r).claim(c, protocol.AgentCountryCodeLookupClaimRequest{})
			return e
		}},
		{"country-result", func(r *Runner, c context.Context) error {
			_, e := country(r).submit(c, "operation", protocol.AgentCountryCodeLookupResultRequest{})
			return e
		}},
	}
}

func TestF1EachServerEntryCancelsAllInflightAndBlocksNewRequests(t *testing.T) {
	for _, entry := range f1Entries() {
		t.Run(entry.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := f1Runner(t, "old-token", &f1Log{})
				ctx, cancel := withAgentAuthentication(context.Background())
				defer cancel(nil)
				var calls, canceled atomic.Int32
				var invalid atomic.Bool
				r.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.Header.Get("Authorization") != "Bearer old-token" {
						t.Error("missing actual configured authentication")
					}
					calls.Add(1)
					if invalid.Load() {
						return f1Response(401, `{"error":{"code":"unauthorized","message":"DO_NOT_LOG"}}`), nil
					}
					<-req.Context().Done()
					canceled.Add(1)
					return nil, req.Context().Err()
				})}
				var workers sync.WaitGroup
				for _, other := range f1Entries() {
					workers.Add(1)
					go func(other f1Entry) { defer workers.Done(); _ = other.call(r, ctx) }(other)
				}
				synctest.Wait()
				if calls.Load() != int32(len(f1Entries())) {
					t.Fatalf("inflight=%d", calls.Load())
				}
				invalid.Store(true)
				if err := entry.call(r, ctx); !errors.Is(err, ErrAgentAuthInvalid) {
					t.Fatalf("entry error=%v", err)
				}
				workers.Wait()
				if canceled.Load() != int32(len(f1Entries())) {
					t.Fatalf("canceled inflight=%d", canceled.Load())
				}
				before := calls.Load()
				for _, other := range f1Entries() {
					if err := other.call(r, ctx); !errors.Is(err, ErrAgentAuthInvalid) {
						t.Fatalf("blocked %s error=%v", other.name, err)
					}
				}
				time.Sleep(72 * time.Hour)
				synctest.Wait()
				if calls.Load() != before || !errors.Is(context.Cause(ctx), ErrAgentAuthInvalid) {
					t.Fatal("new request or wrong shared cancellation cause")
				}
			})
		})
	}
}

func TestF1StartupInvalidAuthWaitsQuietlyAndNewRunnerRecovers(t *testing.T) {
	for _, body := range []string{`{"error":"invalid agent token DO_NOT_LOG"}`, `{"error":{"code":"unauthorized"}}`, `unrecognized`, strings.Repeat("x", 4097), "unreadable"} {
		t.Run(body[:min(25, len(body))], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := &f1Log{}
				r := f1Runner(t, "old-token", logs)
				var calls atomic.Int32
				r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					resp := f1Response(401, body)
					if body == "unreadable" {
						resp.Body = f1UnreadableBody{}
					}
					return resp, nil
				})}
				parent, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- r.Run(parent) }()
				synctest.Wait()
				before := logs.text()
				if calls.Load() != 1 || strings.Count(before, "Server authentication invalid;") != 1 || strings.Contains(before, "DO_NOT_LOG") {
					t.Fatalf("startup calls=%d logs=%s", calls.Load(), before)
				}
				select {
				case <-done:
					t.Fatal("auth-invalid returned and would restart service")
				default:
				}
				time.Sleep(7 * 24 * time.Hour)
				synctest.Wait()
				if calls.Load() != 1 || logs.text() != before {
					t.Fatal("locked Agent kept requesting or logging")
				}
				// Restart from the same existing state path with new configuration, while
				// the old Run remains locked. No reset/re-registration/hot reload.
				config := r.config
				config.Token = "new-token"
				restarted, err := NewWithExecutor(config, slog.New(slog.NewTextHandler(io.Discard, nil)), UnsupportedExecutor{})
				if err != nil {
					t.Fatal(err)
				}
				restarted.collector = reportCollectorFunc(staticReportCollector)
				restarted.quality.probe = r.quality.probe
				var fresh atomic.Int32
				restarted.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.Header.Get("Authorization") != "Bearer new-token" {
						t.Error("restart did not use new token")
					}
					fresh.Add(1)
					return f1Response(200, `{"accepted":true}`), nil
				})}
				newParent, newCancel := context.WithCancel(context.Background())
				newDone := make(chan error, 1)
				go func() { newDone <- restarted.Run(newParent) }()
				time.Sleep(21 * time.Second)
				synctest.Wait()
				if fresh.Load() < 3 || calls.Load() != 1 || restarted.epoch <= r.epoch || restarted.sessionID == r.sessionID {
					t.Fatal("new Runner/session did not recover independently")
				}
				newCancel()
				if err := <-newDone; err != nil {
					t.Fatal(err)
				}
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestF1NegotiatedControl401StopsReportAndOtherWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := &f1Log{}
		r := f1Runner(t, "token", logs)
		r.config.ClashAPIURL = "http://127.0.0.1:9090"
		r.config.OutboundInterval = time.Second
		r.outboundHeartbeatInterval = time.Second
		r.config.SecurityInterval = time.Second
		var invalid atomic.Bool
		var requests, control, report, inflight, canceled atomic.Int32
		r.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests.Add(1)
			if req.URL.Path == "/proxies" {
				return f1Response(200, `{"proxies":{"proxy":{"type":"Selector","now":"a","all":["a","b"]}}}`), nil
			}
			if req.URL.Path == "/api/v1/agent/control/claim" {
				control.Add(1)
				if invalid.Load() {
					time.Sleep(time.Second)
					return f1Response(401, `{"error":"invalid agent token"}`), nil
				}
				time.Sleep(time.Second)
				return f1Response(204, ""), nil
			}
			if invalid.Load() {
				inflight.Add(1)
				<-req.Context().Done()
				canceled.Add(1)
				return nil, req.Context().Err()
			}
			if req.URL.Path == "/api/v1/report" {
				report.Add(1)
				return f1Response(200, `{"accepted":true,"capabilities":{"interactive_control":true,"security":true,"management_capabilities_report":true,"network_quality":true}}`), nil
			}
			if req.URL.Path == "/api/v1/agent/country-code-lookups/claim" {
				return f1Response(204, ""), nil
			}
			if req.URL.Path == "/api/v1/agent/network-quality/config" {
				b, _ := json.Marshal(protocol.QualityConfig{Version: 1, Revision: "1", Supported: true, IntervalMS: 30000, JitterPercent: 20, TimeoutMS: 5000, Targets: []protocol.QualityTarget{}})
				return f1Response(200, string(b)), nil
			}
			return f1Response(200, `{"accepted":true}`), nil
		})}
		parent, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Run(parent) }()
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if report.Load() < 2 || control.Load() < 1 || !r.interactiveControlSupported.Load() || !r.clashControlReady.Load() {
			t.Fatal("real report/control negotiation was not active")
		}
		invalid.Store(true)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if inflight.Load() == 0 || canceled.Load() != inflight.Load() {
			t.Fatalf("other requests not canceled: %d/%d", canceled.Load(), inflight.Load())
		}
		beforeRequests, beforeLog := requests.Load(), logs.text()
		if strings.Count(beforeLog, "Server authentication invalid;") != 1 {
			t.Fatalf("auth log=%s", beforeLog)
		}
		time.Sleep(72 * time.Hour)
		synctest.Wait()
		if requests.Load() != beforeRequests || logs.text() != beforeLog {
			t.Fatal("negotiated workers kept requesting/logging after invalid auth")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestF1OtherErrorsAndLocal401DoNotLockServerAuthentication(t *testing.T) {
	for _, status := range []int{403, 423, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r := f1Runner(t, "token", &f1Log{})
			ctx, cancel := withAgentAuthentication(context.Background())
			defer cancel(nil)
			r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return f1Response(status, `{"error":{"code":"agent_disabled"}}`), nil
			})}
			_, _ = r.post(ctx, protocol.Report{})
			if ctx.Err() != nil {
				t.Fatal("non-401 invalidated credentials")
			}
		})
	}
	for _, fault := range []error{context.DeadlineExceeded, errors.New("network unavailable")} {
		r := f1Runner(t, "token", &f1Log{})
		ctx, cancel := withAgentAuthentication(context.Background())
		r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, fault })}
		_, _ = r.post(ctx, protocol.Report{})
		if ctx.Err() != nil {
			t.Fatal("transport fault invalidated credentials")
		}
		cancel(nil)
	}
	r := f1Runner(t, "token", &f1Log{})
	ctx, cancel := withAgentAuthentication(context.Background())
	defer cancel(nil)
	r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return f1Response(401, `{"error":{"code":"agent_revoked"}}`), nil
	})}
	_, _ = (clashClient{endpoint: "http://127.0.0.1:9090", client: r.client}).discover(ctx)
	execution, err := (&HTTPExecutor{client: r.client}).Execute(ctx, protocol.Job{ProbeType: protocol.ProbeTypeHTTP, Config: protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://target.test/", Method: "GET"}}})
	if err != nil || execution.Result.HTTP == nil || execution.Result.HTTP.StatusCode != 401 || ctx.Err() != nil {
		t.Fatal("local/target401 contaminated Server state")
	}
}

func TestF1InvalidAuthCancelsRunningJobAndQualityProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := &f1Log{}
		r := f1Runner(t, "token", logs)
		var jobStarted, jobCanceled, probeStarted, probeCanceled, calls atomic.Int32
		r.executor = fakeExecutor{capabilities: []protocol.ProbeType{protocol.ProbeTypeTCPConnect}, execute: func(ctx context.Context, _ protocol.Job) (Execution, error) {
			jobStarted.Add(1)
			<-ctx.Done()
			jobCanceled.Add(1)
			return Execution{}, ctx.Err()
		}}
		cfg := protocol.QualityConfig{Version: 1, Revision: "1", Supported: true, Enabled: true, IntervalMS: 30000, JitterPercent: 20, TimeoutMS: 5000,
			Targets: []protocol.QualityTarget{{ID: strings.Repeat("a", 32), Slot: "telecom", Family: "ipv4", SlotRevision: "1", Source: "manual", Protocol: "tcp", Host: "1.1.1.1", Port: 443, Status: "active"}}}
		r.quality.random = func() float64 { return 0 }
		r.quality.allowed = true
		if !r.quality.install(cfg) {
			t.Fatal("invalid active quality fixture")
		}
		r.quality.probe = func(ctx context.Context, _ protocol.QualityTarget) qualityProbeResult {
			probeStarted.Add(1)
			<-ctx.Done()
			probeCanceled.Add(1)
			return qualityProbeResult{outcome: "canceled"}
		}
		var invalid atomic.Bool
		firstReportCtx := make(chan context.Context, 1)
		r.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			if invalid.Load() {
				return f1Response(401, `{"error":{"code":"unauthorized"}}`), nil
			}
			if req.URL.Path == "/api/v1/report" {
				select {
				case firstReportCtx <- req.Context():
				default:
				}
				b, _ := json.Marshal(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &cfg})
				return f1Response(200, string(b)), nil
			}
			if req.URL.Path == "/api/v1/agent/jobs/claim" {
				now := time.Now().UnixMilli()
				job := protocol.Job{ProtocolVersion: protocol.JobProtocolVersion, JobID: "active-job", ProbeType: protocol.ProbeTypeTCPConnect, Config: protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "1.1.1.1", Port: 443}}, CreatedAt: now, NotBefore: now, ExpiresAt: now + 60000, TimeoutMS: 30000, Attempt: 1, LeaseToken: "lease", LeaseExpiresAt: now + 60000}
				if err := job.Validate(); err != nil {
					t.Error(err)
				}
				b, err := json.Marshal(job)
				if err != nil {
					t.Error(err)
				}
				return f1Response(200, string(b)), nil
			}
			return f1Response(200, `{"accepted":true,"results":[]}`), nil
		})}
		parent, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Run(parent) }()
		time.Sleep(25 * time.Second)
		synctest.Wait()
		if jobStarted.Load() != 1 || jobCanceled.Load() != 0 || probeStarted.Load() != 1 || probeCanceled.Load() != 0 {
			t.Fatalf("active job/probe fixture: %d/%d %d/%d", jobStarted.Load(), jobCanceled.Load(), probeStarted.Load(), probeCanceled.Load())
		}
		workerCtx := <-firstReportCtx
		invalid.Store(true)
		if _, err := r.post(workerCtx, protocol.Report{}); !errors.Is(err, ErrAgentAuthInvalid) {
			t.Fatalf("post=%v", err)
		}
		synctest.Wait()
		if jobCanceled.Load() != 1 || probeCanceled.Load() != 1 {
			t.Fatal("running job/quality sample did not honor shared cancellation")
		}
		before, beforeLogs := calls.Load(), logs.text()
		time.Sleep(72 * time.Hour)
		synctest.Wait()
		if calls.Load() != before || jobStarted.Load() != 1 || probeStarted.Load() != 1 || logs.text() != beforeLogs || strings.Count(beforeLogs, "Server authentication invalid;") != 1 {
			t.Fatal("work or logs restarted after invalid auth")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestF1SuccessBodiesAreNotSubjectToAuthenticationReadBudget(t *testing.T) {
	ctx, cancel := withAgentAuthentication(context.Background())
	defer cancel(nil)
	r := f1Runner(t, "token", &f1Log{})
	// The job endpoint permits up to64KiB. Legal leading JSON whitespace
	// exceeds the401 inspection budget without changing the job schema.
	now := time.Now().UnixMilli()
	job := protocol.Job{ProtocolVersion: protocol.JobProtocolVersion, JobID: "large-success", ProbeType: protocol.ProbeTypeTCPConnect, Config: protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: "1.1.1.1", Port: 443}}, CreatedAt: now, NotBefore: now, ExpiresAt: now + 60000, TimeoutMS: 30000, Attempt: 1, LeaseToken: "lease", LeaseExpiresAt: now + 60000}
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat(" ", 50000) + string(encoded)
	r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return f1Response(200, body), nil })}
	client := jobHTTPClient{baseURL: r.config.ServerURL, token: r.config.Token, client: r.client}
	claimed, err := client.claim(ctx, protocol.ClaimRequest{})
	if err != nil || claimed == nil || claimed.JobID != job.JobID || ctx.Err() != nil {
		t.Fatalf("success budget changed: %v", err)
	}
}

type f1TrackedBody struct {
	io.ReadCloser
	closed *atomic.Int32
}

func (b f1TrackedBody) Close() error { b.closed.Add(1); return b.ReadCloser.Close() }

func TestF1Concurrent401ClosesEveryBodyAndKeepsOneCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := f1Runner(t, "token", &f1Log{})
		ctx, cancel := withAgentAuthentication(context.Background())
		defer cancel(nil)
		var entered, closed atomic.Int32
		release := make(chan struct{})
		r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			n := entered.Add(1)
			<-release
			resp := f1Response(401, `{"error":{"code":"unauthorized"}}`)
			if n%3 == 0 {
				resp.Body = f1UnreadableBody{}
			} else if n%3 == 1 {
				resp.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 4097)))
			}
			resp.Body = f1TrackedBody{resp.Body, &closed}
			return resp, nil
		})}
		var workers sync.WaitGroup
		for _, entry := range f1Entries() {
			workers.Add(1)
			go func(entry f1Entry) {
				defer workers.Done()
				if err := entry.call(r, ctx); !errors.Is(err, ErrAgentAuthInvalid) {
					t.Errorf("%s: %v", entry.name, err)
				}
			}(entry)
		}
		synctest.Wait()
		if entered.Load() != 14 {
			t.Fatalf("concurrent actual entries=%d", entered.Load())
		}
		close(release)
		workers.Wait()
		if closed.Load() != 14 || !errors.Is(context.Cause(ctx), ErrAgentAuthInvalid) {
			t.Fatalf("closed=%d cause=%v", closed.Load(), context.Cause(ctx))
		}
	})
}

func TestF1DisabledThenInvalidAuthDoesNotResumeWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := &f1Log{}
		r := f1Runner(t, "token", logs)
		r.config.DisabledInterval = time.Second
		var calls atomic.Int32
		r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return f1Response(423, `{"error":{"code":"agent_disabled"}}`), nil
			}
			return f1Response(401, `{"error":"invalid agent token"}`), nil
		})}
		parent, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Run(parent) }()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		before := logs.text()
		if calls.Load() != 2 || strings.Count(before, "Server authentication invalid;") != 1 {
			t.Fatalf("calls=%d logs=%s", calls.Load(), before)
		}
		time.Sleep(72 * time.Hour)
		synctest.Wait()
		if calls.Load() != 2 || logs.text() != before {
			t.Fatal("disabled wait resumed after invalid auth")
		}
		select {
		case <-done:
			t.Fatal("invalid auth exited before parent cancellation")
		default:
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestF1CanceledChild401DoesNotInvalidateRun(t *testing.T) {
	for _, cancelDuring := range []string{"before-request", "during-transport", "during-body"} {
		t.Run(cancelDuring, func(t *testing.T) {
			r := f1Runner(t, "token", &f1Log{})
			run, cancelRun := withAgentAuthentication(context.Background())
			defer cancelRun(nil)
			child, cancelChild := context.WithCancel(run)
			defer cancelChild()
			var calls, closed atomic.Int32
			r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				resp := f1Response(401, `{"error":{"code":"unauthorized"}}`)
				if cancelDuring == "during-transport" {
					cancelChild()
				}
				if cancelDuring == "during-body" {
					resp.Body = &f1CancelBody{Reader: strings.NewReader(`{"error":"invalid token"}`), cancel: cancelChild}
				}
				resp.Body = f1TrackedBody{resp.Body, &closed}
				return resp, nil
			})}
			if cancelDuring == "before-request" {
				cancelChild()
			}
			_, err := r.post(child, protocol.Report{})
			wantCalls := int32(1)
			if cancelDuring == "before-request" {
				wantCalls = 0
			}
			if !errors.Is(err, context.Canceled) || run.Err() != nil || calls.Load() != wantCalls || closed.Load() != wantCalls {
				t.Fatalf("err=%v run=%v calls=%d closed=%d", err, context.Cause(run), calls.Load(), closed.Load())
			}
			// A later live request must still trigger the ordinary401 protection.
			if _, err := r.post(run, protocol.Report{}); !errors.Is(err, ErrAgentAuthInvalid) {
				t.Fatalf("live401 protection weakened: %v", err)
			}
		})
	}
}

type f1CancelBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (b *f1CancelBody) Read(p []byte) (int, error) { b.cancel(); return b.Reader.Read(p) }
func (*f1CancelBody) Close() error                 { return nil }

func TestF1SharedAuthenticationCauseWinsOverCanceledChild(t *testing.T) {
	r := f1Runner(t, "token", &f1Log{})
	run, cancelRun := withAgentAuthentication(context.Background())
	defer cancelRun(nil)
	child, cancelChild := context.WithCancel(run)
	defer cancelChild()
	var closed atomic.Int32
	r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		cancelChild()
		cancelRun(ErrAgentRevoked)
		resp := f1Response(401, `{"error":"invalid token"}`)
		resp.Body = f1TrackedBody{resp.Body, &closed}
		return resp, nil
	})}
	if _, err := r.post(child, protocol.Report{}); !errors.Is(err, ErrAgentRevoked) || closed.Load() != 1 || !errors.Is(context.Cause(run), ErrAgentRevoked) {
		t.Fatalf("shared cause/body: %v closed=%d", err, closed.Load())
	}
}
