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
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// userSlotSetting is one slot of a saved configuration for the integration tests.
func userSlotSetting(slot, host string, port int, subs ...string) protocol.QualityV4Slot {
	s := protocol.QualityV4Slot{Slot: slot, Kind: "custom", Region: "R-" + slot, ISP: "I-" + slot, Host: host, Port: port, SlotEnabled: true}
	for _, sub := range subs {
		switch sub {
		case "v4tcp":
			s.IPv4TCP = true
		case "v4icmp":
			s.IPv4ICMP = true
		case "v6tcp":
			s.IPv6TCP = true
		case "v6icmp":
			s.IPv6ICMP = true
		}
	}
	return s
}

// saveUserTargets stores and switches on a configuration through the Server's own storage API.
func saveUserTargets(t *testing.T, s *storage.Store, id string, slots ...protocol.QualityV4Slot) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	current, err := s.GetQualityConfigV4(ctx, id, now)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveQualityConfigV4(ctx, id, protocol.QualityV4SaveRequest{ExpectedRevision: current.SettingsRevision, IntervalMS: 30000, Slots: slots}, now)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := s.SetQualityEnabledV4(ctx, id, protocol.QualityV4EnabledRequest{ExpectedRevision: saved.SettingsRevision, Enabled: &on}, now); err != nil {
		t.Fatal(err)
	}
}

// stagedRefused is what a TCP probe of a reachable address that refuses the connection produces.
func stagedRefused(ip string) qualityProbeResult {
	ms := 0.2 // a probe that returns at once still reports a duration of about zero
	d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, DNSOutcome: "success", ConnectAttempted: true, ConnectComplete: true, ConnectMS: &ms, ProbeComplete: true, ErrorStage: "connect", ErrorCode: "refused"}
	return qualityProbeResult{outcome: "refused", ip: ip, details: d.Encode()}
}

// Real new Agent against the real new Server: the negotiation, the oversized-configuration fallback,
// a 503, a lost response and its replay, and the authorization stop (formerly the catalog-era test).
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
			if r.Header.Get(protocol.QualityTargetsHeader) != protocol.QualityTargetsUserV1 {
				t.Error("the fallback pull of a negotiated session lost the header")
			}
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
	r.quality.ipv6 = &qualityIPv6Monitor{scan: func() (bool, error) { return true, nil }}
	r.quality.nat64Resolver = fixedNAT64{}
	var sequence uint64
	if accepted, err := r.sendReport(ctx, &sequence); err != nil || !accepted || !r.qualitySupported.Load() || !r.qualityUserTargets.Load() {
		t.Fatal("first capability handshake", accepted, err)
	}
	// Three slots at the longest names the Server accepts: IPv4 and IPv6 TCP each, twelve targets in all.
	long := strings.Repeat("a", 61) + "." + strings.Repeat("b", 61) + "." + strings.Repeat("c", 61) + "." + strings.Repeat("d", 61) + ".com"
	saveUserTargets(t, s, id,
		userSlotSetting("telecom", long, 65535, "v4tcp", "v6tcp"),
		userSlotSetting("unicom", long, 65534, "v4tcp", "v6tcp"),
		userSlotSetting("mobile", long, 65533, "v4tcp", "v6tcp"))
	if accepted, err := r.sendReport(ctx, &sequence); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	r.quality.mu.Lock()
	if !r.quality.fetch || (r.quality.config != nil && r.quality.config.Enabled) {
		t.Fatal("oversize compact did not request fallback")
	}
	r.quality.mu.Unlock()
	var advance atomic.Int64
	var probes atomic.Int32
	uploadDelays := make(chan time.Time, 4)
	r.quality.uploadScheduled = func(next time.Time) { uploadDelays <- next }
	var scheduledLanes atomic.Int32
	r.quality.scheduleObserved = func(_ uint64, n int) { scheduledLanes.Store(int32(n)) }
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return .5 }
	r.quality.probe = func(_ context.Context, target protocol.QualityTarget) qualityProbeResult {
		probes.Add(1)
		if target.Source != "user" {
			t.Error("a probe for a target that is not user-entered")
		}
		if target.Family == "ipv6" {
			return stagedRefused("2606:4700::1111")
		}
		return stagedRefused("93.184.216.34")
	}
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	waitForAgentCondition(t, "real fallback", func() bool {
		r.quality.mu.Lock()
		defer r.quality.mu.Unlock()
		return r.quality.config != nil && r.quality.config.Enabled && !r.quality.fetch
	})
	if fallbacks.Load() != 1 {
		t.Fatal("fallback flood", fallbacks.Load())
	}
	select {
	case <-uploadDelays:
	case <-time.After(time.Second):
		t.Fatal("initial known-startup gap ACK missing")
	}
	// The lanes appear once the first prefix discovery has finished; the clock moves only after that.
	waitForAgentCondition(t, "six lanes scheduled", func() bool { return scheduledLanes.Load() == 6 })
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
	history, err := stagedGroup(s, ctx, id, first.TargetID)
	if err != nil || history == nil {
		t.Fatal(err, history)
	}
	encoded, _ := json.Marshal(history)
	t.Logf("actual Store history after 503/lost ACK replay: %s", encoded)
	// All summaries derive real raw; duplicates must not count twice.
	if history.Window.ConnectAttempts < 1 || history.Window.ConnectAttempts > 2 {
		t.Fatal("duplicate minute/raw counted", history.Window)
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
}

