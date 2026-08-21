package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestTCPExecutorSuccessfulConnectClosesConnection(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			accepted <- acceptErr
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
		var buffer [1]byte
		_, readErr := connection.Read(buffer[:])
		accepted <- readErr
	}()

	host, port := tcpAddressParts(t, listener.Addr())
	execution, err := NewTCPExecutor().Execute(context.Background(), validTCPExecutorJob(host, port))
	if err != nil {
		t.Fatal(err)
	}
	if !execution.Success || execution.ResolvedIP != "127.0.0.1" || execution.Result.TCPConnect == nil || execution.Result.TCPConnect.ConnectMS < 0 {
		t.Fatalf("execution = %+v", execution)
	}
	if err := execution.Result.Validate(protocol.ProbeTypeTCPConnect); err != nil {
		t.Fatalf("result is invalid: %v", err)
	}
	select {
	case readErr := <-accepted:
		if !errors.Is(readErr, io.EOF) {
			t.Fatalf("target read after executor return = %v, want EOF", readErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("target connection was not closed")
	}
}

func TestTCPExecutorRejectsInvalidConfigWithoutDial(t *testing.T) {
	tests := []struct {
		name string
		job  protocol.Job
	}{
		{name: "empty host", job: validTCPExecutorJob("", 443)},
		{name: "host includes port", job: validTCPExecutorJob("example.com:443", 443)},
		{name: "port zero", job: validTCPExecutorJob("example.com", 0)},
		{name: "port too large", job: validTCPExecutorJob("example.com", 65536)},
		{name: "wrong typed config", job: func() protocol.Job {
			job := validTCPExecutorJob("example.com", 443)
			job.Config = protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: "https://example.com", Method: http.MethodGet}}
			return job
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dials atomic.Int32
			executor := NewTCPExecutor()
			executor.dialContext = func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("unexpected dial")
			}
			execution, err := executor.Execute(context.Background(), test.job)
			if err != nil || execution.Success || execution.ErrorCategory != "invalid_config" || execution.Result.TCPConnect == nil || execution.Result.TCPConnect.ConnectMS != 0 || dials.Load() != 0 {
				t.Fatalf("execution = %+v err=%v dials=%d", execution, err, dials.Load())
			}
			assertValidTCPExecution(t, execution)
		})
	}
}

func TestTCPExecutorJoinsIPv6Address(t *testing.T) {
	executor := NewTCPExecutor()
	executor.dialContext = func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "[2001:db8::1]:443" {
			t.Fatalf("dial = %q %q", network, address)
		}
		return nil, errors.New("test network failure")
	}
	execution, err := executor.Execute(context.Background(), validTCPExecutorJob("2001:db8::1", 443))
	if err != nil || execution.Success || execution.ErrorCategory != "network_error" || execution.ResolvedIP != "2001:db8::1" {
		t.Fatalf("execution = %+v err=%v", execution, err)
	}
	assertValidTCPExecution(t, execution)
}

func TestTCPExecutorClassifiesDialFailures(t *testing.T) {
	tests := []struct {
		name     string
		dialErr  error
		category string
		message  string
	}{
		{name: "DNS", dialErr: &net.DNSError{Err: "no such host", Name: "private.example"}, category: "dns_error", message: "TCP probe hostname resolution failed"},
		{name: "refused", dialErr: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, category: "connection_refused", message: "TCP connection was refused"},
		{name: "network", dialErr: errors.New("secret operating system detail"), category: "network_error", message: "TCP connection failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := NewTCPExecutor()
			executor.dialContext = func(context.Context, string, string) (net.Conn, error) {
				return nil, test.dialErr
			}
			execution, err := executor.Execute(context.Background(), validTCPExecutorJob("example.test", 443))
			if err != nil || execution.Success || execution.ErrorCategory != test.category || execution.ErrorMessage != test.message || execution.Result.TCPConnect.ConnectMS < 0 {
				t.Fatalf("execution = %+v err=%v", execution, err)
			}
			assertValidTCPExecution(t, execution)
		})
	}
}

func TestTCPExecutorTimeoutAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name     string
		deadline bool
		category string
	}{
		{name: "timeout", deadline: true, category: "timeout"},
		{name: "cancellation", category: "canceled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			executor := NewTCPExecutor()
			executor.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if test.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			done := make(chan Execution, 1)
			go func() {
				execution, executeErr := executor.Execute(ctx, validTCPExecutorJob("example.test", 443))
				if executeErr != nil {
					t.Errorf("execute: %v", executeErr)
				}
				done <- execution
			}()
			waitSignal(t, started, "TCP dial start")
			if !test.deadline {
				cancel()
			}
			select {
			case execution := <-done:
				if execution.Success || execution.ErrorCategory != test.category {
					t.Fatalf("execution = %+v", execution)
				}
				assertValidTCPExecution(t, execution)
			case <-time.After(2 * time.Second):
				t.Fatal("executor did not stop after context completion")
			}
		})
	}
}

func TestTCPExecutorRejectsOtherProbeTypes(t *testing.T) {
	execution, err := NewTCPExecutor().Execute(context.Background(), validHTTPExecutorJob("https://example.test", nil))
	if !errors.Is(err, ErrUnsupportedProbeType) || execution.Success {
		t.Fatalf("execution = %+v err=%v", execution, err)
	}
}

func TestProbeExecutorCapabilitiesAndDispatch(t *testing.T) {
	var httpCalls atomic.Int32
	var tcpCalls atomic.Int32
	executor := &ProbeExecutor{
		http: fakeExecutor{execute: func(context.Context, protocol.Job) (Execution, error) {
			httpCalls.Add(1)
			return Execution{Success: true, Result: protocol.ProbeResult{HTTP: &protocol.HTTPResult{}}}, nil
		}},
		tcp: fakeExecutor{execute: func(context.Context, protocol.Job) (Execution, error) {
			tcpCalls.Add(1)
			return Execution{Success: true, Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{}}}, nil
		}},
		icmp: UnsupportedExecutor{},
	}
	capabilities := executor.SupportedProbeTypes()
	if len(capabilities) != 2 || capabilities[0] != protocol.ProbeTypeHTTP || capabilities[1] != protocol.ProbeTypeTCPConnect {
		t.Fatalf("capabilities = %v", capabilities)
	}
	if _, err := executor.Execute(context.Background(), validHTTPExecutorJob("https://example.test", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), validTCPExecutorJob("example.test", 443)); err != nil {
		t.Fatal(err)
	}
	icmp := validTCPExecutorJob("example.test", 443)
	icmp.ProbeType = protocol.ProbeTypeICMPPing
	icmp.Config = protocol.ProbeConfig{ICMPPing: &protocol.ICMPPingConfig{Target: "example.test", Count: 1}}
	if _, err := executor.Execute(context.Background(), icmp); !errors.Is(err, ErrUnsupportedProbeType) {
		t.Fatalf("ICMP error = %v", err)
	}
	if httpCalls.Load() != 1 || tcpCalls.Load() != 1 {
		t.Fatalf("dispatch calls: HTTP=%d TCP=%d", httpCalls.Load(), tcpCalls.Load())
	}
}

