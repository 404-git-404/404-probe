package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"404-probe/internal/protocol"
)

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

// TCPExecutor executes TCP connect probes. A successful probe establishes and
// immediately closes a connection; it does not exchange application data or
// negotiate TLS.
type TCPExecutor struct {
	dialContext dialContextFunc
}

func NewTCPExecutor() *TCPExecutor {
	dialer := &net.Dialer{}
	return &TCPExecutor{dialContext: dialer.DialContext}
}

func (*TCPExecutor) SupportedProbeTypes() []protocol.ProbeType {
	return []protocol.ProbeType{protocol.ProbeTypeTCPConnect}
}

func (e *TCPExecutor) Execute(ctx context.Context, job protocol.Job) (Execution, error) {
	if job.ProbeType != protocol.ProbeTypeTCPConnect {
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	}
	if err := job.Config.Validate(protocol.ProbeTypeTCPConnect); err != nil {
		return failedTCPExecution("invalid_config", "TCP probe configuration is invalid", 0, ""), nil
	}

	config := job.Config.TCPConnect
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	dial := e.dialContext
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}

	started := time.Now()
	connection, err := dial(ctx, "tcp", address)
	connectMS := float64(time.Since(started)) / float64(time.Millisecond)
	if err != nil {
		category, message := classifyTCPProbeError(ctx, err)
		return failedTCPExecution(category, message, connectMS, literalIP(config.Host)), nil
	}
	resolvedIP := remoteIP(connection.RemoteAddr())
	_ = connection.Close()
	return Execution{
		Success:    true,
		ResolvedIP: resolvedIP,
		Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{
			ConnectMS: connectMS,
		}},
	}, nil
}

func failedTCPExecution(category, message string, connectMS float64, resolvedIP string) Execution {
	return Execution{
		Success:       false,
		ResolvedIP:    resolvedIP,
		ErrorCategory: category,
		ErrorMessage:  message,
		Result: protocol.ProbeResult{TCPConnect: &protocol.TCPConnectResult{
			ConnectMS: connectMS,
		}},
	}
}

func classifyTCPProbeError(ctx context.Context, err error) (string, string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "TCP probe timed out"
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "canceled", "TCP probe was canceled"
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return "dns_error", "TCP probe hostname resolution failed"
	}
	if isConnectionRefused(err) {
		return "connection_refused", "TCP connection was refused"
	}
	return "network_error", "TCP connection failed"
}

func isConnectionRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	// Windows net errors retain the Winsock value instead of translating it
	// to syscall.ECONNREFUSED, whose Windows value is synthesized by Go.
	const wsaConnectionRefused syscall.Errno = 10061
	var errno syscall.Errno
	return runtime.GOOS == "windows" && errors.As(err, &errno) && errno == wsaConnectionRefused
}

func literalIP(host string) string {
	address, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	return address.Unmap().String()
}

func remoteIP(address net.Addr) string {
	if address == nil {
		return ""
	}
	if tcpAddress, ok := address.(*net.TCPAddr); ok && tcpAddress.IP != nil {
		return tcpAddress.AddrPort().Addr().Unmap().String()
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return ""
	}
	return literalIP(host)
}

// ProbeExecutor dispatches the production probe types implemented by the
// agent. ICMP is advertised only when its runtime socket backend is available.
type ProbeExecutor struct {
	http Executor
	tcp  Executor
	icmp Executor
}

func NewProbeExecutor() *ProbeExecutor {
	return &ProbeExecutor{http: NewHTTPExecutor(), tcp: NewTCPExecutor(), icmp: NewICMPExecutor()}
}

func (e *ProbeExecutor) SupportedProbeTypes() []protocol.ProbeType {
	capabilities := []protocol.ProbeType{protocol.ProbeTypeHTTP, protocol.ProbeTypeTCPConnect}
	if e != nil && e.icmp != nil && supportsProbeType(e.icmp, protocol.ProbeTypeICMPPing) {
		capabilities = append(capabilities, protocol.ProbeTypeICMPPing)
	}
	return capabilities
}

func (e *ProbeExecutor) Execute(ctx context.Context, job protocol.Job) (Execution, error) {
	switch job.ProbeType {
	case protocol.ProbeTypeHTTP:
		return e.http.Execute(ctx, job)
	case protocol.ProbeTypeTCPConnect:
		return e.tcp.Execute(ctx, job)
	case protocol.ProbeTypeICMPPing:
		if e.icmp != nil && supportsProbeType(e.icmp, protocol.ProbeTypeICMPPing) {
			return e.icmp.Execute(ctx, job)
		}
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	default:
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	}
}

func supportsProbeType(executor Executor, probeType protocol.ProbeType) bool {
	for _, supported := range executor.SupportedProbeTypes() {
		if supported == probeType {
			return true
		}
	}
	return false
}
