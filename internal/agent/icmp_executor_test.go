package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"404-probe/internal/protocol"
	"golang.org/x/net/icmp"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

type fakeICMPBackend struct {
	v4   bool
	v6   bool
	open func(icmpFamily) (icmpPacketConn, bool, error)
}

func (b fakeICMPBackend) Supports(family icmpFamily) bool {
	if family == icmpFamilyIPv4 {
		return b.v4
	}
	return b.v6
}

func (b fakeICMPBackend) Open(family icmpFamily) (icmpPacketConn, bool, error) {
	if !b.Supports(family) {
		return nil, false, errICMPUnsupportedFamily
	}
	return b.open(family)
}

type fakeICMPConn struct {
	read        func([]byte) (int, net.Addr, error)
	write       func([]byte, net.Addr) (int, error)
	setDeadline func(time.Time) error
	closed      atomic.Int32
}

func (c *fakeICMPConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	return c.read(buffer)
}

func (c *fakeICMPConn) WriteTo(packet []byte, address net.Addr) (int, error) {
	return c.write(packet, address)
}

func (c *fakeICMPConn) Close() error {
	c.closed.Add(1)
	return nil
}

func (c *fakeICMPConn) SetDeadline(deadline time.Time) error {
	if c.setDeadline == nil {
		return nil
	}
	return c.setDeadline(deadline)
}

type queuedICMPRead struct {
	packet []byte
	source net.Addr
	err    error
}

func TestICMPExecutorCapabilitiesReflectBackend(t *testing.T) {
	for _, test := range []struct {
		name      string
		v4        bool
		v6        bool
		supported bool
	}{
		{name: "none"},
		{name: "IPv4", v4: true, supported: true},
		{name: "IPv6", v6: true, supported: true},
		{name: "dual stack", v4: true, v6: true, supported: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &ICMPExecutor{backend: fakeICMPBackend{v4: test.v4, v6: test.v6}}
			capabilities := executor.SupportedProbeTypes()
			if test.supported && (len(capabilities) != 1 || capabilities[0] != protocol.ProbeTypeICMPPing) {
				t.Fatalf("capabilities = %v", capabilities)
			}
			if !test.supported && len(capabilities) != 0 {
				t.Fatalf("capabilities = %v", capabilities)
			}
		})
	}
}

func TestICMPExecutorRejectsUnavailableAndInvalidJobsBeforeIO(t *testing.T) {
	job := validICMPExecutorJob("127.0.0.1", 1)
	if _, err := (&ICMPExecutor{backend: fakeICMPBackend{}}).Execute(context.Background(), job); !errors.Is(err, ErrUnsupportedProbeType) {
		t.Fatalf("unavailable backend error = %v", err)
	}

	for _, test := range []struct {
		name   string
		target string
		count  int
	}{
		{name: "empty target", count: 1},
		{name: "invalid count", target: "127.0.0.1", count: 11},
	} {
		t.Run(test.name, func(t *testing.T) {
			var resolverCalls atomic.Int32
			var openCalls atomic.Int32
			executor := &ICMPExecutor{
				backend: fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) {
					openCalls.Add(1)
					return nil, false, errors.New("unexpected open")
				}},
				resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
					resolverCalls.Add(1)
					return nil, errors.New("unexpected resolve")
				}),
			}
			execution, err := executor.Execute(context.Background(), validICMPExecutorJob(test.target, test.count))
			if err != nil || execution.Success || execution.ErrorCategory != "invalid_config" || resolverCalls.Load() != 0 || openCalls.Load() != 0 {
				t.Fatalf("execution=%+v err=%v resolver=%d open=%d", execution, err, resolverCalls.Load(), openCalls.Load())
			}
			assertValidICMPExecution(t, execution)
		})
	}
}

