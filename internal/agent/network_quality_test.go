package agent

import (
	"404-probe/internal/protocol"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/net/icmp"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func n2bTarget() protocol.QualityTarget {
	return protocol.QualityTarget{ID: strings.Repeat("a", 32), Slot: "telecom", Family: "ipv4", SlotRevision: "1", Source: "manual", Protocol: "tcp", Host: "127.0.0.1", Port: 80, Status: "active"}
}
func n2bConfig() protocol.QualityConfig {
	return protocol.QualityConfig{Version: 1, Revision: "1", Supported: true, Enabled: true, IntervalMS: 30000, JitterPercent: 20, TimeoutMS: 5000, Targets: []protocol.QualityTarget{n2bTarget()}}
}
func n2bSample(wall int64) protocol.QualitySample {
	return protocol.QualitySample{TargetID: strings.Repeat("a", 32), ConfigRevision: "1", SlotRevision: "1", ScheduledAt: wall, StartedAt: wall, FinishedAt: wall, Outcome: "canceled"}
}

func TestN2bQueueLimitsImmutableBatchAndSequence(t *testing.T) {
	now := time.Now()
	q := qualityQueue{}
	for i := 0; i < 6000; i++ {
		if err := q.append(n2bSample(1), now); err != nil {
			t.Fatal(err)
		}
	}
	if len(q.rows) != 6000 || q.bytes > qualityQueueBytes {
		t.Fatal(len(q.rows), q.bytes)
	}
	u, err := q.pack("1", "session")
	if err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), u.body...)
	if err := q.append(n2bSample(2), now); err != nil {
		t.Fatal(err)
	}
	if len(q.rows) != 6000 || q.rows[0].seq != "2" {
		t.Fatal("not oldest-first")
	}
	ack := protocol.QualityBatchResponse{Results: []protocol.QualityAck{{"0", "1", "committed"}}}
	if complete, err := q.acknowledge(u, ack, func(string) {}); err != nil || complete {
		t.Fatal(complete, err)
	}
	if q.rows[0].seq != "2" || q.rows[len(q.rows)-1].seq != "6001" || !bytes.Equal(original, u.body) {
		t.Fatal("ACK removed current position or mutated batch")
	}
	q.expire(now.Add(6 * time.Hour))
	if len(q.rows) != 0 || q.bytes != 0 {
		t.Fatal("age bound")
	}
	q.sequence = math.MaxInt64 - 1
	if err := q.append(n2bSample(1), now); err != nil || q.rows[0].seq != strconv.FormatInt(math.MaxInt64, 10) {
		t.Fatal(err)
	}
	if err := q.append(n2bSample(1), now); err == nil {
		t.Fatal("wrapped sequence")
	}
	q.clear()
	if q.sequence != math.MaxInt64 {
		t.Fatal("sequence reset")
	}
	q = qualityQueue{sequence: math.MaxInt64 - 6000}
	for i := 0; i < 6000; i++ {
		sample := n2bSample(now.UnixMilli())
		sample.ConfigRevision = strconv.FormatInt(math.MaxInt64, 10)
		sample.SlotRevision = sample.ConfigRevision
		sample.ResolvedIP = "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff"
		v := 0.12345678901234567
		sample.LatencyMS = &v
		sample.DurationMS = v
		_ = q.append(sample, now)
	}
	if q.bytes > qualityQueueBytes || len(q.rows) >= 6000 {
		t.Fatal("encoded byte quota not enforced", q.bytes, len(q.rows))
	}
	for i := 0; i < 20; i++ {
		one := 1
		q.addGap(now.UnixMilli()+int64(i), now.UnixMilli()+int64(i), "queue_drop", &one)
	}
	u, err = q.pack(strconv.FormatInt(math.MaxInt64, 10), strings.Repeat("s", 128))
	if err != nil || len(u.body) > 32768 || len(u.batch.Samples) > 64 || len(u.batch.Gaps) != 8 {
		t.Fatal(err, len(u.body))
	}
	t.Logf("actual encoded queue bytes=%d rows=%d; full envelope bytes=%d samples=%d gaps=%d", q.bytes, len(q.rows), len(u.body), len(u.batch.Samples), len(u.batch.Gaps))
	for _, g := range u.batch.Gaps {
		if g.Dropped == nil {
			continue
		}
		if *g.Dropped > 6000 {
			t.Fatal("invented precise drop count")
		}
	}
	before := append([]byte(nil), u.body...)
	q.addGap(now.UnixMilli()+100, now.UnixMilli()+100, "queue_drop", nil)
	if !bytes.Equal(before, u.body) || len(q.gaps) > 8 {
		t.Fatal("mutated in-flight gap")
	}
}
func TestN2bACKMatrixAndPressureBackoff(t *testing.T) {
	for _, test := range []struct {
		name string
		acks []protocol.QualityAck
		bad  bool
	}{
		{"committed", []protocol.QualityAck{{"0", "1", "committed"}}, false},
		{"duplicate", []protocol.QualityAck{{"0", "1", "duplicate"}}, false},
		{"wrong-seq", []protocol.QualityAck{{"0", "2", "committed"}}, true},
		{"repeated-index", []protocol.QualityAck{{"0", "1", "committed"}, {"0", "1", "duplicate"}}, true},
		{"out-of-range", []protocol.QualityAck{{"1", "1", "committed"}}, true},
		{"noncanonical-index", []protocol.QualityAck{{"00", "1", "committed"}}, true},
		{"unknown", []protocol.QualityAck{{"0", "1", "ok"}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := qualityQueue{}
			_ = q.append(n2bSample(1), time.Now())
			u, _ := q.pack("1", "s")
			_, err := q.acknowledge(u, protocol.QualityBatchResponse{Results: test.acks}, func(string) {})
			if (err != nil) != test.bad {
				t.Fatal(err)
			}
			if test.bad && len(q.rows) != 1 {
				t.Fatal("bad ACK consumed queue")
			}
		})
	}
	for _, status := range []string{"quota", "storage_pressure"} {
		q := qualityQueue{}
		_ = q.append(n2bSample(1), time.Now())
		u, _ := q.pack("1", "s")
		complete, err := q.acknowledge(u, protocol.QualityBatchResponse{Results: []protocol.QualityAck{{"0", "1", status}}}, func(string) {})
		if err != nil || complete || len(q.rows) != 1 {
			t.Fatal(status, complete, err)
		}
	}
	for _, status := range []string{"invalid", "conflict", "too_old", "clock_skew", "unauthorized"} {
		q := qualityQueue{}
		_ = q.append(n2bSample(1), time.Now())
		u, _ := q.pack("1", "s")
		recorded := ""
		_, err := q.acknowledge(u, protocol.QualityBatchResponse{Results: []protocol.QualityAck{{"0", "1", status}}}, func(s string) { recorded = s })
		if err != nil || len(q.rows) != 0 || recorded != status {
			t.Fatal(status, err)
		}
	}
	for i := 1; i < 15; i++ {
		delay := qualityBackoff(i, 10*time.Second, func() float64 { return 1 })
		if delay > 5*time.Minute || delay < 8*time.Second {
			t.Fatal(delay)
		}
	}
}
func TestN2bScheduleJitterRevisionAndNoBurst(t *testing.T) {
	start := time.Now()
	cfg := n2bConfig()
	cfg.Targets = append(cfg.Targets, func() protocol.QualityTarget {
		v := n2bTarget()
		v.Slot = "unicom"
		v.ID = strings.Repeat("b", 32)
		return v
	}())
	var plan qualitySchedule
	plan.reconcile(&cfg, start, func() float64 { return 0 })
	first := plan.lanes["telecom/ipv4"]
	other := plan.lanes["unicom/ipv4"]
	if first.next.Sub(start) != 24*time.Second || qualityDelay(30*time.Second, func() float64 { return 1 }) != 36*time.Second {
		t.Fatal("jitter bounds")
	}
	plan.reconcile(&cfg, start.Add(time.Second), func() float64 { return 1 })
	if plan.lanes["telecom/ipv4"] != first || first.next.Sub(start) != 24*time.Second {
		t.Fatal("same revision reset")
	}
	var canceled atomic.Int32
	first.cancel = func() { canceled.Add(1) }
	cfg.Revision = "2"
	cfg.Targets[0].ID = strings.Repeat("c", 32)
	cfg.Targets[0].SlotRevision = "2"
	plan.reconcile(&cfg, start.Add(2*time.Second), func() float64 { return 1 })
	if canceled.Load() != 1 || plan.lanes["unicom/ipv4"] != other || plan.lanes["telecom/ipv4"].next.Before(start.Add(26*time.Second)) {
		t.Fatal("changed slot affected unchanged slot")
	}
	plan.reconcile(nil, start, func() float64 { return 0 })
	if len(plan.lanes) != 0 {
		t.Fatal("stop rescheduled")
	}
}
func TestN2bTCPForcedFamilyAndRealLoopback(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port := tcpAddressParts(t, listener.Addr())
	target := n2bTarget()
	target.Host = host
	target.Port = port
	accepted := make(chan struct{})
	go func() {
		c, err := listener.Accept()
		if err == nil {
			c.Close()
		}
		close(accepted)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := NewTCPExecutor().qualityProbe(ctx, target)
	if result.outcome != "success" || result.latency == nil || result.ip != "127.0.0.1" {
		t.Fatal(result)
	}
	<-accepted
	listener.Close()
	result = NewTCPExecutor().qualityProbe(ctx, target)
	if result.outcome != "refused" || result.latency != nil {
		t.Fatal(result)
	}
	for _, family := range []string{"ipv4", "ipv6"} {
		e := NewTCPExecutor()
		target.Family = family
		calls := 0
		e.dialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			calls++
			want := "tcp4"
			if family == "ipv6" {
				want = "tcp6"
			}
			if network != want {
				t.Fatal(network)
			}
			return nil, syscall.ECONNREFUSED
		}
		result = e.qualityProbe(ctx, target)
		if calls != 1 || result.outcome != "refused" || result.latency != nil {
			t.Fatal(result, calls)
		}
	}
}
func TestN2bICMPActualWritesFamilyAndReply(t *testing.T) {
	for _, variant := range []string{"success", "send-fail", "permission", "cancel", "dns"} {
		t.Run(variant, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			target := n2bTarget()
			target.Protocol = "icmp"
			target.Port = 0
			var packet []byte
			var reads int
			conn := &fakeICMPConn{}
			conn.write = func(p []byte, _ net.Addr) (int, error) {
				if variant == "send-fail" {
					return 0, errors.New("send")
				}
				packet = append([]byte(nil), p...)
				if variant == "cancel" {
					cancel()
				}
				return len(p), nil
			}
			conn.read = func(b []byte) (int, net.Addr, error) {
				if variant == "cancel" {
					return 0, nil, context.Canceled
				}
				message, _ := icmp.ParseMessage(1, packet)
				message.Type = echoReplyType(icmpFamilyIPv4)
				wire, _ := message.Marshal(nil)
				source := net.ParseIP("127.0.0.1")
				if reads == 0 {
					source = net.ParseIP("127.0.0.2")
				}
				reads++
				copy(b, wire)
				return len(wire), &net.IPAddr{IP: source}, nil
			}
			e := &ICMPExecutor{backend: fakeICMPBackend{v4: true, open: func(f icmpFamily) (icmpPacketConn, bool, error) {
				if f != icmpFamilyIPv4 {
					t.Fatal("family fallback")
				}
				if variant == "permission" {
					return nil, false, syscall.EACCES
				}
				return conn, false, nil
			}}, resolver: resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
				if network != "ip4" {
					t.Fatal(network)
				}
				return nil, &net.DNSError{Err: "missing"}
			})}
			if variant == "dns" {
				target.Host = "no.invalid"
			}
			result := e.qualityProbe(ctx, target)
			switch variant {
			case "success":
				if result.outcome != "success" || result.sent != 1 || result.received != 1 || result.latency == nil || reads != 2 {
					t.Fatal(result, reads)
				}
			case "cancel":
				if result.outcome != "canceled" || result.sent != 1 || result.received != 0 || result.latency != nil {
					t.Fatal(result)
				}
			default:
				if result.sent != 0 || result.received != 0 || result.latency != nil || result.outcome == "success" {
					t.Fatal(result)
				}
			}
			if variant != "permission" && variant != "dns" && conn.closed.Load() != 1 {
				t.Fatal("socket leaked")
			}
		})
	}
}
func TestN2bWireDecodeOldServerAndMalformedQuality(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v1/report" {
			t.Error("old Server received new request")
		}
		var report protocol.Report
		_ = json.NewDecoder(r.Body).Decode(&report)
		if report.NetworkQuality {
			t.Error("old Server opt-in")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"accepted":true,"capabilities":{},"network_quality":{"revision":123}}`))
	}))
	defer server.Close()
	r, err := NewWithReportCollector(Config{ServerURL: server.URL, AllowInsecureHTTP: true, AgentID: "id", Token: "token", Interval: time.Hour, Timeout: time.Second, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil, reportCollectorFunc(staticReportCollector))
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for i := 0; i < 2; i++ {
		ok, err := r.sendReport(context.Background(), &seq)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if requests.Load() != 2 || r.qualitySupported.Load() {
		t.Fatal(requests.Load())
	}
	for _, wire := range []string{`{"results":[["0","1","committed"]]} {}`, `{"results":[["0","1","committed"]]`, `{"results":[["0","1","committed","extra"]]}`, `{"results":[["0","1"]]}`, `{"gaps":[["0",null,"committed"]]}`, strings.Repeat(" ", 4097)} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(wire)) }))
		r.config.ServerURL = bad.URL
		var result protocol.QualityBatchResponse
		err := r.qualityHTTP(context.Background(), "/x", []byte(`{}`), 4096, &result)
		bad.Close()
		if err == nil {
			t.Fatal("bad ACK JSON accepted")
		}
	}
}

func TestN2bClockJumpIsolationReanchorAndStableRecovery(t *testing.T) {
	if !qualityClockValid(10000, 10005, 10006, 5*time.Millisecond, time.Millisecond) {
		t.Fatal("stable clock rejected")
	}
	if qualityClockValid(10000, 20005, 20006, 5*time.Millisecond, time.Millisecond) {
		t.Fatal("wall jump accepted")
	}
	fence := qualityClockFence{point: time.UnixMilli(10000)}
	if fence.accept(0, false, time.UnixMilli(20005)) {
		t.Fatal("bad old flight accepted")
	}
	for i := 0; i < 5; i++ {
		if fence.accept(0, true, time.UnixMilli(20006)) {
			t.Fatal("other old-anchor flight became valid")
		}
	}
	if fence.generation != 1 || fence.point.UnixMilli() != 20005 {
		t.Fatal("stale flight changed new anchor")
	}
	valid := qualityClockValid(fence.point.UnixMilli(), 20010, 20011, 5*time.Millisecond, time.Millisecond)
	if !valid || !fence.accept(1, valid, time.UnixMilli(20011)) {
		t.Fatal("stable new clock never recovered")
	}
	if qualityClockValid(20005, 20010, 20009, 5*time.Millisecond, -time.Millisecond) {
		t.Fatal("negative wall/duration hidden")
	}
}
