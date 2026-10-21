package agent

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func a1AgentConfig() protocol.QualityConfig {
	c := n2bConfig()
	c.Version = 2
	c.Semantics = protocol.QualityV2Semantics
	c.RegionID = "region"
	c.IPv6 = true
	c.Targets = nil
	for _, s := range []string{"telecom", "unicom", "mobile"} {
		for _, f := range []string{"ipv4", "ipv6"} {
			for _, p := range []string{"tcp", "icmp"} {
				target := n2bTarget()
				target.Slot = s
				target.Family = f
				target.Protocol = p
				target.Region = "region"
				if p == "icmp" {
					target.Port = 0
				}
				if f == "ipv6" {
					target.Host = "::1"
				}
				c.Targets = append(c.Targets, target)
			}
		}
	}
	return c
}
func TestA1TwelveLaneFairWorkerBoundAndIndependentCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b protocol.QualityBatch
		_ = json.NewDecoder(r.Body).Decode(&b)
		ack := protocol.QualityBatchResponse{}
		for i, g := range b.Gaps {
			_ = g
			ack.Gaps = append(ack.Gaps, protocol.QualityAck{string(rune('0' + i)), "", "committed"})
		}
		_ = json.NewEncoder(w).Encode(ack)
	}))
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	c := a1AgentConfig()
	if err := qualityValidateConfig(c); err != nil {
		t.Fatal(err)
	}
	var offset atomic.Int64
	var active, peak atomic.Int32
	release := make(chan struct{}, 12)
	started := make(chan string, 12)
	var mu sync.Mutex
	counts := map[string]int{}
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	r.quality.random = func() float64 { return .5 }
	r.quality.probe = func(ctx context.Context, t protocol.QualityTarget) qualityProbeResult {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		key := qualityLaneKey(t)
		mu.Lock()
		counts[key]++
		mu.Unlock()
		started <- key
		select {
		case <-release:
		case <-ctx.Done():
		}
		active.Add(-1)
		return qualityProbeResult{outcome: "canceled"}
	}
	r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true, NetworkQualityV2: true}, NetworkQuality: &c})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var observed atomic.Bool
	r.quality.scheduleObserved = func(_ uint64, n int) {
		if n == 12 {
			observed.Store(true)
		}
	}
	go func() { r.runQualityWorker(ctx); close(done) }()
	waitForAgentCondition(t, "12 configured lanes", observed.Load)
	offset.Store(int64(31 * time.Second))
	r.quality.signal()
	for i := 0; i < 6; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("first six did not start")
		}
	}
	if active.Load() != 6 {
		t.Fatal("bound", active.Load())
	}
	select {
	case <-started:
		t.Fatal("more than six concurrent workers")
	case <-time.After(20 * time.Millisecond):
	}
	for i := 0; i < 6; i++ {
		release <- struct{}{}
	}
	for i := 0; i < 6; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("pending lane starved")
		}
	}
	mu.Lock()
	if len(counts) != 12 {
		t.Fatal("lane missing", counts)
	}
	for k, n := range counts {
		if n != 1 {
			t.Fatal("catch-up or duplicate", k, n)
		}
	}
	mu.Unlock()
	if peak.Load() > 6 {
		t.Fatal("unbounded peak", peak.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("workers leaked")
	}
	var schedule qualitySchedule
	schedule.reconcile(&c, time.Now(), func() float64 { return 0 })
	tcp := schedule.lanes["telecom/ipv4/tcp"]
	icmp := schedule.lanes["telecom/ipv4/icmp"]
	var stopped atomic.Bool
	icmp.cancel = func() { stopped.Store(true) }
	for i := range c.Targets {
		if c.Targets[i].Slot == "telecom" && c.Targets[i].Family == "ipv4" && c.Targets[i].Protocol == "icmp" {
			c.Targets[i].ID = strings.Repeat("b", 32)
			c.Targets[i].SlotRevision = "2"
		}
	}
	schedule.reconcile(&c, time.Now(), func() float64 { return 0 })
	if !stopped.Load() || schedule.lanes["telecom/ipv4/tcp"] != tcp {
		t.Fatal("cross-protocol cancellation")
	}
}
func TestA1InitialReportOldServerDowngradeAndAlpineNoWorker(t *testing.T) {
	var calls atomic.Int32
	var bodies []protocol.Report
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get(protocol.QualityVersionHeader) != "2" {
			t.Error("missing HTTP discovery hint")
		}
		var report protocol.Report
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		mu.Lock()
		bodies = append(bodies, report)
		mu.Unlock()
		if report.NetworkQualityV2 != nil {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}})
	}))
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	var seq uint64
	if ok, err := r.sendReport(context.Background(), &seq); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if bodies[0].NetworkQualityV2 != nil {
		t.Fatal("first Report sent unadvertised fields")
	}
	r.qualityV2Supported.Store(true)
	if ok, err := r.sendReport(context.Background(), &seq); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if calls.Load() != 3 || bodies[1].NetworkQualityV2 == nil || bodies[2].NetworkQualityV2 != nil || bodies[1].Sequence != bodies[2].Sequence || r.qualityV2Supported.Load() {
		t.Fatal("strict old Server retry/version fence")
	}
	r.quality.excludedReason = "platform_excluded:alpine"
	r.quality.probe = func(context.Context, protocol.QualityTarget) qualityProbeResult {
		t.Error("Alpine probe ran")
		return qualityProbeResult{}
	}
	r.quality.scheduleObserved = func(uint64, int) { t.Error("Alpine dedicated worker initialized") }
	c := a1AgentConfig()
	r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &c})
	r.runQualityWorker(context.Background())
	if r.quality.allowed || r.quality.fetch || r.quality.config != nil {
		t.Fatal("Alpine acquired work")
	}
	if ok, err := r.sendReport(context.Background(), &seq); err != nil || !ok {
		t.Fatal("base report broken", ok, err)
	}
	if bodies[len(bodies)-1].NetworkQuality {
		t.Fatal("Alpine legacy opt-in")
	}
	caps := r.qualityCapabilities()
	if err := caps.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, l := range caps.Lanes {
		if l.Supported {
			t.Fatal("excluded capability active")
		}
	}
}
