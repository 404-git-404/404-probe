package agent

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func userTarget(slot, family, proto, host string, port int) protocol.QualityTarget {
	id := strings.Repeat("a", 31) + string(rune('0'+len(slot)%10))
	return protocol.QualityTarget{ID: id, Slot: slot, Family: family, SlotRevision: "1", Source: "user", Protocol: proto, Host: host, Port: port, Status: "active"}
}

func userConfig(targets ...protocol.QualityTarget) protocol.QualityConfig {
	c := protocol.QualityConfig{Version: 2, Revision: "1", Supported: true, Enabled: true, IPv6: true, IntervalMS: 30000, JitterPercent: 20, TimeoutMS: 5000, Semantics: protocol.QualityStagedSemantics, State: "ready", Targets: targets}
	c.Capabilities = &protocol.QualityCapabilities{Version: 2, Semantics: protocol.QualityStagedSemantics, Targets: protocol.QualityTargetsUserV1, IPv6Address: protocol.QualityIPv6Present}
	for _, f := range []string{"ipv4", "ipv6"} {
		for _, p := range []string{"tcp", "icmp"} {
			c.Capabilities.Lanes = append(c.Capabilities.Lanes, protocol.QualityLaneCapability{Family: f, Protocol: p, Supported: true})
		}
	}
	return c
}