// Real runner, real TCP probes against loopback listeners, real Server and SQLite: the user-entered
// names resolve per family, IPv4 and IPv6 TCP are measured and stored, and a lane the Agent cannot
// run (no ICMP) is never started and never produces a sample.
func TestA1RealRunnerV2TCPFamiliesStoreAndUnavailableICMP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, _ := auth.NewID()
	token, hash, _ := auth.NewToken()
	if err := s.AddAgent(ctx, id, "a1", hash, time.Now()); err != nil {
		t.Fatal(err)
	}
	app, err := appserver.NewApp(s, 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown()
	var acks []string
	var ackMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/network-quality" {
			app.Handler().ServeHTTP(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var batch protocol.QualityBatch
		_ = json.Unmarshal(raw, &batch)
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, r)
		ackMu.Lock()
		for _, sample := range batch.Samples {
			acks = append(acks, sample.Sequence+"/"+sample.TargetID[:6]+"/"+sample.Outcome+" "+recorder.Body.String())
		}
		ackMu.Unlock()
		for k, v := range recorder.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer server.Close()
	listener4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener4.Close()
	listener6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal("IPv6 loopback required by this test", err)
	}
	defer listener6.Close()
	for _, l := range []net.Listener{listener4, listener6} {
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				conn.Close()
			}
		}()
	}
	r := n2bRunner(t, server.URL, id, token)
	r.quality.ipv6 = &qualityIPv6Monitor{scan: func() (bool, error) { return true, nil }}
	r.quality.nat64Resolver = fixedNAT64{}
	dialer := &net.Dialer{}
	tcp := &TCPExecutor{
		qualityResolver: familyResolver{"ip4": "93.184.216.34", "ip6": "2606:4700::1111"},
		qualityDial: func(ctx context.Context, network, address string) (net.Conn, error) {
			// The check before the connection already ran on the resolved public address; the
			// fixture then connects to the listener of that family.
			if network == "tcp6" {
				return dialer.DialContext(ctx, network, listener6.Addr().String())
			}
			return dialer.DialContext(ctx, network, listener4.Addr().String())
		},
	}
	r.executor = &ProbeExecutor{http: NewHTTPExecutor(), tcp: tcp, icmp: &ICMPExecutor{backend: fakeICMPBackend{}}}
	var seq uint64
	if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
		t.Fatal("handshake", ok, err)
	}
	saveUserTargets(t, s, id,
		userSlotSetting("telecom", "one.example.com", 443, "v4tcp", "v6tcp", "v4icmp", "v6icmp"),
		userSlotSetting("unicom", "two.example.com", 8443, "v4tcp", "v6tcp"),
		userSlotSetting("mobile", "three.example.com", 80, "v6tcp"))
	if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// The Agent's clock runs a minute behind the Server's, so every sample is already in the past when it
	// is stored and counts at once (a sample from the future waits until it has matured).
	var offset atomic.Int64
	offset.Store(0)
	var observed, queued atomic.Int32
	uploadScheduled := make(chan time.Time, 8)
	r.quality.sampleQueued = func(string, string) { queued.Add(1) }
	r.quality.uploadScheduled = func(next time.Time) { uploadScheduled <- next }
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	// Lanes fall due at 27 s (0.9 of the period), the first upload at 30 s: all five samples are
	// queued before the single upload that carries them.
	r.quality.random = func() float64 { return .25 }
	r.quality.scheduleObserved = func(_ uint64, n int) { observed.Store(int32(n)) }
	waitForAgentCondition(t, "configuration installed", func() bool {
		r.quality.mu.Lock()
		defer r.quality.mu.Unlock()
		return r.quality.config != nil && r.quality.config.Enabled
	})
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	r.quality.mu.Lock()
	config := *r.quality.config
	r.quality.mu.Unlock()
	expected := 0
	for _, x := range config.Targets {
		if x.Status == "active" {
			expected++
		}
	}
	// 5 TCP lanes: telecom v4+v6, unicom v4+v6, mobile v6. The two ICMP lanes are not runnable here.
	if expected != 5 {
		t.Fatalf("active lanes %d, want 5: %+v", expected, config.Targets)
	}
	waitForAgentCondition(t, "runtime-supported lanes", func() bool { return int(observed.Load()) == expected })
	select {
	case <-uploadScheduled:
	case <-time.After(time.Second):
		t.Fatal("startup gap ACK")
	}
	offset.Add(int64(28 * time.Second))
	r.quality.signal()
	waitForAgentCondition(t, "real measurements queued", func() bool { return queued.Load() >= int32(expected) })
	offset.Add(int64(3 * time.Second))
	r.quality.signal()
	expectedIP := func(target protocol.QualityTarget) string {
		if target.Family == "ipv6" {
			return "2606:4700::1111"
		}
		return "93.184.216.34"
	}
	storedAll := func() bool {
		for _, target := range config.Targets {
			if target.Status != "active" {
				continue
			}
			g, err := stagedGroup(s, ctx, id, target.ID)
			if err != nil || g == nil || g.Window.ConnectSuccesses < 1 || g.Latest == nil || g.Latest.ResolvedIP != expectedIP(target) {
				return false
			}
		}
		return true
	}
	for wait := time.Now().Add(2 * time.Second); time.Now().Before(wait) && !storedAll(); time.Sleep(5 * time.Millisecond) {
	}
	if !storedAll() {
		ackMu.Lock()
		t.Log(acks)
		ackMu.Unlock()
		for _, target := range config.Targets {
			if target.Status == "active" {
				g, err := stagedGroup(s, ctx, id, target.ID)
				wire, _ := json.Marshal(g)
				t.Log(target.Slot, target.Family, target.ID[:6], err, string(wire))
			}
		}
		t.Fatal("not every active lane stored a real TCP sample")
	}
	for _, target := range config.Targets {
		if target.Status != "active" {
			g, err := stagedGroup(s, ctx, id, target.ID)
			if err != nil || (g != nil && g.Window.Count != 0) {
				t.Fatal("a lane that does not run fabricated a sample", target.Slot, target.Family, target.Protocol, g, err)
			}
		}
	}
	web, err := s.GetQualityConfigV4(ctx, id, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range web.Lanes {
		if lane.Protocol == "icmp" && lane.Slot == "telecom" && lane.Status != protocol.QualityLaneICMPUnsupported {
			t.Fatalf("ICMP lane status %s", lane.Status)
		}
	}
	if got, err := s.GetQualitySessionConfig(ctx, id, r.epoch, r.sessionID, 2, true); err != nil || got.Version != 2 {
		t.Fatal(got, err)
	}
	t.Log("real TCP4/TCP6 loopback -> v2 HTTP -> SQLite, epoch=" + strconv.FormatUint(r.epoch, 10))
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker shutdown")
	}
}

// familyResolver answers each family with one fixed public address.
type familyResolver map[string]string

func (f familyResolver) LookupNetIP(_ context.Context, network, _ string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr(f[network])}, nil
}

// stagedGroup is the history of the staged samples of one target.
func stagedGroup(s *storage.Store, ctx context.Context, id, target string) (*storage.QualityHistoryGroup, error) {
	h, err := s.QualityHistoryV2(ctx, id, target, 1, time.Now().Add(70*time.Second))
	if err != nil {
		return nil, err
	}
	for i := range h.Groups {
		if h.Groups[i].Semantics == protocol.QualityStagedSemantics {
			return &h.Groups[i], nil
		}
	}
	return nil, nil
}
