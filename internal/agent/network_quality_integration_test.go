package agent

import (
	"404-probe/internal/protocol"
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
	if plan.lanes["telecom/ipv4/tcp"].next.Equal(plan.lanes["unicom/ipv4/tcp"].next) {
		t.Fatal("initial timers not independently jittered")
	}
}
