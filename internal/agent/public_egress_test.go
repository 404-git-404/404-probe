package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

// countingChecker stands in for the HTTPS transport to the echo service: every Check is one
// would-be request to api.ipify.org or api6.ipify.org.
type countingChecker struct {
	calls atomic.Int32
	mu    sync.Mutex
	seen  []string
	reply func(family string) protocol.EgressObservation
}

func (c *countingChecker) Check(_ context.Context, family string) protocol.EgressObservation {
	c.calls.Add(1)
	c.mu.Lock()
	c.seen = append(c.seen, family)
	c.mu.Unlock()
	if c.reply != nil {
		return c.reply(family)
	}
	return protocol.UnknownEgress(family, "network")
}

func egressTestExecutor(checker egressChecker) *ProbeExecutor {
	executor := NewProbeExecutor()
	executor.egress = &PublicEgressExecutor{checker: checker}
	return executor
}

func egressJob(now time.Time) protocol.Job {
	return protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion, JobID: "egress-job", ProbeType: protocol.ProbeTypePublicEgress,
		Config:    protocol.ProbeConfig{PublicEgress: &protocol.PublicEgressConfig{}},
		CreatedAt: now.UnixMilli(), NotBefore: now.UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli(), TimeoutMS: 20000,
		Attempt: 1, LeaseToken: "lease", LeaseExpiresAt: now.Add(time.Minute).UnixMilli(),
	}
}

// Reports no longer carry any public egress data, and an Agent that was never given a job makes no
// request to the echo service, however long it runs and whatever the Server advertises.
func TestPublicEgressIdleAgentNeverContactsTheEchoService(t *testing.T) {
	var mu sync.Mutex
	var reports []map[string]any
	claims := make(chan protocol.ClaimRequest, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/jobs/claim":
			var claim protocol.ClaimRequest
			_ = json.NewDecoder(r.Body).Decode(&claim)
			claims <- claim
			w.WriteHeader(http.StatusNoContent)
		default:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			reports = append(reports, body)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"accepted":true,"capabilities":{"public_egress_report":true,"public_egress_job":true}}`))
		}
	}))
	defer server.Close()
	checker := &countingChecker{}
	runner := newJobTestRunner(t, server.URL, time.Second, 20*time.Millisecond, egressTestExecutor(checker))
	runner.collector = reportCollectorFunc(staticReportCollector)
	var sequence uint64
	for i := 0; i < 3; i++ {
		if ok, err := runner.sendReport(context.Background(), &sequence); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runner.runJobWorker(ctx); close(done) }()
	var advertised bool
	for i := 0; i < 3; i++ {
		select {
		case claim := <-claims:
			for _, kind := range claim.SupportedProbeTypes {
				advertised = advertised || kind == protocol.ProbeTypePublicEgress
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no claim")
		}
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	waitSignal(t, done, "job worker shutdown")
	if !advertised {
		t.Fatal("the capability was not offered to a Server that advertised it")
	}
	if checker.calls.Load() != 0 {
		t.Fatalf("%d echo-service requests without a job", checker.calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, body := range reports {
		if _, ok := body["public_egress"]; ok {
			t.Fatalf("report still carries public egress data: %v", body)
		}
	}
}

func TestPublicEgressCapabilityNeedsServerAdvertisement(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		claims := make(chan protocol.ClaimRequest, 4)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var claim protocol.ClaimRequest
			_ = json.NewDecoder(r.Body).Decode(&claim)
			claims <- claim
			w.WriteHeader(http.StatusNoContent)
		}))
		runner := newJobTestRunner(t, server.URL, time.Second, time.Hour, egressTestExecutor(&countingChecker{}))
		runner.publicEgressSupported.Store(advertised)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { runner.runJobWorker(ctx); close(done) }()
		claim := <-claims
		found := false
		for _, kind := range claim.SupportedProbeTypes {
			found = found || kind == protocol.ProbeTypePublicEgress
		}
		if found != advertised {
			t.Errorf("advertised=%t claim=%v", advertised, claim.SupportedProbeTypes)
		}
		cancel()
		waitSignal(t, done, "job worker shutdown")
		server.Close()
	}
}

func TestPublicEgressJobChecksBothFamiliesAndReturnsTheResult(t *testing.T) {
	now := time.Now()
	checker := &countingChecker{reply: func(family string) protocol.EgressObservation {
		if family == "ipv4" {
			return protocol.EgressObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", Reason: "verified", CheckedAt: 5, ValidUntil: 9,
				LastSuccess: &protocol.EgressSuccess{IP: "8.8.8.8", CheckedAt: 5}}
		}
		return protocol.EgressObservation{Family: "ipv6", Status: "unavailable", Reason: "family_unavailable", CheckedAt: 5, ValidUntil: 9}
	}}
	submitted := make(chan protocol.JobResult, 1)
	var served atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/jobs/claim":
			if served.Swap(true) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeTestJSON(t, w, egressJob(now))
		case strings.HasSuffix(r.URL.Path, "/result"):
			body, _ := io.ReadAll(r.Body)
			result, err := protocol.DecodeJobResult(body, protocol.ProbeTypePublicEgress)
			if err != nil {
				t.Error(err)
			}
			submitted <- result
			_, _ = w.Write([]byte(`{"accepted":true,"duplicate":false,"job_status":"finished"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, 10*time.Millisecond, egressTestExecutor(checker))
	runner.publicEgressSupported.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runner.runJobWorker(ctx); close(done) }()
	var result protocol.JobResult
	select {
	case result = <-submitted:
	case <-time.After(3 * time.Second):
		t.Fatal("no result")
	}
	cancel()
	waitSignal(t, done, "job worker shutdown")
	if !result.Success || result.Result.PublicEgress == nil {
		t.Fatalf("result=%+v", result)
	}
	v4, v6 := result.Result.PublicEgress.IPv4, result.Result.PublicEgress.IPv6
	if v4.Status != "available" || v4.IP != "8.8.8.8" || v6.Status != "unavailable" || v6.Reason != "family_unavailable" {
		t.Fatalf("v4=%+v v6=%+v", v4, v6)
	}
	// The Agent's own clocks and lifetimes are not sent: the Server stamps them.
	if v4.CheckedAt != 0 || v4.ValidUntil != 0 || v4.LastSuccess != nil || v6.CheckedAt != 0 {
		t.Fatalf("times were sent: v4=%+v v6=%+v", v4, v6)
	}
	checker.mu.Lock()
	defer checker.mu.Unlock()
	if len(checker.seen) != 2 || checker.seen[0] != "ipv4" || checker.seen[1] != "ipv6" {
		t.Fatalf("checked %v", checker.seen)
	}
}

