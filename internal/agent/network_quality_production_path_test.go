package agent

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// These tests go through the constructors production uses (NewProbeExecutor, which is what the quality
// worker takes its executors from) and only swap in a counter where a real socket would otherwise be
// touched. They exist because the guarded dialer was once wired but never reached: a test that injects
// its own dialer proves nothing about the production path.

type countingICMPBackend struct{ opens atomic.Int32 }

func (b *countingICMPBackend) Supports(icmpFamily) bool { return true }
func (b *countingICMPBackend) Open(icmpFamily) (icmpPacketConn, bool, error) {
	b.opens.Add(1)
	return nil, false, net.ErrClosed
}

type internalResolver struct{ ip netip.Addr }

func (r internalResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{r.ip}, nil
}

// refusedAddresses are never to be reached by a quality probe, as literals or behind a hostname.
var refusedAddresses = []string{
	"127.0.0.1", "10.1.2.3", "172.16.5.4", "192.168.1.1", "169.254.169.254", "100.64.0.1", "198.51.100.7", "203.0.113.9",
	"0.0.0.0", "::1", "fc00::1", "fe80::1", "2001:db8::1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
}

func qualityTarget(host, family string) protocol.QualityTarget {
	return protocol.QualityTarget{Host: host, Port: 80, Family: family, Protocol: "tcp", Source: "catalog"}
}

func familyOf(address string) string {
	if ip, err := netip.ParseAddr(address); err == nil && ip.Unmap().Is6() {
		return "ipv6"
	}
	return "ipv4"
}

func productionExecutors(t *testing.T) (*TCPExecutor, *ICMPExecutor) {
	t.Helper()
	useRealQualityPolicy(t)
	executor := NewProbeExecutor()
	tcp, ok := executor.tcp.(*TCPExecutor)
	if !ok {
		t.Fatalf("production TCP executor is %T", executor.tcp)
	}
	icmp, ok := executor.icmp.(*ICMPExecutor)
	if !ok {
		t.Fatalf("production ICMP executor is %T", executor.icmp)
	}
	return tcp, icmp
}