func TestICMPExecutorFiltersRepliesAndCalculatesStatistics(t *testing.T) {
	target := netip.MustParseAddr("192.0.2.10")
	foreign := &net.IPAddr{IP: net.ParseIP("192.0.2.11")}
	source := &net.IPAddr{IP: net.ParseIP(target.String())}
	var writes [][]byte
	var reads []queuedICMPRead
	readIndex := 0
	connection := &fakeICMPConn{}
	connection.write = func(packet []byte, destination net.Addr) (int, error) {
		if got := destination.(*net.IPAddr).IP.String(); got != target.String() {
			t.Fatalf("destination = %q", got)
		}
		writes = append(writes, append([]byte(nil), packet...))
		if len(writes) == 3 {
			reply1 := icmpReplyForRequest(t, icmpFamilyIPv4, writes[0])
			reply2 := icmpReplyForRequest(t, icmpFamilyIPv4, writes[1])
			reply3 := icmpReplyForRequest(t, icmpFamilyIPv4, writes[2])
			wrongNonce := alteredICMPReply(t, icmpFamilyIPv4, writes[0], 1, 0, true)
			wrongSequence := alteredICMPReply(t, icmpFamilyIPv4, writes[0], 4, 0, false)
			wrongCode := alteredICMPReply(t, icmpFamilyIPv4, writes[0], 1, 1, false)
			reads = []queuedICMPRead{
				{packet: []byte{1, 2}, source: source},
				{packet: reply1, source: foreign},
				{packet: wrongNonce, source: source},
				{packet: wrongSequence, source: source},
				{packet: wrongCode, source: source},
				{packet: reply2, source: source},
				{packet: reply2, source: source},
				{packet: reply1, source: source},
				{packet: reply3, source: source},
			}
		}
		return len(packet), nil
	}
	connection.read = func(buffer []byte) (int, net.Addr, error) {
		read := reads[readIndex]
		readIndex++
		copy(buffer, read.packet)
		return len(read.packet), read.source, read.err
	}

	base := time.Unix(1, 0)
	times := []time.Time{base, base, base, base.Add(4 * time.Millisecond), base.Add(6 * time.Millisecond), base.Add(8 * time.Millisecond)}
	timeIndex := 0
	executor := testICMPExecutor(fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) {
		return connection, false, nil
	}})
	executor.now = func() time.Time {
		value := times[timeIndex]
		timeIndex++
		return value
	}
	execution, err := executor.Execute(context.Background(), validICMPExecutorJob(target.String(), 3))
	if err != nil || !execution.Success || execution.ResolvedIP != target.String() || connection.closed.Load() != 1 {
		t.Fatalf("execution=%+v err=%v closed=%d", execution, err, connection.closed.Load())
	}
	result := execution.Result.ICMPPing
	if result.Sent != 3 || result.Received != 3 || result.PacketLossPercent != 0 || result.LatencyMinMS != 4 || result.LatencyAvgMS != 6 || result.LatencyMaxMS != 8 {
		t.Fatalf("result = %+v", result)
	}
	assertValidICMPExecution(t, execution)
}

func TestICMPExecutorSelectsFirstSupportedResolvedAddress(t *testing.T) {
	target := netip.MustParseAddr("192.0.2.20")
	connection := singleReplyICMPConn(t, icmpFamilyIPv4, target)
	executor := testICMPExecutor(fakeICMPBackend{v4: true, open: func(family icmpFamily) (icmpPacketConn, bool, error) {
		if family != icmpFamilyIPv4 {
			t.Fatalf("family = %d", family)
		}
		return connection, false, nil
	}})
	executor.resolver = resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip" || host != "probe.example" {
			t.Fatalf("lookup = %q %q", network, host)
		}
		return []netip.Addr{netip.MustParseAddr("2001:db8::20"), target}, nil
	})
	execution, err := executor.Execute(context.Background(), validICMPExecutorJob("probe.example", 1))
	if err != nil || !execution.Success || execution.ResolvedIP != target.String() {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	assertValidICMPExecution(t, execution)
}

func TestICMPExecutorIPv6AndMappedIPv4(t *testing.T) {
	for _, test := range []struct {
		name       string
		target     string
		family     icmpFamily
		resolvedIP string
	}{
		{name: "IPv6", target: "2001:db8::60", family: icmpFamilyIPv6, resolvedIP: "2001:db8::60"},
		{name: "mapped IPv4", target: "::ffff:192.0.2.60", family: icmpFamilyIPv4, resolvedIP: "192.0.2.60"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := netip.MustParseAddr(test.resolvedIP)
			connection := singleReplyICMPConn(t, test.family, target)
			originalWrite := connection.write
			connection.write = func(packet []byte, destination net.Addr) (int, error) {
				if _, ok := destination.(*net.UDPAddr); !ok {
					t.Fatalf("datagram destination = %T", destination)
				}
				return originalWrite(packet, destination)
			}
			backend := fakeICMPBackend{v4: test.family == icmpFamilyIPv4, v6: test.family == icmpFamilyIPv6, open: func(family icmpFamily) (icmpPacketConn, bool, error) {
				if family != test.family {
					t.Fatalf("family = %d", family)
				}
				return connection, true, nil
			}}
			execution, err := testICMPExecutor(backend).Execute(context.Background(), validICMPExecutorJob(test.target, 1))
			if err != nil || !execution.Success || execution.ResolvedIP != test.resolvedIP {
				t.Fatalf("execution=%+v err=%v", execution, err)
			}
			assertValidICMPExecution(t, execution)
		})
	}
}

