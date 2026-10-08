package agent

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestN2bFallbackDuringRepeatedReportAndNewerFences(t *testing.T) {
	for _, mode := range []string{"same-missing", "newer", "stop-resume"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/agent/network-quality/config" {
					close(entered)
					<-release
					cfg := n2bConfig()
					cfg.Enabled = false
					cfg.Targets[0].Status = "paused"
					_ = json.NewEncoder(w).Encode(cfg)
					return
				}
				_ = json.NewEncoder(w).Encode(protocol.QualityBatchResponse{})
			}))
			defer server.Close()
			r := n2bRunner(t, server.URL, "id", "token")
			r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { r.runQualityWorker(ctx); close(done) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("fallback did not start")
			}
			r.quality.mu.Lock()
			beforeGeneration := r.quality.generation
			r.quality.mu.Unlock()
			want := "1"
			if mode == "same-missing" {
				for i := 0; i < 5; i++ {
					r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}})
				}
				r.quality.mu.Lock()
				if r.quality.generation != beforeGeneration {
					t.Fatal("duplicate report invalidated valid read")
				}
				r.quality.mu.Unlock()
			} else {
				if mode == "stop-resume" {
					r.stopQuality("fixture_stop")
				}
				cfg := n2bConfig()
				cfg.Revision = "2"
				cfg.Enabled = false
				cfg.Targets[0].Status = "paused"
				r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &cfg})
				want = "2"
			}
			close(release)
			waitForAgentCondition(t, "fallback installed or newer preserved", func() bool {
				r.quality.mu.Lock()
				defer r.quality.mu.Unlock()
				return r.quality.config != nil && r.quality.config.Revision == want && !r.quality.fetch
			})
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("fallback did not stop")
			}
		})
	}
}

func TestN2bStopResumeFenceDiscardsOldResultEvenSameTarget(t *testing.T) {
	uploaded := make(chan protocol.QualitySample, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch protocol.QualityBatch
		_ = json.NewDecoder(r.Body).Decode(&batch)
		ack := protocol.QualityBatchResponse{}
		for i, s := range batch.Samples {
			uploaded <- s
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
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	queued := make(chan protocol.QualitySample, 8)
	r.quality.sampleQueued = func(seq, outcome string) { queued <- protocol.QualitySample{Sequence: seq, Outcome: outcome} }
	startupAck := make(chan struct{})
	var firstUpload sync.Once
	r.quality.uploadScheduled = func(time.Time) { firstUpload.Do(func() { close(startupAck) }) }
	resumed := make(chan struct{})
	var observed sync.Once
	r.quality.scheduleObserved = func(auth uint64, lanes int) {
		if auth == 1 && lanes == 1 {
			observed.Do(func() { close(resumed) })
		}
	}
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return 0 }
	r.quality.probe = func(ctx context.Context, _ protocol.QualityTarget) qualityProbeResult {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release
			return qualityProbeResult{outcome: "canceled"}
		}
		zero := 0.0
		return qualityProbeResult{outcome: "success", ip: "127.0.0.1", latency: &zero}
	}
	cfg := n2bConfig()
	response := protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &cfg}
	r.observeQualityReport(true, response)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	select {
	case <-startupAck:
	case <-time.After(time.Second):
		t.Fatal("startup fact not ACKed")
	}
	advance.Store(int64(25 * time.Second))
	r.quality.signal()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first probe not started")
	}
	// Both notifications may collapse to one wake: stopSerial is independent.
	r.stopQuality("fixture_stop")
	r.observeQualityReport(true, response)
	r.quality.mu.Lock()
	if !r.quality.allowed || r.quality.stopSerial != 1 {
		t.Fatal("resume lost stop fence")
	}
	r.quality.mu.Unlock()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("same target old flight not cancelled")
	}
	close(release)
	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("resumed schedule not installed")
	}
	advance.Store(int64(55 * time.Second))
	r.quality.signal()
	select {
	case sample := <-queued:
		if sample.Outcome != "success" || sample.Sequence != "1" {
			t.Fatal("pre-stop sample reentered resumed queue", sample)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed lane did not sample", "probe_calls", calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resume shutdown leaked")
	}
}
