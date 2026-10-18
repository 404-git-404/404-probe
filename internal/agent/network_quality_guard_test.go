package agent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// useRealQualityPolicy runs a test with the production address policy instead of the permissive one
// the rest of the package installs for its loopback fixtures.
func useRealQualityPolicy(t *testing.T) {
	t.Helper()
	previous := qualityAddressPolicy
	qualityAddressPolicy = qualityAddressSafe
	t.Cleanup(func() { qualityAddressPolicy = previous })
}

type fixedResolver struct{ ip netip.Addr }

func (r fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{r.ip}, nil
}

var unsafeQualityAddresses = []string{
	"127.0.0.1", "127.8.8.8", "10.0.0.5", "10.255.255.255", "172.16.0.1", "172.31.255.1", "192.168.1.1",
	"169.254.169.254", "169.254.0.1", "100.64.0.1", "100.127.255.1", "192.0.2.1", "198.51.100.7", "203.0.113.9",
	"198.18.0.1", "240.0.0.1", "::1", "fc00::1", "fd12:3456::1", "fe80::1", "2001:db8::1", "2002::1",
}

func TestQualityAddressPolicyUsesThePublicEgressDenyList(t *testing.T) {
	for _, text := range append(append([]string{}, unsafeQualityAddresses...), "0.0.0.0", "224.0.0.1", "255.255.255.255", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::", "ff02::1", "fec0::1") {
		if qualityAddressSafe(netip.MustParseAddr(text)) {
			t.Errorf("%s was considered safe", text)
		}
	}
	for _, text := range []string{"8.8.8.8", "1.1.1.1", "114.114.114.114", "223.5.5.5", "2606:4700:4700::1111", "2400:3200::1"} {
		if !qualityAddressSafe(netip.MustParseAddr(text)) {
			t.Errorf("%s was considered unsafe", text)
		}
	}
	// The guard and the egress validation cannot drift apart: same function, same answers.
	for _, text := range append(append([]string{}, unsafeQualityAddresses...), "8.8.8.8", "2606:4700:4700::1111") {
		ip := netip.MustParseAddr(text).Unmap()
		family := "ipv4"
		if ip.Is6() {
			family = "ipv6"
		}
		if qualityAddressSafe(netip.MustParseAddr(text)) != protocol.PublicEgressIP(ip.String(), family) {
			t.Errorf("%s: policy differs from protocol.PublicEgressIP", text)
		}
	}
}

func TestQualityTCPNeverDialsAnUnsafeAddress(t *testing.T) {
	useRealQualityPolicy(t)
	for _, text := range unsafeQualityAddresses {
		ip := netip.MustParseAddr(text)
		family := "ipv4"
		if ip.Is6() {
			family = "ipv6"
		}
		var dials atomic.Int32
		dial := func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("must not be called")
		}
		// A literal address in the target, staged and legacy.
		for _, staged := range []bool{true, false} {
			e := &TCPExecutor{dialContext: dial, qualityResolver: fixedResolver{ip}}
			target := protocol.QualityTarget{Host: text, Port: 443, Family: family, Protocol: "tcp"}
			var result qualityProbeResult
			if staged {
				result = e.qualityStagedProbe(context.Background(), target)
			} else {
				result = e.qualityProbe(context.Background(), target)
			}
			if result.outcome != "unsafe_address" || result.latency != nil || dials.Load() != 0 {
				t.Errorf("%s staged=%t: outcome=%q dials=%d", text, staged, result.outcome, dials.Load())
			}
			if result.ip != "" {
				t.Errorf("%s staged=%t: the unsafe address was reported back: %q", text, staged, result.ip)
			}
		}
		// A public-looking name whose DNS answer is the unsafe address (rebinding).
		e := &TCPExecutor{dialContext: dial, qualityResolver: fixedResolver{ip}}
		result := e.qualityStagedProbe(context.Background(), protocol.QualityTarget{Host: "catalog-host.example", Port: 443, Family: family, Protocol: "tcp"})
		if result.outcome != "unsafe_address" || dials.Load() != 0 {
			t.Errorf("%s via DNS: outcome=%q dials=%d", text, result.outcome, dials.Load())
		}
		details, err := protocol.DecodeQualityDetails(result.details)
		if err != nil || details == nil || details.ErrorStage != "local" || details.ErrorCode != "unsafe_address" || details.ConnectAttempted {
			t.Errorf("%s: details %+v err=%v", text, details, err)
		}
	}
}

type fakeRemoteConn struct {
	net.Conn
	remote net.Addr
}

func (c fakeRemoteConn) RemoteAddr() net.Addr { return c.remote }
func (c fakeRemoteConn) Close() error         { return nil }