func TestICMPExecutorDeadlineResults(t *testing.T) {
	for _, test := range []struct {
		name         string
		replies      int
		wantSuccess  bool
		wantCategory string
		wantReceived int
		wantLoss     float64
	}{
		{name: "partial reply succeeds", replies: 1, wantSuccess: true, wantReceived: 1, wantLoss: 50},
		{name: "no reply times out", wantCategory: "timeout", wantLoss: 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := netip.MustParseAddr("192.0.2.30")
			var requests [][]byte
			readIndex := 0
			connection := &fakeICMPConn{}
			connection.write = func(packet []byte, _ net.Addr) (int, error) {
				requests = append(requests, append([]byte(nil), packet...))
				return len(packet), nil
			}
			connection.read = func(buffer []byte) (int, net.Addr, error) {
				if readIndex < test.replies {
					packet := icmpReplyForRequest(t, icmpFamilyIPv4, requests[readIndex])
					readIndex++
					copy(buffer, packet)
					return len(packet), &net.IPAddr{IP: net.ParseIP(target.String())}, nil
				}
				return 0, nil, os.ErrDeadlineExceeded
			}
			executor := testICMPExecutor(fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) {
				return connection, false, nil
			}})
			execution, err := executor.Execute(context.Background(), validICMPExecutorJob(target.String(), 2))
			if err != nil || execution.Success != test.wantSuccess || execution.ErrorCategory != test.wantCategory || connection.closed.Load() != 1 {
				t.Fatalf("execution=%+v err=%v closed=%d", execution, err, connection.closed.Load())
			}
			result := execution.Result.ICMPPing
			if result.Received != test.wantReceived || result.PacketLossPercent != test.wantLoss {
				t.Fatalf("result = %+v", result)
			}
			assertValidICMPExecution(t, execution)
		})
	}
}

func TestICMPExecutorCancellationPreservesPartialStatistics(t *testing.T) {
	target := netip.MustParseAddr("192.0.2.40")
	firstRead := make(chan struct{})
	unblockRead := make(chan struct{})
	var unblock sync.Once
	var requests [][]byte
	readCount := 0
	connection := &fakeICMPConn{}
	connection.write = func(packet []byte, _ net.Addr) (int, error) {
		requests = append(requests, append([]byte(nil), packet...))
		return len(packet), nil
	}
	connection.read = func(buffer []byte) (int, net.Addr, error) {
		if readCount == 0 {
			readCount++
			packet := icmpReplyForRequest(t, icmpFamilyIPv4, requests[0])
			copy(buffer, packet)
			close(firstRead)
			return len(packet), &net.IPAddr{IP: net.ParseIP(target.String())}, nil
		}
		<-unblockRead
		return 0, nil, os.ErrDeadlineExceeded
	}
	connection.setDeadline = func(time.Time) error {
		unblock.Do(func() { close(unblockRead) })
		return nil
	}
	executor := testICMPExecutor(fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) {
		return connection, false, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Execution, 1)
	go func() {
		execution, err := executor.Execute(ctx, validICMPExecutorJob(target.String(), 2))
		if err != nil {
			t.Errorf("execute: %v", err)
		}
		done <- execution
	}()
	waitSignal(t, firstRead, "first ICMP reply")
	cancel()
	select {
	case execution := <-done:
		if execution.Success || execution.ErrorCategory != "canceled" || execution.Result.ICMPPing.Received != 1 || execution.Result.ICMPPing.PacketLossPercent != 50 || connection.closed.Load() != 1 {
			t.Fatalf("execution=%+v closed=%d", execution, connection.closed.Load())
		}
		assertValidICMPExecution(t, execution)
	case <-time.After(2 * time.Second):
		t.Fatal("ICMP read did not stop after cancellation")
	}
}

