package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/platformsupport"
	"404-probe/internal/protocol"
)

type blockedDiskHealth struct {
	entered chan struct{}
	release chan struct{}
}

func (d blockedDiskHealth) Run(context.Context) { close(d.entered); <-d.release }
func (d blockedDiskHealth) Cleanup()            {}
func (d blockedDiskHealth) Snapshot() protocol.DiskHealth {
	start := time.Now().Add(-31 * time.Second).UnixMilli()
	return protocol.DiskHealth{Status: protocol.DiskError, Reasons: []protocol.DiskHealthReason{protocol.DiskStalled}, StartedAt: start, StalledSince: start}
}

func TestHostHealthOnlyFollowsAcceptedCapabilitiesAndDoesNotWaitForProbe(t *testing.T) {
	var calls, collections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/report" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		n := calls.Add(1)
		var report protocol.Report
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil || report.Validate() != nil {
			t.Errorf("invalid report: %v %v", err, report.Validate())
		}
		// Requests 1-3 are what a strict older Server can accept. An advertisement
		// on a rejected report must have no effect on the next request.
		if (report.HostHealth != nil) != (n == 4) {
			t.Errorf("request %d carries host health = %v", n, report.HostHealth != nil)
		}
		if n == 4 && report.HostHealth != nil && report.HostHealth.Disk.Status != protocol.DiskError {
			t.Error("stalled probe did not reach report")
		}
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: n != 2, Capabilities: protocol.ReportCapabilities{HostHealthReport: n == 2 || n == 3}})
	}))
	defer server.Close()
	r, err := NewWithReportCollector(Config{ServerURL: server.URL, AgentID: "test", Token: "synthetic", Interval: 10 * time.Millisecond,
		Timeout: time.Second, StatePath: filepath.Join(t.TempDir(), "epoch"), AllowInsecureHTTP: true}, nil,
		reportCollectorFunc(func(ctx context.Context) (protocol.Report, error) {
			collections.Add(1)
			return (platformMetrics{}).Collect(ctx)
		}))
	if err != nil {
		t.Fatal(err)
	}
	disk := blockedDiskHealth{entered: make(chan struct{}), release: make(chan struct{})}
	r.diskHealth = disk
	defer close(disk.release)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-disk.entered:
	case <-time.After(time.Second):
		t.Fatal("probe was not started")
	}
	waitForAgentCondition(t, "reports while probe is blocked", func() bool { return calls.Load() >= 5 })
	if collections.Load() < 5 {
		t.Fatal("metrics collection stopped behind disk probe")
	}
	cancel()
}

func TestPlatformTimeoutKeepsOSAndClosesGatesUntilRetry(t *testing.T) {
	m := &platformMonitor{timeout: 100 * time.Millisecond, read: func() platformsupport.Identity { return platformsupport.Identity{OS: "debian 12"} }}
	wake := func() {}
	m.refresh(wake)
	release := make(chan struct{})
	woke := make(chan struct{}, 2)
	m.read = func() platformsupport.Identity { <-release; return platformsupport.Identity{OS: "late"} }
	done := make(chan struct{})
	go func() { m.refresh(func() { woke <- struct{}{} }); close(done) }()
	<-woke
	s := m.snapshot()
	if s.lastOS != "debian 12" || s.identity.OS != "" || !s.identity.Deferred.DeferredUnsupported || !s.identity.Quality.Excluded {
		t.Errorf("timed-out identity: %+v", s)
	}
	close(release)
	<-done
	m.read = func() platformsupport.Identity { return platformsupport.Identity{OS: "debian 13"} }
	m.refresh(wake)
	s = m.snapshot()
	if s.lastOS != "debian 13" || s.identity.Deferred.DeferredUnsupported || s.identity.Quality.Excluded {
		t.Fatalf("recovered identity: %+v", s)
	}
}

func TestHostHealthReportFallsBackAfterServerRollback(t *testing.T) {
	type step struct {
		sequence   uint64
		carries    bool
		badRequest bool
		advertises bool
	}
	steps := []step{
		{1, false, false, true},
		{2, true, true, false}, {2, false, false, false},
		{3, false, false, false},
		{4, false, false, true},
		{5, true, false, true},
		{6, true, true, false}, {6, false, true, false},
		{7, false, false, false},
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		i := int(requests.Add(1)) - 1
		if i >= len(steps) {
			t.Errorf("unexpected report request %d", i+1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		want := steps[i]
		var report protocol.Report
		if err := json.NewDecoder(req.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		if report.Sequence != want.sequence || (report.HostHealth != nil) != want.carries {
			t.Errorf("request %d: sequence=%d host health=%v, want %d/%v", i+1, report.Sequence, report.HostHealth != nil, want.sequence, want.carries)
		}
		if !want.carries && report.OS != protocol.UnavailableSystemName {
			t.Errorf("request %d: missing legacy OS placeholder", i+1)
		}
		if want.badRequest {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{HostHealthReport: want.advertises}})
	}))
	defer server.Close()
	r, err := NewWithReportCollector(Config{ServerURL: server.URL, AgentID: "test", Token: "synthetic", Interval: time.Second,
		Timeout: time.Second, StatePath: filepath.Join(t.TempDir(), "epoch"), AllowInsecureHTTP: true}, nil,
		reportCollectorFunc(func(ctx context.Context) (protocol.Report, error) {
			report, err := (platformMetrics{}).Collect(ctx)
			report.OS = ""
			return report, err
		}))
	if err != nil {
		t.Fatal(err)
	}
	var sequence uint64
	for _, wantAccepted := range []bool{true, true, true, true, true, false, true} {
		accepted, err := r.sendReport(context.Background(), &sequence)
		if err != nil || accepted != wantAccepted {
			t.Fatalf("report %d accepted=%v err=%v, want %v", sequence, accepted, err, wantAccepted)
		}
	}
	if requests.Load() != int32(len(steps)) {
		t.Fatalf("sent %d requests, want %d", requests.Load(), len(steps))
	}
}