func TestQualityTCPStillProbesAPublicTarget(t *testing.T) {
	useRealQualityPolicy(t)
	var dials atomic.Int32
	var dialed atomic.Value
	client, server := net.Pipe()
	defer server.Close()
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dials.Add(1)
		dialed.Store(address)
		return fakeRemoteConn{Conn: client, remote: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443}}, nil
	}
	e := &TCPExecutor{dialContext: dial, qualityResolver: fixedResolver{netip.MustParseAddr("8.8.8.8")}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := e.qualityStagedProbe(ctx, protocol.QualityTarget{Host: "catalog-host.example", Port: 443, Family: "ipv4", Protocol: "tcp"})
	if result.outcome != "success" || result.ip != "8.8.8.8" || dials.Load() != 1 || dialed.Load() != "8.8.8.8:443" {
		t.Fatalf("result=%+v dials=%d dialed=%v", result, dials.Load(), dialed.Load())
	}
	legacy := (&TCPExecutor{dialContext: dial}).qualityProbe(ctx, protocol.QualityTarget{Host: "8.8.8.8", Port: 443, Family: "ipv4", Protocol: "tcp"})
	if legacy.outcome != "success" || dials.Load() != 2 {
		t.Fatalf("legacy result=%+v dials=%d", legacy, dials.Load())
	}
}

// The default production dialer checks the resolved address before any socket connects, so even a
// hostname that resolves to loopback never reaches the network.
func TestQualityDefaultDialerRefusesUnsafeAddresses(t *testing.T) {
	useRealQualityPolicy(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	for _, address := range []string{listener.Addr().String(), "localhost:" + port} {
		if _, err := qualityDefaultDial()(context.Background(), "tcp4", address); !errors.Is(err, errUnsafeQualityAddress) {
			t.Errorf("%s: err=%v", address, err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Fatalf("%d connections reached the loopback listener", accepted.Load())
	}
}

func TestQualityICMPNeverOpensASocketForAnUnsafeAddress(t *testing.T) {
	useRealQualityPolicy(t)
	for _, text := range unsafeQualityAddresses {
		ip := netip.MustParseAddr(text)
		family, v4, v6 := "ipv4", true, false
		if ip.Is6() {
			family, v4, v6 = "ipv6", false, true
		}
		var opened atomic.Int32
		backend := fakeICMPBackend{v4: v4, v6: v6, open: func(icmpFamily) (icmpPacketConn, bool, error) {
			opened.Add(1)
			return nil, false, errors.New("must not open")
		}}
		for _, staged := range []bool{true, false} {
			e := &ICMPExecutor{backend: backend, resolver: fixedResolver{ip}}
			target := protocol.QualityTarget{Host: text, Family: family, Protocol: "icmp"}
			var result qualityProbeResult
			if staged {
				result = e.qualityStagedProbe(context.Background(), target)
			} else {
				result = e.qualityProbe(context.Background(), target)
			}
			if result.outcome != "unsafe_address" || result.ip != "" || opened.Load() != 0 {
				t.Errorf("%s staged=%t: outcome=%q ip=%q opened=%d", text, staged, result.outcome, result.ip, opened.Load())
			}
		}
	}
}

func TestQualitySampleWithUnsafeAddressOutcomeIsAcceptedByTheServerProtocol(t *testing.T) {
	useRealQualityPolicy(t)
	target := protocol.QualityTarget{Host: "127.0.0.1", Port: 443, Family: "ipv4", Protocol: "tcp", Slot: "telecom"}
	result := (&TCPExecutor{qualityResolver: fixedResolver{netip.MustParseAddr("127.0.0.1")}}).qualityStagedProbe(context.Background(), target)
	if result.outcome != "unsafe_address" || len(result.details) == 0 {
		t.Fatalf("result=%+v", result)
	}
	now := time.Now()
	sample := protocol.QualitySample{Sequence: "1", ConfigRevision: "1", SlotRevision: "1", TargetID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Outcome: result.outcome, Details: result.details, StartedAt: now.UnixMilli(), FinishedAt: now.UnixMilli(), ScheduledAt: now.UnixMilli()}
	if reason := sample.Validate(now, target); reason != "" {
		t.Fatalf("a TCP unsafe_address sample was rejected as %q", reason)
	}
	if sample.Attempt() {
		t.Fatal("a refused probe counts as a measurement attempt")
	}
}

func TestQualityICMPUnsafeSampleIsAcceptedByTheServerProtocol(t *testing.T) {
	useRealQualityPolicy(t)
	target := protocol.QualityTarget{Host: "10.1.2.3", Family: "ipv4", Protocol: "icmp", Slot: "telecom"}
	backend := fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) { return nil, false, errors.New("must not open") }}
	result := (&ICMPExecutor{backend: backend}).qualityStagedProbe(context.Background(), target)
	if result.outcome != "unsafe_address" {
		t.Fatalf("result=%+v", result)
	}
	now := time.Now()
	sample := protocol.QualitySample{Sequence: "1", ConfigRevision: "1", SlotRevision: "1", TargetID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Outcome: result.outcome, Details: result.details, StartedAt: now.UnixMilli(), FinishedAt: now.UnixMilli(), ScheduledAt: now.UnixMilli()}
	if reason := sample.Validate(now, target); reason != "" {
		t.Fatalf("an ICMP unsafe_address sample was rejected as %q", reason)
	}
	if sample.Attempt() {
		t.Fatal("a refused probe counts as a measurement attempt")
	}
}
