package agent

import (
	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	appserver "404-probe/internal/server"
	"404-probe/internal/storage"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func n2bRunner(t *testing.T, url, id, token string) *Runner {
	t.Helper()
	r, err := NewWithExecutor(Config{ServerURL: url, AllowInsecureHTTP: true, AgentID: id, Token: token, Interval: 10 * time.Second, Timeout: 5 * time.Second, StatePath: filepath.Join(t.TempDir(), "epoch")}, slog.New(slog.NewTextHandler(io.Discard, nil)), UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	r.collector = reportCollectorFunc(func(context.Context) (protocol.Report, error) {
		return protocol.Report{Hostname: "quality-fixture", OS: "linux", Arch: "amd64", BootID: "boot", Uptime: 1, CPUPercent: 1, Load1: 1, Load5: 1, Load15: 1, RAMUsed: 1, RAMTotal: 2, RAMPercent: 50, DiskUsed: 1, DiskTotal: 2, DiskPercent: 50, RXBytes: 1, TXBytes: 1}, nil
	})
	return r
}
func TestN2bRealServerFallbackLostACKAndUniqueMinute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, _ := auth.NewID()
	token, hash, _ := auth.NewToken()
	if err := s.AddAgent(ctx, id, "test", hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	app, err := appserver.NewApp(s, 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown()
	var resources, fallbacks, duplicates, dropped, qualityRequests atomic.Int32
	lostSample := make(chan protocol.QualitySample, 1)
	var firstCanonical []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/report" {
			resources.Add(1)
		}
		if r.URL.Path == "/api/v1/agent/network-quality/config" {
			fallbacks.Add(1)
		}
		var batch protocol.QualityBatch
		if r.URL.Path == "/api/v1/agent/network-quality" {
			qualityRequests.Add(1)
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			_ = json.Unmarshal(raw, &batch)
			// Genuine 503 before Store handling; queued sample must survive.
			if len(batch.Samples) > 0 && qualityRequests.Load() == 2 {
				w.WriteHeader(503)
				return
			}
		}
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, r)
		if len(batch.Samples) > 0 && recorder.Code == 200 {
			var ack protocol.QualityBatchResponse
			_ = json.Unmarshal(recorder.Body.Bytes(), &ack)
			for _, item := range ack.Results {
				if item[2] == "duplicate" {
					duplicates.Add(1)
				}
			}
			if dropped.CompareAndSwap(0, 1) {
				firstCanonical, _ = json.Marshal(batch.Samples[0])
				lostSample <- batch.Samples[0]
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					connection.Close()
				}
				return
			}
			for _, sample := range batch.Samples {
				if sample.Sequence == "1" {
					raw, _ := json.Marshal(sample)
					if firstCanonical != nil && !bytes.Equal(raw, firstCanonical) {
						t.Error("sample changed across lost ACK retry")
					}
				}
			}
		}
		for k, values := range recorder.Header() {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer server.Close()
	r := n2bRunner(t, server.URL, id, token)
	var sequence uint64
	if accepted, err := r.sendReport(ctx, &sequence); err != nil || !accepted || !r.qualitySupported.Load() {
		t.Fatal("first capability handshake", accepted, err)
	}
	choices := []protocol.QualityChoice{}
	for _, slot := range []string{"telecom", "unicom", "mobile"} {
		choices = append(choices, protocol.QualityChoice{Slot: slot, Source: "manual", Protocol: "tcp", Host: strings.Repeat("&", 253), Port: 65535})
	}
	config, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: "0", Enabled: true, IPv6: true, Choices: choices}, func(c protocol.QualityChoice, family string) (protocol.QualityTarget, error) {
		return protocol.QualityTarget{Host: c.Host, Port: c.Port, EndpointVersion: "fixture"}, nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := r.sendReport(ctx, &sequence); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	r.quality.mu.Lock()
	if !r.quality.fetch || r.quality.config != nil {
		t.Fatal("oversize compact did not request fallback")
	}
	r.quality.mu.Unlock()
	var advance atomic.Int64
	var probes atomic.Int32
	uploadDelays := make(chan time.Time, 4)
	r.quality.uploadScheduled = func(next time.Time) { uploadDelays <- next }
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return .5 }
	r.quality.probe = func(context.Context, protocol.QualityTarget) qualityProbeResult {
		probes.Add(1)
		return qualityProbeResult{outcome: "dns_error"}
	}
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	waitForAgentCondition(t, "real fallback", func() bool {
		r.quality.mu.Lock()
		defer r.quality.mu.Unlock()
		return r.quality.config != nil && !r.quality.fetch
	})
	if fallbacks.Load() != 1 {
		t.Fatal("fallback flood", fallbacks.Load())
	}
	select {
	case <-uploadDelays:
	case <-time.After(time.Second):
		t.Fatal("initial known-startup gap ACK missing")
	}
	advance.Store(int64(31 * time.Second))
	r.quality.signal()
	waitForAgentCondition(t, "six real lanes", func() bool { return probes.Load() == 6 })
	// First sample upload is 503. Advance only upload deadline, below next probe.
	waitForAgentCondition(t, "503 fixture", func() bool { return qualityRequests.Load() >= 2 })
	select {
	case <-uploadDelays:
	case <-time.After(time.Second):
		t.Fatal("503 retry not scheduled")
	}
	advance.Store(int64(42 * time.Second))
	r.quality.signal()
	var first protocol.QualitySample
	select {
	case first = <-lostSample:
	case <-time.After(2 * time.Second):
		t.Fatal("lost response fixture not reached")
	}
	select {
	case <-uploadDelays:
	case <-time.After(time.Second):
		t.Fatal("lost ACK retry not scheduled")
	}
	advance.Store(int64(63 * time.Second))
	r.quality.signal()
	waitForAgentCondition(t, "duplicate after lost committed response", func() bool { return duplicates.Load() > 0 })
	history, err := s.QualityHistory(ctx, id, first.TargetID, 1, time.Now().Add(70*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(history)
	t.Logf("actual Store history after 503/lost ACK replay: %s", encoded)
	// All summaries derive real raw; duplicates must not count twice.
	if history.Recent.Attempts < 1 || history.Recent.Attempts > 2 {
		t.Fatal("duplicate minute/raw counted", history.Recent)
	}
	if _, err := s.DisableAgent(ctx, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.sendReport(ctx, &sequence); err != ErrAgentDisabled {
		t.Fatal("disabled barrier", err)
	}
	waitForAgentCondition(t, "authorization stop", func() bool { r.quality.mu.Lock(); defer r.quality.mu.Unlock(); return !r.quality.allowed })
	count := probes.Load()
	advance.Store(int64(100 * time.Second))
	r.quality.signal()
	time.Sleep(20 * time.Millisecond)
	if probes.Load() != count {
		t.Fatal("disabled continued sampling")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not Wait/stop")
	}
	if resources.Load() != 3 || fallbacks.Load() != 1 {
		t.Fatal(resources.Load(), fallbacks.Load())
	}
	_ = config
}

func TestN2bCancelledLanesStillCountAgainstSixAndWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch protocol.QualityBatch
		_ = json.NewDecoder(r.Body).Decode(&batch)
		ack := protocol.QualityBatchResponse{}
		for i, s := range batch.Samples {
			ack.Results = append(ack.Results, protocol.QualityAck{string(rune('0' + i)), s.Sequence, "committed"})
		}
		for i := range batch.Gaps {
			ack.Gaps = append(ack.Gaps, protocol.QualityAck{string(rune('0' + i)), "", "committed"})
		}
		_ = json.NewEncoder(w).Encode(ack)
	}))
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	var advance atomic.Int64
	var calls, active, peak atomic.Int32
	release := make(chan struct{})
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return 0 }
	r.quality.probe = func(ctx context.Context, _ protocol.QualityTarget) qualityProbeResult {
		calls.Add(1)
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		<-ctx.Done()
		<-release
		return qualityProbeResult{outcome: "canceled"}
	}
	cfg := n2bConfig()
	cfg.IPv6 = true
	cfg.Targets = nil
	for _, slot := range []string{"telecom", "unicom", "mobile"} {
		for _, family := range []string{"ipv4", "ipv6"} {
			target := n2bTarget()
			target.Slot = slot
			target.Family = family
			cfg.Targets = append(cfg.Targets, target)
		}
	}
	r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &cfg})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	time.Sleep(10 * time.Millisecond)
	advance.Store(int64(25 * time.Second))
	r.quality.signal()
	waitForAgentCondition(t, "six blocked probes", func() bool { return calls.Load() == 6 })
	for i := 2; i < 12; i++ {
		cfg.Revision = string(rune('0' + i))
		if i >= 10 {
			cfg.Revision = "1" + string(rune('0'+i-10))
		}
		for j := range cfg.Targets {
			cfg.Targets[j].ID = strings.Repeat("b", 32)
			cfg.Targets[j].SlotRevision = cfg.Revision
		}
		r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &cfg})
		advance.Add(int64(30 * time.Second))
		r.quality.signal()
		time.Sleep(2 * time.Millisecond)
	}
	if calls.Load() != 6 || peak.Load() != 6 {
		t.Fatal("cancelled old lanes spawned unbounded probes", calls.Load(), peak.Load())
	}
	cancel()
	select {
	case <-done:
		t.Fatal("returned before cancelled probes exited")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("failed to Wait after release")
	}
	if active.Load() != 0 {
		t.Fatal("probe leaked")
	}
}