func TestICMPExecutorStableSetupAndSocketFailures(t *testing.T) {
	t.Run("DNS", func(t *testing.T) {
		executor := testICMPExecutor(fakeICMPBackend{v4: true})
		executor.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, &net.DNSError{Err: "private resolver detail", Name: "private.example"}
		})
		execution, err := executor.Execute(context.Background(), validICMPExecutorJob("private.example", 1))
		if err != nil || execution.ErrorCategory != "dns_error" || execution.ErrorMessage != "ICMP probe hostname resolution failed" {
			t.Fatalf("execution=%+v err=%v", execution, err)
		}
		assertValidICMPExecution(t, execution)
	})

	for _, test := range []struct {
		name     string
		openErr  error
		category string
		message  string
	}{
		{name: "permission", openErr: &os.PathError{Op: "socket", Path: "private", Err: syscall.EACCES}, category: "permission_denied", message: "ICMP socket permission was denied"},
		{name: "unsupported family", openErr: errICMPUnsupportedFamily, category: "unsupported_address_family", message: "ICMP target address family is unavailable"},
		{name: "network", openErr: errors.New("private operating system detail"), category: "network_error", message: "ICMP network operation failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := testICMPExecutor(fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) {
				return nil, false, test.openErr
			}})
			execution, err := executor.Execute(context.Background(), validICMPExecutorJob("127.0.0.1", 1))
			if err != nil || execution.Success || execution.ErrorCategory != test.category || execution.ErrorMessage != test.message {
				t.Fatalf("execution=%+v err=%v", execution, err)
			}
			assertValidICMPExecution(t, execution)
		})
	}
}

func TestICMPExecutorDNSContextErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		deadline bool
		category string
	}{
		{name: "timeout", deadline: true, category: "timeout"},
		{name: "cancellation", category: "canceled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if test.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
				<-ctx.Done()
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}
			defer cancel()
			executor := testICMPExecutor(fakeICMPBackend{v4: true})
			executor.resolver = resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
				return nil, ctx.Err()
			})
			execution, err := executor.Execute(ctx, validICMPExecutorJob("probe.example", 1))
			if err != nil || execution.Success || execution.ErrorCategory != test.category {
				t.Fatalf("execution=%+v err=%v", execution, err)
			}
			assertValidICMPExecution(t, execution)
		})
	}
}

func TestICMPSocketCancellationBeatsDeadlineError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	category, message := classifyICMPSocketError(ctx, os.ErrDeadlineExceeded)
	if category != "canceled" || message != "ICMP probe was canceled" {
		t.Fatalf("classification = %q %q", category, message)
	}
}

func TestICMPExecutorUnsupportedLiteralFamilyDoesNotOpenSocket(t *testing.T) {
	var opens atomic.Int32
	executor := testICMPExecutor(fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) {
		opens.Add(1)
		return nil, false, errors.New("unexpected open")
	}})
	execution, err := executor.Execute(context.Background(), validICMPExecutorJob("2001:db8::1", 1))
	if err != nil || execution.Success || execution.ErrorCategory != "unsupported_address_family" || opens.Load() != 0 {
		t.Fatalf("execution=%+v err=%v opens=%d", execution, err, opens.Load())
	}
	assertValidICMPExecution(t, execution)
}

func TestICMPExecutorClosesSocketAfterWriteFailure(t *testing.T) {
	connection := &fakeICMPConn{
		read: func([]byte) (int, net.Addr, error) { return 0, nil, errors.New("unexpected read") },
		write: func([]byte, net.Addr) (int, error) {
			return 0, errors.New("private write failure")
		},
	}
	executor := testICMPExecutor(fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) {
		return connection, false, nil
	}})
	execution, err := executor.Execute(context.Background(), validICMPExecutorJob("127.0.0.1", 1))
	if err != nil || execution.Success || execution.ErrorCategory != "network_error" || connection.closed.Load() != 1 {
		t.Fatalf("execution=%+v err=%v closed=%d", execution, err, connection.closed.Load())
	}
	assertValidICMPExecution(t, execution)
}

