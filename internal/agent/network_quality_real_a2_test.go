package agent

import (
	"404-probe/internal/protocol"
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// Opt-in uses a construction-owned disposable Linux network namespace.
func TestA2RealControlledSockets(t *testing.T) {
	if os.Getenv("PROBE_A2_REAL_SOCKET") != "1" {
		t.Skip("requires disposable controlled Linux netns responder")
	}
	e := NewICMPExecutor()
	for _, tc := range []struct {
		ip       string
		received int
	}{{"127.0.0.2", 3}, {"127.0.0.3", 2}, {"127.0.0.4", 0}} {
		t.Run(tc.ip, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			target := protocol.QualityTarget{ID: "a2-real", Host: tc.ip, Family: "ipv4", Protocol: "icmp"}
			start := time.Now()
			r := e.qualityStagedProbe(ctx, target)
			finish := time.Now()
			d, err := protocol.DecodeQualityDetails(r.details)
			if err != nil || r.sent != 3 || r.received != tc.received || !d.WindowComplete {
				t.Fatal(r, d, err)
			}
			s := protocol.QualitySample{Sequence: "1", ConfigRevision: "1", SlotRevision: "1", TargetID: target.ID, ScheduledAt: start.UnixMilli(), StartedAt: start.UnixMilli(), FinishedAt: finish.UnixMilli(), DurationMS: qualityMS(finish.Sub(start)), Outcome: r.outcome, ResolvedIP: r.ip, Sent: r.sent, Received: r.received, LatencyMS: r.latency, Details: r.details}
			if reason := s.Validate(finish, target); reason != "" {
				t.Fatal(reason, string(r.details))
			}
			t.Logf("actual raw Socket %s sent%d received%d elapsed%v details=%s", tc.ip, r.sent, r.received, finish.Sub(start), r.details)
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	r := e.qualityStagedProbe(ctx, protocol.QualityTarget{Host: "127.0.0.4", Protocol: "icmp", Family: "ipv4"})
	cancel()
	d, _ := protocol.DecodeQualityDetails(r.details)
	if d.WindowComplete || d.ProbeComplete || r.sent != 2 || r.received != 0 {
		t.Fatal("real cancellation fabricated loss", r, d)
	}
	t.Logf("actual cancellation sent%d observed replies%d no loss denominator details=%s", r.sent, r.received, r.details)
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp4", "127.0.0.1:5353")
	}}
	tcp := NewTCPExecutor()
	tcp.qualityResolver = resolver
	for _, tc := range []struct {
		host    string
		port    int
		outcome string
	}{{"a2.test", 9450, "success"}, {"127.0.0.1", 9451, "refused"}, {"127.0.0.1", 9452, "timeout"}, {"missing.test", 9450, "dns_error"}} {
		t.Run(tc.outcome, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := tcp.qualityStagedProbe(ctx, protocol.QualityTarget{Host: tc.host, Port: tc.port, Family: "ipv4", Protocol: "tcp"})
			d, err := protocol.DecodeQualityDetails(r.details)
			if err != nil || r.outcome != tc.outcome {
				t.Fatal(r, d, err)
			}
			if tc.outcome == "success" && (d.DNSMS < 200 || d.ConnectMS == nil || *d.ConnectMS > 100) {
				t.Fatal("DNS mixed into pure connect", d)
			}
			if tc.outcome == "dns_error" && d.ConnectAttempted {
				t.Fatal("NXDOMAIN manufactured connect", d)
			}
			t.Logf("actual TCP outcome=%s resolved=%s details=%s", r.outcome, r.ip, r.details)
		})
	}
}