func TestProbeExecutorConditionallyDispatchesICMP(t *testing.T) {
	var icmpCalls atomic.Int32
	executor := &ProbeExecutor{
		http: UnsupportedExecutor{},
		tcp:  UnsupportedExecutor{},
		icmp: fakeExecutor{
			capabilities: []protocol.ProbeType{protocol.ProbeTypeICMPPing},
			execute: func(context.Context, protocol.Job) (Execution, error) {
				icmpCalls.Add(1)
				return Execution{Success: true, Result: protocol.ProbeResult{ICMPPing: &protocol.ICMPPingResult{Sent: 1, Received: 1}}}, nil
			},
		},
	}
	capabilities := executor.SupportedProbeTypes()
	if len(capabilities) != 3 || capabilities[0] != protocol.ProbeTypeHTTP || capabilities[1] != protocol.ProbeTypeTCPConnect || capabilities[2] != protocol.ProbeTypeICMPPing {
		t.Fatalf("capabilities = %v", capabilities)
	}
	job := validICMPExecutorJob("127.0.0.1", 1)
	if _, err := executor.Execute(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if icmpCalls.Load() != 1 {
		t.Fatalf("ICMP calls = %d", icmpCalls.Load())
	}
}

func TestTCPExecutorWorkerIntegration(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- struct{}{}
			_ = connection.Close()
		}
	}()
	host, port := tcpAddressParts(t, listener.Addr())
	job := validTCPExecutorJob(host, port)
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
			if len(claim.SupportedProbeTypes) != 1 || claim.SupportedProbeTypes[0] != protocol.ProbeTypeTCPConnect {
				t.Errorf("capabilities = %v", claim.SupportedProbeTypes)
			}
			if claims.Add(1) == 1 {
				writeTestJSON(t, w, job)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/agent/jobs/tcp-job/result":
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
				return
			}
			result, decodeErr := protocol.DecodeJobResult(body, protocol.ProbeTypeTCPConnect)
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

	runner := newJobTestRunner(t, api.URL, time.Second, time.Second, NewTCPExecutor())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runJobWorker(ctx)
		close(done)
	}()
	select {
	case result := <-resultReceived:
		if !result.Success || result.ResolvedIP != "127.0.0.1" || result.Result.TCPConnect == nil || result.LeaseToken != job.LeaseToken || result.Attempt != job.Attempt {
			t.Fatalf("submitted result = %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not submit TCP result")
	}
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("TCP target did not accept worker connection")
	}
	cancel()
	waitSignal(t, done, "TCP worker shutdown")
}

func validTCPExecutorJob(host string, port int) protocol.Job {
	now := time.Now().UnixMilli()
	return protocol.Job{
		ProtocolVersion: protocol.JobProtocolVersion,
		JobID:           "tcp-job",
		ProbeType:       protocol.ProbeTypeTCPConnect,
		Config:          protocol.ProbeConfig{TCPConnect: &protocol.TCPConnectConfig{Host: host, Port: port}},
		CreatedAt:       now,
		NotBefore:       now,
		ExpiresAt:       now + 60_000,
		TimeoutMS:       1_000,
		Attempt:         1,
		LeaseToken:      "lease-token",
		LeaseExpiresAt:  now + 30_000,
	}
}

func tcpAddressParts(t *testing.T, address net.Addr) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(address.String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func assertValidTCPExecution(t *testing.T, execution Execution) {
	t.Helper()
	if err := execution.Result.Validate(protocol.ProbeTypeTCPConnect); err != nil {
		t.Fatalf("result is invalid: %v", err)
	}
}

func TestNewUsesHTTPAndTCPExecutors(t *testing.T) {
	runner, err := New(Config{
		ServerURL: "https://example.test", AgentID: "agent", Token: "token",
		Interval: time.Second, Timeout: time.Second, StatePath: filepath.Join(t.TempDir(), "epoch"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := runner.executor.SupportedProbeTypes()
	if len(capabilities) < 2 || len(capabilities) > 3 || capabilities[0] != protocol.ProbeTypeHTTP || capabilities[1] != protocol.ProbeTypeTCPConnect {
		t.Fatalf("capabilities = %v", capabilities)
	}
	if len(capabilities) == 3 && capabilities[2] != protocol.ProbeTypeICMPPing {
		t.Fatalf("capabilities = %v", capabilities)
	}
	if _, ok := runner.executor.(*ProbeExecutor); !ok {
		t.Fatalf("production executor type = %T", runner.executor)
	}
}

func TestTCPConnectionRefusedUsesStableError(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port := tcpAddressParts(t, listener.Addr())
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	execution, err := NewTCPExecutor().Execute(context.Background(), validTCPExecutorJob(host, port))
	if err != nil || execution.Success || execution.ErrorCategory != "connection_refused" || execution.ErrorMessage != "TCP connection was refused" || execution.ResolvedIP != "127.0.0.1" {
		t.Fatalf("execution = %+v err=%v", execution, err)
	}
	assertValidTCPExecution(t, execution)
}
