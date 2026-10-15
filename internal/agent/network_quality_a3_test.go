package agent

import (
	"404-probe/internal/protocol"
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// Delay the first scheduling timeout after it expires, as when a CPU-limited
// process resumes late. Replies still use the normal staged packet fixture.
type a3LateWakeConn struct {
	*a2PacketConn
	late    time.Duration
	delayed bool
}

func (c *a3LateWakeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.a2PacketConn.ReadFrom(b)
	if err == os.ErrDeadlineExceeded && !c.delayed {
		c.delayed = true
		time.Sleep(c.late)
	}
	return n, addr, err
}

func TestA3ICMPLateWakeDoesNotCatchUpPackets(t *testing.T) {
	for _, late := range []time.Duration{55 * time.Millisecond, 250 * time.Millisecond} {
		t.Run(late.String(), func(t *testing.T) {
			c := &a3LateWakeConn{a2PacketConn: &a2PacketConn{wake: make(chan struct{}, 1), packets: make(chan queuedICMPRead, 8), receive: 3}, late: late}
			e := &ICMPExecutor{backend: fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) { return c, false, nil }}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			target := protocol.QualityTarget{ID: "a3target", Host: "127.0.0.1", Protocol: "icmp", Family: "ipv4"}
			start := time.Now()
			r := e.qualityStagedProbe(ctx, target)
			finish := time.Now()
			d, err := protocol.DecodeQualityDetails(r.details)
			if err != nil || !c.delayed || r.sent != 3 || r.received != 3 || r.outcome != "success" || !d.WindowComplete {
				t.Fatal("late wake fixture/result", r, d, err)
			}
			for i := 1; i < len(c.writes); i++ {
				if gap := c.writes[i].Sub(c.writes[i-1]); gap < 199*time.Millisecond {
					t.Errorf("actual write gap %v after delayed wake; writes=%v", gap, c.writes)
				}
			}
			sample := protocol.QualitySample{Sequence: "1", TargetID: target.ID, ConfigRevision: "1", SlotRevision: "1", ScheduledAt: start.UnixMilli(), StartedAt: start.UnixMilli(), FinishedAt: finish.UnixMilli(), DurationMS: qualityMS(finish.Sub(start)), Outcome: r.outcome, ResolvedIP: r.ip, Sent: r.sent, Received: r.received, LatencyMS: r.latency, Details: r.details}
			if reason := sample.Validate(finish, target); reason != "" {
				t.Errorf("executor produced invalid upload: %s details=%s", reason, r.details)
			}
			t.Logf("late=%v actual gaps=%v,%v elapsed=%v details=%s", late, c.writes[1].Sub(c.writes[0]), c.writes[2].Sub(c.writes[1]), finish.Sub(start), r.details)
		})
	}
}

func TestA3ICMPCancelDuringLateWakeSpacing(t *testing.T) {
	c := &a3LateWakeConn{a2PacketConn: &a2PacketConn{wake: make(chan struct{}, 1), packets: make(chan queuedICMPRead, 8), receive: 3}, late: 55 * time.Millisecond}
	e := &ICMPExecutor{backend: fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) { return c, false, nil }}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(350*time.Millisecond, cancel)
	defer timer.Stop()
	r := e.qualityStagedProbe(ctx, protocol.QualityTarget{Host: "127.0.0.1", Protocol: "icmp", Family: "ipv4"})
	d, err := protocol.DecodeQualityDetails(r.details)
	if err != nil || r.outcome != "canceled" || r.sent != 2 || d.WindowComplete || d.ProbeComplete || d.Echoes[2].SentMS != nil {
		t.Fatal("cancellation sent a catch-up packet or completed the observation", r, d, err)
	}
}
