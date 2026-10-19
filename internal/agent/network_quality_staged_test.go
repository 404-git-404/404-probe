package agent

import (
	"404-probe/internal/protocol"
	"context"
	"errors"
	"golang.org/x/net/icmp"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"
)

type a2Resolver struct {
	delay  time.Duration
	err    error
	family string
}

func (r *a2Resolver) LookupNetIP(ctx context.Context, family, host string) ([]netip.Addr, error) {
	r.family = family
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(r.delay):
	}
	if r.err != nil {
		return nil, r.err
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

type a2CloseConn struct {
	net.Conn
	delay time.Duration
}

func (c a2CloseConn) Close() error { time.Sleep(c.delay); return c.Conn.Close() }
func TestA2TCPDNSConnectCloseAndActualRefused(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	resolver := &a2Resolver{delay: 220 * time.Millisecond}
	dialer := &net.Dialer{}
	e := &TCPExecutor{qualityResolver: resolver, qualityDial: func(ctx context.Context, n, a string) (net.Conn, error) {
		c, err := dialer.DialContext(ctx, n, a)
		if err != nil {
			return nil, err
		}
		return a2CloseConn{c, 120 * time.Millisecond}, nil
	}}
	target := protocol.QualityTarget{Host: "controlled.test", Port: listener.Addr().(*net.TCPAddr).Port, Family: "ipv4", Protocol: "tcp"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	result := e.qualityStagedProbe(ctx, target)
	d, err := protocol.DecodeQualityDetails(result.details)
	if err != nil || result.outcome != "success" || d.DNSMS < 200 || d.ConnectMS == nil || *d.ConnectMS > 100 || time.Since(start) < 320*time.Millisecond || resolver.family != "ip4" {
		t.Fatal(result, d, err)
	}
	t.Logf("actual TCP: dns=%.3fms connect=%.3fms including artificial slow close=%v", d.DNSMS, *d.ConnectMS, time.Since(start))
	port := target.Port
	listener.Close()
	target.Host = "127.0.0.1"
	target.Port = port
	result = e.qualityStagedProbe(ctx, target)
	d, _ = protocol.DecodeQualityDetails(result.details)
	if result.outcome != "refused" || !d.ProbeComplete || !d.ConnectComplete || d.DNSMS != 0 {
		t.Fatal(result, d)
	}
	resolver.err = &net.DNSError{Err: "controlled NXDOMAIN", Name: target.Host, IsNotFound: true}
	target.Host = "missing.test"
	result = e.qualityStagedProbe(ctx, target)
	d, _ = protocol.DecodeQualityDetails(result.details)
	if result.outcome != "dns_error" || d.ConnectAttempted || !d.ProbeComplete {
		t.Fatal(result, d)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	result = e.qualityStagedProbe(canceled, target)
	d, _ = protocol.DecodeQualityDetails(result.details)
	if result.outcome != "canceled" || d.ProbeComplete || d.ConnectComplete {
		t.Fatal(result, d)
	}
}

func TestA2StagedTCPTimeoutBeforeContextUpdate(t *testing.T) {
	ctx := context.Background()
	socketTimeout := &net.OpError{Op: "dial", Net: "tcp4", Err: os.ErrDeadlineExceeded}
	if ctx.Err() != nil || errors.Is(socketTimeout, context.DeadlineExceeded) || !socketTimeout.Timeout() {
		t.Fatal("fixture does not represent socket deadline before context update")
	}
	legacy, _ := classifyTCPProbeError(ctx, socketTimeout)
	if legacy != "network_error" {
		t.Fatal("legacy classification changed", legacy)
	}
	e := &TCPExecutor{qualityDial: func(context.Context, string, string) (net.Conn, error) { return nil, socketTimeout }}
	r := e.qualityStagedProbe(ctx, protocol.QualityTarget{Host: "127.0.0.1", Port: 9452, Protocol: "tcp", Family: "ipv4"})
	d, err := protocol.DecodeQualityDetails(r.details)
	if err != nil || r.outcome != "timeout" || r.latency != nil || d.ErrorCode != "timeout" || d.ErrorStage != "connect" || !d.ConnectAttempted || !d.ConnectComplete || !d.ProbeComplete || ctx.Err() != nil {
		t.Fatal(r, d, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := qualityTCPConnectOutcome(canceled, socketTimeout); got != "canceled" {
		t.Fatal("socket timeout overrode explicit cancellation", got)
	}
	if got := qualityTCPConnectOutcome(ctx, io.ErrUnexpectedEOF); got != "network_error" {
		t.Fatal("non-timeout network error changed", got)
	}
	t.Log("ctx.Err=nil, wrapped poll deadline Timeout=true: staged timeout; legacy network_error; explicit cancellation preserved")
}

type a2PacketConn struct {
	mu       sync.Mutex
	deadline time.Time
	wake     chan struct{}
	packets  chan queuedICMPRead
	requests []*icmp.Echo
	receive  int
	fault    error
	short    bool
	writes   []time.Time
}

func (c *a2PacketConn) SetDeadline(d time.Time) error {
	c.mu.Lock()
	c.deadline = d
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}
func (c *a2PacketConn) Close() error { return nil }
func (c *a2PacketConn) WriteTo(packet []byte, address net.Addr) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !time.Now().Before(c.deadline) {
		return 0, os.ErrDeadlineExceeded
	}
	if c.short {
		return len(packet) - 1, nil
	}
	m, err := icmp.ParseMessage(1, packet)
	if err != nil {
		return 0, err
	}
	p := m.Body.(*icmp.Echo)
	c.requests = append(c.requests, p)
	c.writes = append(c.writes, time.Now())
	if p.Seq == 1 && c.receive > 0 {
		reply, _ := (&icmp.Message{Type: echoReplyType(icmpFamilyIPv4), Body: p}).Marshal(nil)
		c.packets <- queuedICMPRead{packet: reply, source: &net.IPAddr{IP: net.ParseIP("127.0.0.1")}}
	}
	if p.Seq == 3 {
		for _, seq := range []int{3, 2, 2} {
			if seq > c.receive {
				continue
			}
			echo := c.requests[seq-1]
			reply, _ := (&icmp.Message{Type: echoReplyType(icmpFamilyIPv4), Body: echo}).Marshal(nil)
			c.packets <- queuedICMPRead{packet: reply, source: &net.IPAddr{IP: net.ParseIP("127.0.0.1")}}
		}
	}
	return len(packet), nil
}
func (c *a2PacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if c.fault != nil {
		return 0, nil, c.fault
	}
	for {
		c.mu.Lock()
		deadline := c.deadline
		c.mu.Unlock()
		timer := time.NewTimer(max(time.Duration(0), time.Until(deadline)))
		select {
		case p := <-c.packets:
			timer.Stop()
			return copy(b, p.packet), p.source, p.err
		case <-timer.C:
			return 0, nil, os.ErrDeadlineExceeded
		case <-c.wake:
			timer.Stop()
		}
	}
}
func TestA2ICMPThreeEchoDeadlinesReorderDupAndIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name     string
		received int
		cancel   bool
		fault    error
		short    bool
	}{{"3of3", 3, false, nil, false}, {"3of2", 2, false, nil, false}, {"3of0", 0, false, nil, false}, {"cancel", 0, true, nil, false}, {"receiver_fault", 0, false, io.ErrUnexpectedEOF, false}, {"short_write", 0, false, nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := &a2PacketConn{wake: make(chan struct{}, 1), packets: make(chan queuedICMPRead, 8), receive: tc.received, fault: tc.fault, short: tc.short}
			e := &ICMPExecutor{backend: fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) { return c, false, nil }}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if tc.cancel {
				time.AfterFunc(250*time.Millisecond, cancel)
			}
			target := protocol.QualityTarget{ID: "a2target", Host: "127.0.0.1", Protocol: "icmp", Family: "ipv4"}
			start := time.Now()
			r := e.qualityStagedProbe(ctx, target)
			finish := time.Now()
			d, err := protocol.DecodeQualityDetails(r.details)
			if err != nil {
				t.Fatal(err)
			}
			sample := protocol.QualitySample{Sequence: "1", TargetID: target.ID, ConfigRevision: "1", SlotRevision: "1", ScheduledAt: start.UnixMilli(), StartedAt: start.UnixMilli(), FinishedAt: finish.UnixMilli(), DurationMS: qualityMS(finish.Sub(start)), Outcome: r.outcome, ResolvedIP: r.ip, Sent: r.sent, Received: r.received, LatencyMS: r.latency, Details: r.details}
			if reason := sample.Validate(finish, target); reason != "" {
				t.Fatal("invalid produced sample", reason, string(r.details))
			}
			if tc.cancel || tc.fault != nil || tc.short {
				if sample.PacketDenominator() || d.WindowComplete {
					t.Fatal("local failure fabricated loss", sample, d)
				}
			} else {
				if r.sent != 3 || r.received != tc.received || !d.WindowComplete {
					t.Fatal(r, d)
				}
				for i := 1; i < len(c.writes); i++ {
					if c.writes[i].Sub(c.writes[i-1]) < 199*time.Millisecond {
						t.Fatal("spacing", c.writes)
					}
				}
			}
			t.Logf("sent=%d received=%d outcome=%s elapsed=%v details=%s", r.sent, r.received, r.outcome, finish.Sub(start), r.details)
		})
	}
}
func TestA2ICMPPermissionDetectionFact(t *testing.T) {
	backend := detectICMPSocketBackend(map[icmpFamily][]icmpSocketMode{icmpFamilyIPv4: {{network: "ip4:icmp"}}}, func(string, string) (icmpPacketConn, error) { return nil, os.ErrPermission })
	e := &ICMPExecutor{backend: backend}
	r := e.qualityStagedProbe(context.Background(), protocol.QualityTarget{Host: "127.0.0.1", Family: "ipv4", Protocol: "icmp"})
	d, err := protocol.DecodeQualityDetails(r.details)
	if err != nil || r.outcome != "permission_denied" || d.ErrorStage != "socket" || d.ProbeComplete || r.sent != 0 || !errors.Is(backend.Failure(icmpFamilyIPv4), os.ErrPermission) {
		t.Fatal(r, d, err)
	}
}