func TestDetectICMPSocketBackendUsesFirstWorkingMode(t *testing.T) {
	var attempts []string
	var probeCloses atomic.Int32
	listen := func(network, _ string) (icmpPacketConn, error) {
		attempts = append(attempts, network)
		if network != "ip4:icmp" {
			return nil, errors.New("unavailable")
		}
		return &fakeICMPConn{
			read:        func([]byte) (int, net.Addr, error) { return 0, nil, errors.New("unused") },
			write:       func([]byte, net.Addr) (int, error) { return 0, errors.New("unused") },
			setDeadline: func(time.Time) error { return nil },
			closed:      atomic.Int32{},
		}, nil
	}
	backend := detectICMPSocketBackend(map[icmpFamily][]icmpSocketMode{
		icmpFamilyIPv4: {{network: "udp4"}, {network: "ip4:icmp"}},
		icmpFamilyIPv6: {{network: "udp6"}},
	}, func(network, address string) (icmpPacketConn, error) {
		connection, err := listen(network, address)
		if err == nil {
			original := connection.(*fakeICMPConn)
			return &closeTrackingICMPConn{fakeICMPConn: original, closes: &probeCloses}, nil
		}
		return nil, err
	})
	if !backend.Supports(icmpFamilyIPv4) || backend.Supports(icmpFamilyIPv6) {
		t.Fatalf("supports IPv4=%t IPv6=%t", backend.Supports(icmpFamilyIPv4), backend.Supports(icmpFamilyIPv6))
	}
	udp4Index := slices.Index(attempts, "udp4")
	raw4Index := slices.Index(attempts, "ip4:icmp")
	if len(attempts) != 3 || udp4Index < 0 || raw4Index < 0 || udp4Index > raw4Index || !slices.Contains(attempts, "udp6") || probeCloses.Load() != 1 {
		t.Fatalf("attempts=%v probe closes=%d", attempts, probeCloses.Load())
	}
}

type closeTrackingICMPConn struct {
	*fakeICMPConn
	closes *atomic.Int32
}

func (c *closeTrackingICMPConn) Close() error {
	c.closes.Add(1)
	return c.fakeICMPConn.Close()
}

func TestICMPExecutorWorkerIntegration(t *testing.T) {
	target := netip.MustParseAddr("192.0.2.50")
	connection := singleReplyICMPConn(t, icmpFamilyIPv4, target)
	executor := testICMPExecutor(fakeICMPBackend{v4: true, open: func(icmpFamily) (icmpPacketConn, bool, error) {
		return connection, false, nil
	}})
	job := validICMPExecutorJob(target.String(), 1)
	resultReceived := make(chan protocol.JobResult, 1)
	var claims atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/agent/jobs/claim":
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
				return
			}
			claim, decodeErr := protocol.DecodeClaimRequest(body)
			if decodeErr != nil {
				t.Errorf("decode claim: %v", decodeErr)
				return
			}
			if len(claim.SupportedProbeTypes) != 1 || claim.SupportedProbeTypes[0] != protocol.ProbeTypeICMPPing {
				t.Errorf("capabilities = %v", claim.SupportedProbeTypes)
			}
			if claims.Add(1) == 1 {
				writeTestJSON(t, w, job)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/jobs/icmp-job/result":
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
				return
			}
			result, decodeErr := protocol.DecodeJobResult(body, protocol.ProbeTypeICMPPing)
			if decodeErr != nil {
				t.Errorf("decode result: %v", decodeErr)
				return
			}
			resultReceived <- result
			writeTestJSON(t, w, map[string]any{"accepted": true, "duplicate": false, "job_status": "finished"})
		default:
			http.NotFound(w, request)
		}
	}))
	defer api.Close()

	runner := newJobTestRunner(t, api.URL, time.Second, time.Second, executor)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runJobWorker(ctx)
		close(done)
	}()
	select {
	case result := <-resultReceived:
		if !result.Success || result.ResolvedIP != target.String() || result.Result.ICMPPing == nil || result.LeaseToken != job.LeaseToken || result.Attempt != job.Attempt {
			t.Fatalf("submitted result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not submit ICMP result")
	}
	cancel()
	waitSignal(t, done, "ICMP worker shutdown")
}

