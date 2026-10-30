package server

import (
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDashboardWarmupIsBoundedAndDoesNotGateRequests(t *testing.T) {
	a, s, _, _ := testApp(t)
	defer s.Close()
	defer a.Shutdown()
	for i := range 12 {
		if err := s.AddAgent(t.Context(), fmt.Sprintf("%032x", i+1), fmt.Sprint("warm", i), []byte(fmt.Sprint("hash", i)), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}, 20), make(chan struct{})
	var active, maximum atomic.Int64
	a.dashboardWarmupProbe = func(string) {
		n := active.Add(1)
		for m := maximum.Load(); n > m && !maximum.CompareAndSwap(m, n); m = maximum.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-a.shutdown.Done():
		}
		active.Add(-1)
	}
	a.startDashboardNetworkWarmup()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	select {
	case <-entered:
		t.Fatal("unbounded warmup concurrency")
	default:
	}
	if v := a.cachedDashboardNetwork(httptest.NewRequest("GET", "/", nil), "foreground", time.Now()); !v.Known {
		t.Fatal("foreground request waited for warmup")
	}
	close(release)
	select {
	case <-a.dashboardWarmupDone:
	case <-time.After(3 * time.Second):
		t.Fatal("warmup did not finish")
	}
	if maximum.Load() > 2 {
		t.Fatal(maximum.Load())
	}
	a.dashboardMu.Lock()
	entry := a.dashboardNetwork["foreground"]
	a.dashboardMu.Unlock()
	if entry.Expires.Sub(time.UnixMilli(entry.Value.CheckedAt)) > 5001*time.Millisecond {
		t.Fatal("TTL extended")
	}
}

func TestDashboardWarmupFailureAndShutdownAreHarmless(t *testing.T) {
	a, s, _, _ := testApp(t)
	s.Close()
	a.startDashboardNetworkWarmup()
	a.Shutdown()
	select {
	case <-a.dashboardWarmupDone:
	default:
		t.Fatal("warmup survived shutdown")
	}
	a.dashboardMu.Lock()
	defer a.dashboardMu.Unlock()
	if len(a.dashboardNetwork) != 0 {
		t.Fatal("failed warmup poisoned cache")
	}
}