func TestPublicEgressTotalFailureIsAnExplicitError(t *testing.T) {
	executor := &PublicEgressExecutor{checker: &countingChecker{}}
	execution, err := executor.Execute(context.Background(), egressJob(time.Now()))
	if err != nil || execution.Success || execution.ErrorCategory != "no_public_address" {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	if err := execution.Result.Validate(protocol.ProbeTypePublicEgress); err != nil {
		t.Fatal(err)
	}
}

// A job that is anything but the exact parameterless job is refused before any connection is made.
func TestPublicEgressRefusesAnythingButTheParameterlessJob(t *testing.T) {
	now := time.Now()
	for _, config := range []string{`{"url":"https://example.com"}`, `{"host":"203.0.113.9"}`, `{"family":"ipv4"}`, `{"extra":null}`, `[]`, `"x"`, `null`} {
		wire := `{"protocol_version":1,"job_id":"egress-job","probe_type":"public_egress","config":` + config + `,"created_at":1,"not_before":1,"expires_at":2,"timeout_ms":20000,"attempt":1,"lease_token":"lease","lease_expires_at":2}`
		var job protocol.Job
		if err := json.Unmarshal([]byte(wire), &job); err == nil {
			t.Errorf("config %s was accepted: %+v", config, job)
		}
	}
	checker := &countingChecker{}
	executor := &PublicEgressExecutor{checker: checker}
	for _, job := range []protocol.Job{
		{ProbeType: protocol.ProbeTypeHTTP, Config: protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com", Method: "GET"}}},
		{ProbeType: protocol.ProbeTypePublicEgress},
		{ProbeType: protocol.ProbeTypeGoogleStatus, Config: protocol.ProbeConfig{GoogleStatus: &protocol.GoogleStatusConfig{}}},
	} {
		if _, err := executor.Execute(context.Background(), job); !errors.Is(err, ErrUnsupportedProbeType) {
			t.Errorf("job %s err=%v", job.ProbeType, err)
		}
	}
	// The same refusal through a Server response: the Agent logs it, submits nothing and checks nothing.
	var served, results atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/jobs/claim":
			if served.Add(1) == 1 {
				_, _ = io.WriteString(w, `{"protocol_version":1,"job_id":"x","probe_type":"public_egress","config":{"url":"https://api.ipify.org"},"created_at":`+jsonInt(now.UnixMilli())+`,"not_before":`+jsonInt(now.UnixMilli())+`,"expires_at":`+jsonInt(now.Add(time.Minute).UnixMilli())+`,"timeout_ms":20000,"attempt":1,"lease_token":"lease","lease_expires_at":`+jsonInt(now.Add(time.Minute).UnixMilli())+`}`)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			results.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, 10*time.Millisecond, egressTestExecutor(checker))
	runner.publicEgressSupported.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runner.runJobWorker(ctx); close(done) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	waitSignal(t, done, "job worker shutdown")
	if checker.calls.Load() != 0 || results.Load() != 0 {
		t.Fatalf("checks=%d results=%d", checker.calls.Load(), results.Load())
	}
}

func jsonInt(v int64) string { b, _ := json.Marshal(v); return string(b) }

// An old Server never advertises the job: the Agent neither offers it nor attaches telemetry.
func TestPublicEgressOldServerGetsNothing(t *testing.T) {
	var bodies []map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"accepted":true,"capabilities":{"public_egress_report":true}}`))
	}))
	defer s.Close()
	r, err := New(Config{ServerURL: s.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.collector = reportCollectorFunc(staticReportCollector)
	var seq uint64
	for i := 0; i < 3; i++ {
		if ok, err := r.sendReport(context.Background(), &seq); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	for _, body := range bodies {
		if _, ok := body["public_egress"]; ok {
			t.Fatal("telemetry attached")
		}
	}
	if r.publicEgressSupported.Load() {
		t.Fatal("capability assumed without the Server advertising it")
	}
}