func TestICMPLoopbackSmoke(t *testing.T) {
	mode := os.Getenv("PROBE_ICMP_SMOKE")
	if mode == "" {
		t.Skip("set PROBE_ICMP_SMOKE=available or unavailable to exercise the real platform ICMP backend")
	}
	if mode != "available" && mode != "unavailable" {
		t.Fatalf("unknown PROBE_ICMP_SMOKE mode %q", mode)
	}
	executor := NewICMPExecutor()
	if len(executor.SupportedProbeTypes()) == 0 {
		if mode == "available" {
			t.Fatal("expected a usable ICMP socket family, but capability detection found none")
		}
		t.Log("platform correctly reports no usable ICMP socket family")
		return
	}
	if mode == "unavailable" {
		t.Fatalf("expected ICMP to be unavailable, but detected capabilities %v", executor.SupportedProbeTypes())
	}
	target := "127.0.0.1"
	if !executor.backend.Supports(icmpFamilyIPv4) {
		target = "::1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	execution, err := executor.Execute(ctx, validICMPExecutorJob(target, 1))
	if err != nil || !execution.Success || execution.Result.ICMPPing.Received != 1 {
		t.Fatalf("loopback execution=%+v err=%v", execution, err)
	}
}

func testICMPExecutor(backend icmpBackend) *ICMPExecutor {
	return &ICMPExecutor{
		backend: backend,
		resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, errors.New("unexpected DNS lookup")
		}),
		random: func(buffer []byte) (int, error) {
			for index := range buffer {
				buffer[index] = byte(index + 1)
			}
			return len(buffer), nil
		},
		now: time.Now,
	}
}

func validICMPExecutorJob(target string, count int) protocol.Job {
	now := time.Now().UnixMilli()
	return protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion,
		JobID:           "icmp-job",
		ProbeType:       protocol.ProbeTypeICMPPing,
		Config:          protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: target, Count: count}},
		CreatedAt:       now,
		NotBefore:       now,
		ExpiresAt:       now + 60_000,
		TimeoutMS:       1_000,
		Attempt:         1,
		LeaseToken:      "lease-token",
		LeaseExpiresAt:  now + 30_000,
	}
}

func singleReplyICMPConn(t *testing.T, family icmpFamily, target netip.Addr) *fakeICMPConn {
	t.Helper()
	var request []byte
	read := false
	connection := &fakeICMPConn{}
	connection.write = func(packet []byte, _ net.Addr) (int, error) {
		request = append([]byte(nil), packet...)
		return len(packet), nil
	}
	connection.read = func(buffer []byte) (int, net.Addr, error) {
		if read {
			return 0, nil, os.ErrDeadlineExceeded
		}
		read = true
		packet := icmpReplyForRequest(t, family, request)
		copy(buffer, packet)
		return len(packet), &net.IPAddr{IP: net.IP(target.AsSlice())}, nil
	}
	return connection
}

func icmpReplyForRequest(t *testing.T, family icmpFamily, request []byte) []byte {
	t.Helper()
	message, err := icmp.ParseMessage(icmpProtocol(family), request)
	if err != nil {
		t.Fatal(err)
	}
	echo, ok := message.Body.(*icmp.Echo)
	if !ok {
		t.Fatalf("request body = %T", message.Body)
	}
	reply, err := (&icmp.Message{
		Type: echoReplyType(family),
		Code: 0,
		Body: &icmp.Echo{ID: echo.ID, Seq: echo.Seq, Data: append([]byte(nil), echo.Data...)},
	}).Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func alteredICMPReply(t *testing.T, family icmpFamily, request []byte, sequence, code int, alterNonce bool) []byte {
	t.Helper()
	message, err := icmp.ParseMessage(icmpProtocol(family), request)
	if err != nil {
		t.Fatal(err)
	}
	echo := message.Body.(*icmp.Echo)
	data := append([]byte(nil), echo.Data...)
	if alterNonce {
		data[len(data)-1] ^= 0xff
	}
	reply, err := (&icmp.Message{
		Type: echoReplyType(family), Code: code,
		Body: &icmp.Echo{ID: echo.ID, Seq: sequence, Data: data},
	}).Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func assertValidICMPExecution(t *testing.T, execution Execution) {
	t.Helper()
	if err := execution.Result.Validate(protocol.ProbeTypeICMPPing); err != nil {
		t.Fatalf("result is invalid: %v", err)
	}
}
