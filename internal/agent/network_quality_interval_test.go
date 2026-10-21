package agent

import (
	"404-probe/internal/protocol"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The nominal period is the user's 30-300 s in steps of 30 s; the +/-20% jitter of v1.0.4 stays
// (decision N5-A): every single delay lies in [0.8, 1.2] x period and the period is never below 30 s.
func TestIntervalSelectionAndJitterBounds(t *testing.T) {
	for ms := 30000; ms <= 300000; ms += 30000 {
		c := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
		c.IntervalMS = ms
		if err := qualityValidateConfigFor(c, true); err != nil {
			t.Errorf("%d ms refused: %v", ms, err)
		}
		period := time.Duration(ms) * time.Millisecond
		if period < 30*time.Second {
			t.Fatal("period below 30 s")
		}
		for _, x := range []float64{0, .25, .5, .75, 1} {
			d := qualityDelay(period, func() float64 { return x })
			if d < period*8/10 || d > period*12/10 {
				t.Errorf("%d ms with %.2f: %v", ms, x, d)
			}
		}
	}
	for _, ms := range []int{0, 1, 29999, 30001, 45000, 330000, 600000, -30000} {
		c := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
		c.IntervalMS = ms
		if qualityValidateConfigFor(c, true) == nil {
			t.Errorf("%d ms accepted", ms)
		}
	}
	c := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	c.IntervalMS, c.JitterPercent = 60000, 10
	if qualityValidateConfigFor(c, true) == nil {
		t.Error("another jitter accepted")
	}
	// The older semantics keep their fixed 30 s.
	old := n2bConfig()
	old.IntervalMS = 60000
	if qualityValidateConfig(old) == nil {
		t.Error("a longer period was accepted without staged semantics")
	}
}

func TestScheduleUsesTheConfiguredPeriodAndAdoptsAShorterOne(t *testing.T) {
	start := time.Now()
	c := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	c.IntervalMS = 300000
	var plan qualitySchedule
	plan.reconcile(&c, start, func() float64 { return .5 })
	lane := plan.lanes["telecom/ipv4/tcp"]
	if got := lane.next.Sub(start); got != 300*time.Second {
		t.Fatalf("first probe after %v", got)
	}
	// Same frequency: nothing moves.
	plan.reconcile(&c, start.Add(time.Second), func() float64 { return 0 })
	if lane.next.Sub(start) != 300*time.Second {
		t.Fatal("an unchanged configuration moved the schedule")
	}
	// A shorter period pulls the probe forward at once (within 0.8-1.2 x the new period).
	c.IntervalMS = 30000
	c.Revision = "2"
	plan.reconcile(&c, start.Add(10*time.Second), func() float64 { return 1 })
	if got := lane.next.Sub(start.Add(10 * time.Second)); got != 36*time.Second {
		t.Fatalf("after a shorter period: %v", got)
	}
	// A longer period does not pull anything back; it applies once the scheduled probe has run.
	scheduled := lane.next
	c.IntervalMS = 120000
	plan.reconcile(&c, start.Add(11*time.Second), func() float64 { return 0 })
	if !lane.next.Equal(scheduled) || lane.interval != 120*time.Second {
		t.Fatalf("a longer period moved the schedule: %v", lane.next.Sub(start))
	}
}

// The worker itself waits the configured period between probes of a lane.
func TestWorkerProbesOncePerConfiguredPeriod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	r.setQualityUserTargets(true)
	r.quality.nat64Resolver = fixedNAT64{}
	var advance, probes atomic.Int64
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return .5 } // exactly the nominal period
	r.quality.probe = func(context.Context, protocol.QualityTarget) qualityProbeResult {
		probes.Add(1)
		return qualityProbeResult{outcome: "canceled"}
	}
	laneSeen := make(chan struct{}, 1)
	r.quality.scheduleObserved = func(_ uint64, n int) {
		if n == 1 {
			select {
			case laneSeen <- struct{}{}:
			default:
			}
		}
	}
	c := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	c.IntervalMS = 120000
	r.observeQualityReport(true, protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true}, NetworkQuality: &c})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	<-laneSeen
	step := func(by time.Duration, want int64) {
		t.Helper()
		advance.Add(int64(by))
		r.quality.signal()
		time.Sleep(60 * time.Millisecond)
		if got := probes.Load(); got != want {
			t.Fatalf("after advancing %v: %d probes, want %d", by, got, want)
		}
	}
	step(119*time.Second, 0)
	step(2*time.Second, 1)
	step(119*time.Second, 1)
	step(2*time.Second, 2)
	cancel()
	<-done
}