// A negotiated session accepts user targets that satisfy the Server's host rules and nothing else; an
// unnegotiated one keeps the catalog path (design 4.2).
func TestQualityValidateUserTargets(t *testing.T) {
	ok := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	if err := qualityValidateConfigFor(ok, true); err != nil {
		t.Fatal(err)
	}
	if qualityValidateConfigFor(ok, false) == nil || qualityValidateConfig(ok) == nil {
		t.Fatal("a user target was accepted without the negotiation")
	}
	catalog := userConfig(func() protocol.QualityTarget {
		x := userTarget("telecom", "ipv4", "tcp", "example.com", 443)
		x.Source, x.Region = "catalog", "region-a"
		return x
	}())
	catalog.RegionID = "region-a"
	if qualityValidateConfigFor(catalog, true) == nil {
		t.Fatal("a catalog target was accepted in a negotiated session")
	}
	if err := qualityValidateConfigFor(catalog, false); err != nil {
		t.Fatalf("the catalog path changed: %v", err)
	}
	for name, mutate := range map[string]func(*protocol.QualityConfig){
		"upper case":        func(c *protocol.QualityConfig) { c.Targets[0].Host = "Example.com" },
		"trailing dot":      func(c *protocol.QualityConfig) { c.Targets[0].Host = "example.com." },
		"private literal":   func(c *protocol.QualityConfig) { c.Targets[0].Host = "10.0.0.1" },
		"loopback literal":  func(c *protocol.QualityConfig) { c.Targets[0].Host = "127.0.0.1" },
		"reserved suffix":   func(c *protocol.QualityConfig) { c.Targets[0].Host = "printer.local" },
		"hex number":        func(c *protocol.QualityConfig) { c.Targets[0].Host = "0x7f.1" },
		"empty host":        func(c *protocol.QualityConfig) { c.Targets[0].Host = "" },
		"v6 literal in v4":  func(c *protocol.QualityConfig) { c.Targets[0].Host = "2606:4700::1111" },
		"port zero":         func(c *protocol.QualityConfig) { c.Targets[0].Port = 0 },
		"port too large":    func(c *protocol.QualityConfig) { c.Targets[0].Port = 70000 },
		"carries a region":  func(c *protocol.QualityConfig) { c.Targets[0].Region = "x" },
		"carries a kind":    func(c *protocol.QualityConfig) { c.Targets[0].Kind = "custom" },
		"legacy semantics":  func(c *protocol.QualityConfig) { c.Semantics = protocol.QualityV2Semantics },
		"version 1":         func(c *protocol.QualityConfig) { c.Version = 1 },
		"disabled but live": func(c *protocol.QualityConfig) { c.Enabled = false },
	} {
		c := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
		mutate(&c)
		if qualityValidateConfigFor(c, true) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A paused target of an impossible family is not checked (the Server keeps all four lanes).
	paused := userTarget("telecom", "ipv6", "tcp", "93.184.216.34", 443)
	paused.Status = "paused"
	c := userConfig(userTarget("telecom", "ipv4", "tcp", "93.184.216.34", 443), paused)
	if err := qualityValidateConfigFor(c, true); err != nil {
		t.Fatal(err)
	}
	// ICMP carries no port; IPv6 literal stays in its family.
	if err := qualityValidateConfigFor(userConfig(userTarget("telecom", "ipv4", "icmp", "example.com", 0), userTarget("unicom", "ipv6", "tcp", "2606:4700::1111", 443)), true); err != nil {
		t.Fatal(err)
	}
	if qualityValidateConfigFor(userConfig(userTarget("telecom", "ipv4", "icmp", "example.com", 443)), true) == nil {
		t.Fatal("ICMP with a port accepted")
	}
}

type negotiationServer struct {
	mu           sync.Mutex
	userTargets  atomic.Bool
	rejectTarget atomic.Bool // answer 400 to a report that carries the targets capability
	inline       atomic.Pointer[protocol.QualityConfig]
	reports      []protocol.Report
	headers      []string
	pullHeaders  []string
	pullStatus   atomic.Int32
	pullBody     atomic.Pointer[protocol.QualityConfig]
}

func (n *negotiationServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/report":
			var report protocol.Report
			_ = json.NewDecoder(r.Body).Decode(&report)
			n.mu.Lock()
			n.reports = append(n.reports, report)
			n.headers = append(n.headers, r.Header.Get(protocol.QualityTargetsHeader))
			n.mu.Unlock()
			if report.NetworkQualityV2 != nil && report.NetworkQualityV2.Targets != "" && n.rejectTarget.Load() {
				w.WriteHeader(400)
				return
			}
			response := protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{NetworkQuality: true, NetworkQualityV2: true, NetworkQualityStaged: true, NetworkQualityUserTargets: n.userTargets.Load()}}
			response.NetworkQuality = n.inline.Load()
			_ = json.NewEncoder(w).Encode(response)
		case "/api/v1/agent/network-quality/config":
			n.mu.Lock()
			n.pullHeaders = append(n.pullHeaders, r.Header.Get(protocol.QualityTargetsHeader))
			n.mu.Unlock()
			if status := int(n.pullStatus.Load()); status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"unsupported","message":"current session unsupported"}}`))
				return
			}
			if c := n.pullBody.Load(); c != nil {
				_ = json.NewEncoder(w).Encode(c)
				return
			}
			w.WriteHeader(500)
		default:
			_ = json.NewEncoder(w).Encode(protocol.QualityBatchResponse{})
		}
	})
}

func (n *negotiationServer) report(i int) protocol.Report {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reports[i]
}

func TestQualityTwoStepNegotiationAndWithdrawal(t *testing.T) {
	n := &negotiationServer{}
	server := httptest.NewServer(n.handler())
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	r.quality.ipv6 = &qualityIPv6Monitor{scan: func() (bool, error) { return false, nil }}
	r.quality.nat64Resolver = fixedNAT64{}
	ctx := context.Background()
	var seq uint64
	send := func() {
		t.Helper()
		if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	// A Server that never heard of user targets: the header goes out, nothing else changes.
	send()
	send()
	if n.headers[0] != protocol.QualityTargetsUserV1 || n.headers[1] != protocol.QualityTargetsUserV1 {
		t.Fatalf("the targets header was not sent: %q", n.headers)
	}
	if caps := n.report(1).NetworkQualityV2; caps == nil || caps.Targets != "" || caps.IPv6Address != "" || r.qualityUserTargets.Load() {
		t.Fatalf("new fields sent to a Server that did not answer the header: %+v", caps)
	}
	// Step one answered: step two carries the targets capability and the IPv6 address state.
	n.userTargets.Store(true)
	send()
	if r.qualityUserTargets.Load() != true {
		t.Fatal("the answer to the header was not recorded")
	}
	send()
	caps := n.report(3).NetworkQualityV2
	if caps == nil || caps.Targets != protocol.QualityTargetsUserV1 || caps.IPv6Address != protocol.QualityIPv6Unknown || caps.Validate() != nil {
		t.Fatalf("step two capability: %+v", caps)
	}
	// The negotiated session installs a user configuration from the report response.
	config := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	n.inline.Store(&config)
	send()
	r.quality.mu.Lock()
	installed := r.quality.config != nil && r.quality.config.Targets[0].Source == "user"
	r.quality.mu.Unlock()
	if !installed {
		t.Fatal("the user configuration was not installed")
	}
	// The Server withdraws the capability: what was installed stops at once, and nothing user-entered
	// can be installed again.
	n.userTargets.Store(false)
	send()
	r.quality.mu.Lock()
	cleared := r.quality.config == nil && !r.quality.userTargets
	r.quality.mu.Unlock()
	if !cleared || r.qualityUserTargets.Load() {
		t.Fatalf("withdrawal left state behind: config=%v flag=%v", !cleared, r.qualityUserTargets.Load())
	}
	send()
	if caps := n.report(len(n.reports) - 1).NetworkQualityV2; caps == nil || caps.Targets != "" || caps.IPv6Address != "" {
		t.Fatalf("withdrawn session still sends the new fields: %+v", caps)
	}
}

func TestQualityRejectedTargetsCapabilityFallsBack(t *testing.T) {
	n := &negotiationServer{}
	n.userTargets.Store(true)
	server := httptest.NewServer(n.handler())
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	r.quality.ipv6 = &qualityIPv6Monitor{scan: func() (bool, error) { return false, nil }}
	r.quality.nat64Resolver = fixedNAT64{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seq uint64
	for i := 0; i < 2; i++ {
		if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r.quality.probe = func(ctx context.Context, _ protocol.QualityTarget) qualityProbeResult {
		once.Do(func() { close(started) })
		<-ctx.Done()
		select {
		case <-canceled:
		default:
			close(canceled)
		}
		return qualityProbeResult{outcome: "canceled"}
	}
	r.quality.random = func() float64 { return 0 }
	var advance atomic.Int64
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	scheduled := make(chan struct{}, 1)
	r.quality.scheduleObserved = func(_ uint64, n int) {
		if n == 1 {
			select {
			case scheduled <- struct{}{}:
			default:
			}
		}
	}
	config := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	n.inline.Store(&config)
	if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
		t.Fatal(ok, err)
	}
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	<-scheduled
	advance.Store(int64(31 * time.Second))
	r.quality.signal()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the probe never started")
	}
	// An old Server (or a proxy) now answers 400 to the report that carries the new capability.
	n.rejectTarget.Store(true)
	n.userTargets.Store(false) // a Server that rejects the field does not advertise it either
	n.inline.Store(nil)
	if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
		t.Fatal(ok, err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("the probe in flight was not cancelled")
	}
	r.quality.mu.Lock()
	cleared := r.quality.config == nil && !r.quality.userTargets
	r.quality.mu.Unlock()
	if !cleared || r.qualityUserTargets.Load() {
		t.Fatal("the fallback left the negotiation behind")
	}
	last := n.report(len(n.reports) - 1)
	if last.NetworkQualityV2 != nil {
		t.Fatal("the retry still carried the rejected capability")
	}
	cancel()
	<-done
}

// Withdrawal also stops a running worker: no lane, no probe (design 4.2, 4.5).
func TestQualityWithdrawalCancelsInFlightProbeAndDropsQueue(t *testing.T) {
	n := &negotiationServer{}
	n.userTargets.Store(true)
	server := httptest.NewServer(n.handler())
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	r.quality.ipv6 = &qualityIPv6Monitor{scan: func() (bool, error) { return false, nil }}
	r.quality.nat64Resolver = fixedNAT64{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seq uint64
	for i := 0; i < 2; i++ {
		if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	var once, once2 sync.Once
	r.quality.probe = func(ctx context.Context, _ protocol.QualityTarget) qualityProbeResult {
		once.Do(func() { close(started) })
		<-ctx.Done()
		once2.Do(func() { close(canceled) })
		return qualityProbeResult{outcome: "canceled"}
	}
	var advance atomic.Int64
	r.quality.now = func() time.Time { return time.Now().Add(time.Duration(advance.Load())) }
	r.quality.random = func() float64 { return 0 }
	lanes := make(chan int, 16)
	r.quality.scheduleObserved = func(_ uint64, n int) {
		select {
		case lanes <- n:
		default:
		}
	}
	config := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	n.inline.Store(&config)
	if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
		t.Fatal(ok, err)
	}
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	waitForAgentCondition(t, "lane scheduled", func() bool {
		select {
		case count := <-lanes:
			return count == 1
		default:
			return false
		}
	})
	advance.Store(int64(31 * time.Second))
	r.quality.signal()
	<-started
	n.userTargets.Store(false)
	n.inline.Store(nil)
	if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
		t.Fatal(ok, err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("probe not cancelled")
	}
	waitForAgentCondition(t, "no lane scheduled", func() bool {
		for {
			select {
			case count := <-lanes:
				if count == 0 {
					return true
				}
			default:
				return false
			}
		}
	})
	cancel()
	<-done
}

// S3 (R07-B review): a pull answered 409 "unsupported" must never leave the installed user
// configuration running.
func TestQualityPullUnsupportedDropsInstalledUserConfig(t *testing.T) {
	n := &negotiationServer{}
	n.userTargets.Store(true)
	server := httptest.NewServer(n.handler())
	defer server.Close()
	r := n2bRunner(t, server.URL, "id", "token")
	r.quality.ipv6 = &qualityIPv6Monitor{scan: func() (bool, error) { return false, nil }}
	r.quality.nat64Resolver = fixedNAT64{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seq uint64
	for i := 0; i < 2; i++ {
		if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	config := userConfig(userTarget("telecom", "ipv4", "tcp", "example.com", 443))
	n.inline.Store(&config)
	if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
		t.Fatal(ok, err)
	}
	n.pullStatus.Store(409)
	n.inline.Store(nil)
	// A later report with no inline configuration makes the Agent ask for it.
	r.quality.mu.Lock()
	r.quality.fetch = true
	r.quality.mu.Unlock()
	var probes atomic.Int32
	r.quality.probe = func(context.Context, protocol.QualityTarget) qualityProbeResult {
		probes.Add(1)
		return qualityProbeResult{outcome: "canceled"}
	}
	done := make(chan struct{})
	go func() { r.runQualityWorker(ctx); close(done) }()
	waitForAgentCondition(t, "pull answered", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return len(n.pullHeaders) > 0
	})
	waitForAgentCondition(t, "config dropped", func() bool {
		r.quality.mu.Lock()
		defer r.quality.mu.Unlock()
		return r.quality.config == nil && !r.quality.allowed
	})
	if n.pullHeaders[0] != protocol.QualityTargetsUserV1 {
		t.Fatal("a negotiated pull must carry the header")
	}
	cancel()
	<-done
	if probes.Load() != 0 {
		t.Fatal("a probe ran after the Server said unsupported")
	}
}

func TestQualityIPv6MonitorStates(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	scans := make(chan struct {
		present bool
		err     error
	}, 8)
	m := &qualityIPv6Monitor{scan: func() (bool, error) {
		s := <-scans
		return s.present, s.err
	}}
	push := func(present bool, err error) {
		scans <- struct {
			present bool
			err     error
		}{present, err}
	}
	at := func(d time.Duration) time.Time { return start.Add(d) }
	expect := func(want string, d time.Duration, present bool, err error) {
		t.Helper()
		push(present, err)
		if got := m.observe(at(d)); got != want {
			t.Fatalf("at %v present=%v err=%v: got %s want %s", d, present, err, got, want)
		}
	}
	// Not confirmed in the first five minutes, then none.
	expect("unknown", 0, false, nil)
	expect("unknown", 4*time.Minute+59*time.Second, false, nil)
	expect("none", 5*time.Minute, false, nil)
	// A scan error is unknown and the observation period starts over.
	expect("unknown", 6*time.Minute, false, errors.New("scan"))
	expect("unknown", 7*time.Minute, false, nil)
	expect("unknown", 11*time.Minute+59*time.Second, false, nil)
	expect("none", 12*time.Minute, false, nil)
	// An address appearing is reported at once; after that "none" is never reported again.
	expect("present", 13*time.Minute, true, nil)
	expect("unknown", 14*time.Minute, false, nil)
	expect("unknown", 40*time.Minute, false, nil)
	expect("present", 41*time.Minute, true, nil)
}

func TestQualityIPv6ScanReadsProcNetIfInet6(t *testing.T) {
	restore := []any{qualityIfInet6Path, qualityReadFile, qualityInterfaceAt, qualityScanGOOS}
	defer func() {
		qualityIfInet6Path, qualityReadFile = restore[0].(string), restore[1].(func(string) ([]byte, error))
		qualityInterfaceAt, qualityScanGOOS = restore[2].(func(int) (*net.Interface, error)), restore[3].(string)
	}()
	qualityScanGOOS = "linux"
	qualityInterfaceAt = func(i int) (*net.Interface, error) {
		switch i {
		case 1:
			return &net.Interface{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback}, nil
		case 2:
			return &net.Interface{Index: 2, Name: "eth0", Flags: net.FlagUp}, nil
		case 3:
			return &net.Interface{Index: 3, Name: "eth1", Flags: 0}, nil
		}
		return nil, errors.New("no such interface")
	}
	cases := []struct {
		name, file string
		want       bool
		fail       bool
	}{
		{"no file means IPv6 is disabled", "", false, false},
		{"loopback only", "00000000000000000000000000000001 01 80 10 80 lo\n", false, false},
		{"link local only", "fe800000000000000000000000000001 02 40 20 80 eth0\n", false, false},
		{"global on an up interface", "26064700000000000000000000001111 02 40 00 80 eth0\n", true, false},
		{"unique local counts", "fd000000000000000000000000000001 02 40 00 80 eth0\n", true, false},
		{"tentative is excluded", "26064700000000000000000000001111 02 40 00 c0 eth0\n", false, false},
		{"duplicate address failed is excluded", "26064700000000000000000000001111 02 40 00 88 eth0\n", false, false},
		{"interface down", "26064700000000000000000000001111 03 40 00 80 eth1\n", false, false},
		{"loopback interface with a global address", "26064700000000000000000000001111 01 40 00 80 lo\n", false, false},
		{"second line wins", "fe800000000000000000000000000001 02 40 20 80 eth0\n26064700000000000000000000001111 02 40 00 80 eth0\n", true, false},
		{"garbage is an error, not none", "zz064700000000000000000000001111 02 40 00 80 eth0\n", false, true},
	}
	for _, tc := range cases {
		if tc.file == "" {
			qualityReadFile = func(string) ([]byte, error) {
				return nil, &os.PathError{Op: "open", Path: "if_inet6", Err: os.ErrNotExist}
			}
		} else {
			wire := tc.file
			qualityReadFile = func(string) ([]byte, error) { return []byte(wire), nil }
		}
		got, err := scanUsableIPv6()
		if (err != nil) != tc.fail || got != tc.want {
			t.Errorf("%s: got %v err=%v", tc.name, got, err)
		}
	}
	qualityReadFile = func(string) ([]byte, error) { return nil, errors.New("permission denied") }
	if _, err := scanUsableIPv6(); err == nil {
		t.Error("an unreadable file must be an error (unknown), not none")
	}
}
