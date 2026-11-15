package agent

import (
	"bytes"
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

// countingChecker stands in for a family check against the fixed echo sites.
type countingChecker struct {
	calls       atomic.Int32
	mu          sync.Mutex
	seen        []string
	reply       func(family string) protocol.EgressObservation
	withContext func(context.Context, string) protocol.EgressObservation
}

func (c *countingChecker) Check(ctx context.Context, family string) protocol.EgressObservation {
	c.calls.Add(1)
	c.mu.Lock()
	c.seen = append(c.seen, family)
	c.mu.Unlock()
	if c.withContext != nil {
		return c.withContext(ctx, family)
	}
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
	if len(checker.seen) != 2 || checker.seen[0] == checker.seen[1] {
		t.Fatalf("checked %v", checker.seen)
	}
}

func TestPublicEgressSitesUseLatestServerCapability(t *testing.T) {
	type oldObservation struct {
		Family      string                  `json:"family"`
		Status      string                  `json:"status"`
		IP          string                  `json:"ip,omitempty"`
		CheckedAt   int64                   `json:"checked_at"`
		ValidUntil  int64                   `json:"valid_until"`
		Reason      string                  `json:"reason"`
		LastSuccess *protocol.EgressSuccess `json:"last_success,omitempty"`
	}
	type oldResult struct {
		IPv4 oldObservation `json:"ipv4"`
		IPv6 oldObservation `json:"ipv6"`
	}
	oldPayload, err := json.Marshal(oldResult{
		IPv4: oldObservation{Family: "ipv4", Status: "available", IP: "8.8.8.8", Reason: "verified"},
		IPv6: oldObservation{Family: "ipv6", Status: "unknown", Reason: "dns"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, supported := range []bool{false, true} {
		var advertised atomic.Bool
		advertised.Store(!supported)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{
				PublicEgressJob: true, PublicEgressSites: advertised.Load(),
			}})
		}))
		checkStarted, release := make(chan struct{}), make(chan struct{})
		checker := &countingChecker{reply: func(family string) protocol.EgressObservation {
			if family == "ipv4" {
				close(checkStarted)
				<-release
				return protocol.EgressObservation{Family: family, Status: "available", IP: "8.8.8.8", Reason: "verified",
					Site: protocol.EgressSiteIPify, Tried: 1, CheckedAt: 123, ValidUntil: 456}
			}
			o := protocol.UnknownEgress(family, "dns")
			o.Tried = 3
			return o
		}}
		runner := newJobTestRunner(t, server.URL, time.Second, time.Hour, egressTestExecutor(checker))
		runner.collector = reportCollectorFunc(staticReportCollector)
		var sequence uint64
		if ok, err := runner.sendReport(context.Background(), &sequence); !ok || err != nil {
			server.Close()
			t.Fatal(ok, err)
		}
		result := make(chan protocol.JobResult, 1)
		go func() { result <- runner.executeJob(context.Background(), egressJob(time.Now())) }()
		waitSignal(t, checkStarted, "in-flight public egress check")
		advertised.Store(supported)
		ok, err := runner.sendReport(context.Background(), &sequence)
		close(release)
		server.Close()
		if !ok || err != nil {
			t.Fatal(ok, err)
		}
		wire, err := json.Marshal(<-result)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(wire, &envelope); err != nil {
			t.Fatal(err)
		}
		var old oldResult
		strict := json.NewDecoder(bytes.NewReader(envelope.Result))
		strict.DisallowUnknownFields()
		oldErr := strict.Decode(&old)
		decoded, err := protocol.DecodeJobResult(wire, protocol.ProbeTypePublicEgress)
		if err != nil {
			t.Fatal(err)
		}
		v4, v6 := decoded.Result.PublicEgress.IPv4, decoded.Result.PublicEgress.IPv6
		if supported {
			if oldErr == nil || v4.Site != protocol.EgressSiteIPify || v4.Tried != 1 || v6.Tried != 3 {
				t.Fatalf("modern payload %s, legacy error %v", envelope.Result, oldErr)
			}
		} else if oldErr != nil || !bytes.Equal(envelope.Result, oldPayload) {
			t.Fatalf("legacy payload %s, want %s: %v", envelope.Result, oldPayload, oldErr)
		}
	}
}

func TestPublicEgressParallelBudgetKeepsOtherFamilySuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), publicEgressResultReserve+250*time.Millisecond)
	defer cancel()
	taskDeadline, _ := ctx.Deadline()
	v4Started, v6Finished := make(chan struct{}), make(chan struct{})
	checker := &countingChecker{withContext: func(familyCtx context.Context, family string) protocol.EgressObservation {
		deadline, ok := familyCtx.Deadline()
		if !ok || !deadline.Equal(taskDeadline.Add(-publicEgressResultReserve)) {
			t.Errorf("%s deadline %v vs task %v", family, deadline, taskDeadline)
		}
		if family == "ipv4" {
			close(v4Started)
			select {
			case <-v6Finished:
			case <-familyCtx.Done():
				t.Error("IPv6 did not run alongside the slow IPv4 check")
			}
			<-familyCtx.Done()
			return protocol.UnknownEgress(family, "timeout")
		}
		<-v4Started
		close(v6Finished)
		return protocol.EgressObservation{Status: "available", IP: "2606:4700:4700::1111", Reason: "verified"}
	}}
	executor := &PublicEgressExecutor{checker: checker}
	got, err := executor.Execute(ctx, egressJob(time.Now()))
	if err != nil || !got.Success || got.ErrorCategory != "" || ctx.Err() != nil ||
		got.Result.PublicEgress.IPv4.Reason != "timeout" || got.Result.PublicEgress.IPv6.Status != "available" {
		t.Fatalf("got %+v, %v, task error %v", got, err, ctx.Err())
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	executor.checker = &countingChecker{}
	got, err = executor.Execute(canceled, egressJob(time.Now()))
	if err != nil || got.Success || got.ErrorCategory != "canceled" {
		t.Fatalf("parent cancellation: %+v, %v", got, err)
	}
}

func TestPublicEgressJobDeadlineIncludesResultSubmission(t *testing.T) {
	job := egressJob(time.Now())
	job.TimeoutMS = 250
	post := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") {
			writeTestJSON(t, w, job)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		post <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			_, _ = w.Write([]byte(`{"accepted":true,"job_status":"finished"}`))
		}
	}))
	defer server.Close()
	r := newJobTestRunner(t, server.URL, 3*time.Second, time.Hour, egressTestExecutor(&countingChecker{}))
	client := jobHTTPClient{baseURL: server.URL, token: r.config.Token, client: r.client}
	request := protocol.ClaimRequest{ProtocolVersion: protocol.JobProtocolVersion, AgentEpoch: r.epoch,
		SessionID: r.sessionID, SupportedProbeTypes: []protocol.ProbeType{protocol.ProbeTypePublicEgress}}
	start := time.Now()
	if !r.runJobCycle(context.Background(), client, request) || time.Since(start) >= 1500*time.Millisecond {
		t.Fatalf("result POST escaped the job deadline after %v", time.Since(start))
	}
	select {
	case <-post:
	default:
		t.Fatal("the job never attempted to submit its result")
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
