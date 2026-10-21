package agent

import (
	"404-probe/internal/protocol"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

type scriptResolver struct {
	answers []netip.Addr
	err     error
	asked   []string
}

func (r *scriptResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	r.asked = append(r.asked, network+"/"+host)
	return r.answers, r.err
}

func userProbeTarget(family, proto, host string) protocol.QualityTarget {
	port := 443
	if proto == "icmp" {
		port = 0
	}
	return protocol.QualityTarget{ID: "0123456789abcdef0123456789abcdef", Slot: "telecom", Family: family, SlotRevision: "1", Source: "user", Protocol: proto, Host: host, Port: port, Status: "active"}
}

func decodeStaged(t *testing.T, result qualityProbeResult) *protocol.QualityDetails {
	t.Helper()
	d, err := protocol.DecodeQualityDetails(result.details)
	if err != nil || d == nil {
		t.Fatal(err, string(result.details))
	}
	return d
}

// Address diagnostic events (design 7.3): not_found for a name that does not exist or has no record
// of the family, no_address for an answer without the family, unsafe_address for a non-public answer.
// Each result is a valid staged sample, so the Server accepts it.
func TestAddressDiagnosticEventsAreValidSamples(t *testing.T) {
	useRealQualityPolicy(t)
	setNAT64(t)
	notFound := &net.DNSError{Err: "no such host", Name: "gone.example.com", IsNotFound: true}
	type probe func(resolver icmpResolver, target protocol.QualityTarget) qualityProbeResult
	tcp := func(resolver icmpResolver, target protocol.QualityTarget) qualityProbeResult {
		return (&TCPExecutor{qualityResolver: resolver, qualityDial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("a connection was attempted")
			return nil, nil
		}}).qualityStagedProbe(context.Background(), target)
	}
	icmpProbe := func(resolver icmpResolver, target protocol.QualityTarget) qualityProbeResult {
		return (&ICMPExecutor{backend: fakeICMPBackend{v4: true, v6: true}, resolver: resolver}).qualityStagedProbe(context.Background(), target)
	}
	for name, run := range map[string]probe{"tcp": tcp, "icmp": icmpProbe} {
		proto := name
		t.Run(proto, func(t *testing.T) {
			cases := []struct {
				what             string
				resolver         *scriptResolver
				source           string
				outcome, dns, ec string
			}{
				{"name does not exist", &scriptResolver{err: notFound}, "user", "dns_error", "not_found", "not_found"},
				{"a catalog target keeps dns_error", &scriptResolver{err: notFound}, "catalog", "dns_error", "dns_error", "dns_error"},
				{"no record of the family", &scriptResolver{answers: []netip.Addr{}}, "user", "dns_error", "no_address", "no_address"},
				{"only the other family", &scriptResolver{answers: []netip.Addr{netip.MustParseAddr("2606:4700::1111")}}, "user", "dns_error", "no_address", "no_address"},
				{"server failure is not an address event", &scriptResolver{err: errors.New("SERVFAIL")}, "user", "dns_error", "dns_error", "dns_error"},
				{"private answer", &scriptResolver{answers: []netip.Addr{netip.MustParseAddr("10.1.2.3")}}, "user", "unsafe_address", "success", "unsafe_address"},
			}
			for _, tc := range cases {
				target := userProbeTarget("ipv4", proto, "gone.example.com")
				target.Source = tc.source
				result := run(tc.resolver, target)
				d := decodeStaged(t, result)
				if result.outcome != tc.outcome || d.DNSOutcome != tc.dns || d.ErrorCode != tc.ec {
					t.Errorf("%s: outcome=%s dns=%s code=%s", tc.what, result.outcome, d.DNSOutcome, d.ErrorCode)
				}
				now := time.Now()
				sample := protocol.QualitySample{Sequence: "1", TargetID: target.ID, ConfigRevision: "1", SlotRevision: "1", ScheduledAt: now.UnixMilli(), StartedAt: now.UnixMilli(), FinishedAt: now.UnixMilli() + 20, DurationMS: 20, Outcome: result.outcome, ResolvedIP: result.ip, Sent: result.sent, Received: result.received, Details: result.details}
				if reason := sample.ValidateDetails(now.Add(time.Second), target); reason != "" {
					// This is the Server's check; every diagnostic event must pass it.
					t.Errorf("%s: the Server would refuse this sample: %s", tc.what, reason)
				}
			}
		})
	}
	// The question is asked per family, every time.
	resolver := &scriptResolver{err: notFound}
	run := tcp
	run(resolver, userProbeTarget("ipv6", "tcp", "gone.example.com"))
	run(resolver, userProbeTarget("ipv6", "tcp", "gone.example.com"))
	if len(resolver.asked) != 2 || resolver.asked[0] != "ip6/gone.example.com" {
		t.Fatalf("%v", resolver.asked)
	}
}

// Resolving to a public first address and a private second one connects to the first only; resolving
// to a private first address never looks further (design 5.3).
func TestResolutionOrderNeverFallsThroughToASaferAddress(t *testing.T) {
	useRealQualityPolicy(t)
	setNAT64(t)
	var dialed []string
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, errors.New("refused")
	}
	run := func(answers ...string) qualityProbeResult {
		dialed = nil
		var ips []netip.Addr
		for _, a := range answers {
			ips = append(ips, netip.MustParseAddr(a))
		}
		return (&TCPExecutor{qualityResolver: &scriptResolver{answers: ips}, qualityDial: dial}).qualityStagedProbe(context.Background(), userProbeTarget("ipv4", "tcp", "mixed.example.com"))
	}
	if r := run("10.0.0.1", "93.184.216.34"); r.outcome != "unsafe_address" || len(dialed) != 0 {
		t.Fatalf("private first: %s dialed=%v", r.outcome, dialed)
	}
	if r := run("93.184.216.34", "10.0.0.1"); len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("public first: %s dialed=%v", r.outcome, dialed)
	}
}

// The default dialer refuses a private address before connecting, even if a resolver slipped one past
// the earlier check (DNS rebinding between resolution and dial).
func TestConnectionGuardRefusesPrivateAddressAtDialTime(t *testing.T) {
	useRealQualityPolicy(t)
	setNAT64(t)
	for _, address := range []string{"127.0.0.1:80", "10.0.0.1:80", "[fd00::1]:80", "[2001:db8::1]:80"} {
		if err := qualityGuardControl("tcp", address, nil); !errors.Is(err, errUnsafeQualityAddress) {
			t.Errorf("%s: %v", address, err)
		}
	}
	setNAT64(t, "2606:4700:100::/96")
	embeddedPrivate := nat64Compose(netip.MustParsePrefix("2606:4700:100::/96"), netip.MustParseAddr("10.1.2.3"))
	if err := qualityGuardControl("tcp", net.JoinHostPort(embeddedPrivate.String(), "80"), nil); !errors.Is(err, errUnsafeQualityAddress) {
		t.Fatalf("a translated private address reached the dialer: %v", err)
	}
	if err := qualityGuardControl("tcp", "93.184.216.34:80", nil); err != nil {
		t.Fatal(err)
	}
}