func TestN2bSleepingWakeSkipsCatchUpAndIndependentInitialJitter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch protocol.QualityBatch
		_ = json.NewDecoder(r.Body).Decode(&batch)
		ack := protocol.QualityBatchResponse{}
		for i, s := range batch.Samples {
			ack.Results = append(ack.Results, protocol.QualityAck{string(rune('0' + i)), s.Sequence, "committed"})
		}
		for i := range batch.Gaps {
			ack.Gaps = append(ack.Gaps, protocol.QualityAck{string(rune('0' + i)), "", "committed"})
		}
		_ = json.NewEncoder(w).Encode(ack)
	}))
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	var advance atomic.Int64
	var calls atomic.Int32
	queued := make(chan struct{}, 2)
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return 0 }
	r.quality.probe = func(context.Context, protocol.QualityTarget) qualityProbeResult {
		calls.Add(1)
		zero := 0.0
		return qualityProbeResult{outcome: "success", ip: "127.0.0.1", latency: &zero}
	}
	r.quality.sampleQueued = func(string, string) { queued <- struct{}{} }
	cfg := n2bConfig()
	r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &cfg})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	time.Sleep(10 * time.Millisecond)
	advance.Store(int64(1000 * time.Second))
	r.quality.signal()
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("sleep wake didn't sample")
	}
	for i := 0; i < 20; i++ {
		r.quality.signal()
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("sleep resume catch-up burst", calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sleep wake leaked")
	}
	var plan qualitySchedule
	cfg.Targets = append(cfg.Targets, func() protocol.QualityTarget { target := n2bTarget(); target.Slot = "unicom"; return target }())
	draw := 0
	plan.reconcile(&cfg, time.Now(), func() float64 {
		draw++
		if draw == 1 {
			return 0
		}
		return 1
	})
	if plan.lanes["telecom/ipv4"].next.Equal(plan.lanes["unicom/ipv4"].next) {
		t.Fatal("initial timers not independently jittered")
	}
}