// The quality probes must not use the dialer that TCP-connect jobs use.
func TestQualityProbesNeverUseTheJobDialer(t *testing.T) {
	tcp, _ := productionExecutors(t)
	var jobDials atomic.Int32
	tcp.dialContext = func(context.Context, string, string) (net.Conn, error) {
		jobDials.Add(1)
		return nil, net.ErrClosed
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	target := qualityTarget("127.0.0.1", "ipv4")
	target.Port = port
	for _, probe := range []func(context.Context, protocol.QualityTarget) qualityProbeResult{tcp.qualityProbe, tcp.qualityStagedProbe} {
		if result := probe(context.Background(), target); result.outcome != "unsafe_address" {
			t.Fatalf("outcome=%q", result.outcome)
		}
	}
	if jobDials.Load() != 0 {
		t.Fatalf("a quality probe used the job dialer %d times", jobDials.Load())
	}
}

// Legacy TCP quality probe: literals are refused up front, and a hostname that resolves to loopback is
// refused by the production dialer at connect time, so the listener never sees a connection.
func TestProductionLegacyTCPQualityProbeRefusesNonPublicTargets(t *testing.T) {
	tcp, _ := productionExecutors(t)
	var accepted atomic.Int32
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			connection.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, address := range refusedAddresses {
		target := qualityTarget(address, familyOf(address))
		if result := tcp.qualityProbe(ctx, target); result.outcome != "unsafe_address" || result.latency != nil || result.ip != "" {
			t.Errorf("legacy literal %s: %+v", address, result)
		}
	}
	// A hostname: the system resolver turns "localhost" into 127.0.0.1; only the dialer's own check can stop it.
	port := listener.Addr().(*net.TCPAddr).Port
	hostname := qualityTarget("localhost", "ipv4")
	hostname.Port = port
	result := tcp.qualityProbe(ctx, hostname)
	if result.outcome != "unsafe_address" || result.latency != nil {
		t.Fatalf("hostname resolving to loopback: %+v", result)
	}
	time.Sleep(100 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Fatalf("the listener saw %d connection(s)", accepted.Load())
	}
}

// Staged TCP quality probe: the resolved address is checked before anything is dialed.
func TestProductionStagedTCPQualityProbeRefusesNonPublicTargets(t *testing.T) {
	tcp, _ := productionExecutors(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, address := range refusedAddresses {
		ip := netip.MustParseAddr(address)
		family := familyOf(address)
		// Literal target and a hostname that resolves to the address.
		for name, setup := range map[string]func() protocol.QualityTarget{
			"literal": func() protocol.QualityTarget { tcp.qualityResolver = nil; return qualityTarget(address, family) },
			"hostname": func() protocol.QualityTarget {
				tcp.qualityResolver = internalResolver{ip}
				return qualityTarget("catalog-host.example", family)
			},
		} {
			result := tcp.qualityStagedProbe(ctx, setup())
			if result.outcome != "unsafe_address" || result.latency != nil {
				t.Errorf("staged %s %s: %+v", name, address, result)
			}
		}
	}
}

// ICMP quality probes (legacy and staged) check the resolved address before a socket is even opened.
func TestProductionICMPQualityProbesRefuseNonPublicTargets(t *testing.T) {
	_, icmp := productionExecutors(t)
	backend := &countingICMPBackend{}
	icmp.backend = backend
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, address := range refusedAddresses {
		ip := netip.MustParseAddr(address)
		family := familyOf(address)
		literal := qualityTarget(address, family)
		literal.Protocol = "icmp"
		hostname := qualityTarget("catalog-host.example", family)
		hostname.Protocol = "icmp"
		icmp.resolver = internalResolver{ip}
		for name, probe := range map[string]func(context.Context, protocol.QualityTarget) qualityProbeResult{"legacy": icmp.qualityProbe, "staged": icmp.qualityStagedProbe} {
			for kind, target := range map[string]protocol.QualityTarget{"literal": literal, "hostname": hostname} {
				if result := probe(ctx, target); result.outcome != "unsafe_address" {
					t.Errorf("ICMP %s %s %s: %+v", name, kind, address, result)
				}
			}
		}
	}
	if backend.opens.Load() != 0 {
		t.Fatalf("an ICMP socket was opened %d time(s) for a refused target", backend.opens.Load())
	}
}

// The public IP check dials through its own guarded transport; it must refuse the same ranges as built by
// the production constructor (egress.New is what NewPublicEgressExecutor wraps).
func TestProductionPublicEgressExecutorIsConstructedWithTheGuard(t *testing.T) {
	executor := NewProbeExecutor()
	if executor.egress == nil {
		t.Fatal("the production executor has no public egress executor")
	}
}

// NAT64: the well-known prefix is judged by the IPv4 address inside it, in the policy, in the production
// dialer's connect-time hook and in the staged probe built by the production constructor.
func TestQualityGuardJudgesNAT64ByTheEmbeddedIPv4(t *testing.T) {
	tcp, _ := productionExecutors(t)
	for address, allowed := range map[string]bool{
		"64:ff9b::808:808": true, "64:ff9b::101:101": true,
		"64:ff9b::a00:1": false, "64:ff9b::7f00:1": false, "64:ff9b::a9fe:a9fe": false, "64:ff9b::c0a8:101": false,
		"64:ff9b:1::808:808": false, "64:ff9b::1:808:808": false, "2002:808:808::1": false,
	} {
		ip := netip.MustParseAddr(address)
		if got := qualityAddressPolicy(ip); got != allowed {
			t.Errorf("policy(%s)=%v want %v", address, got, allowed)
		}
		err := qualityGuardControl("tcp6", net.JoinHostPort(address, "80"), nil)
		if (err == nil) != allowed {
			t.Errorf("connect-time hook(%s) err=%v, allowed=%v", address, err, allowed)
		}
		if !allowed {
			tcp.qualityResolver = internalResolver{ip}
			if result := tcp.qualityStagedProbe(context.Background(), qualityTarget("catalog-host.example", "ipv6")); result.outcome != "unsafe_address" {
				t.Errorf("staged probe to %s: %+v", address, result)
			}
		}
	}
}
