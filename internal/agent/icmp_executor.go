package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"syscall"
	"time"

	"404-probe/internal/protocol"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const icmpNonceBytes = 16

var (
	errICMPUnsupportedFamily = errors.New("ICMP address family is unavailable")
	icmpPayloadPrefix        = []byte("404-probe-icmp-v1:")
)

type icmpFamily uint8

const (
	icmpFamilyIPv4 icmpFamily = 4
	icmpFamilyIPv6 icmpFamily = 6
)

type icmpPacketConn interface {
	ReadFrom([]byte) (int, net.Addr, error)
	WriteTo([]byte, net.Addr) (int, error)
	Close() error
	SetDeadline(time.Time) error
}

type icmpBackend interface {
	Supports(icmpFamily) bool
	Open(icmpFamily) (icmpPacketConn, bool, error)
}

type icmpResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type ICMPExecutor struct {
	backend  icmpBackend
	resolver icmpResolver
	random   func([]byte) (int, error)
	now      func() time.Time
}

func NewICMPExecutor() *ICMPExecutor {
	return &ICMPExecutor{
		backend:  newPlatformICMPBackend(),
		resolver: net.DefaultResolver,
		random:   rand.Read,
		now:      time.Now,
	}
}

func (e *ICMPExecutor) SupportedProbeTypes() []protocol.ProbeType {
	if e == nil || e.backend == nil || (!e.backend.Supports(icmpFamilyIPv4) && !e.backend.Supports(icmpFamilyIPv6)) {
		return nil
	}
	return []protocol.ProbeType{protocol.ProbeTypeICMPPing}
}

func (e *ICMPExecutor) Execute(ctx context.Context, job protocol.Job) (Execution, error) {
	if job.ProbeType != protocol.ProbeTypeICMPPing {
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	}
	if len(e.SupportedProbeTypes()) == 0 {
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	}
	if err := job.Config.Validate(protocol.ProbeTypeICMPPing); err != nil {
		return failedICMPExecution("invalid_config", "ICMP probe configuration is invalid", "", emptyICMPMeasurement(job)), nil
	}

	config := job.Config.ICMPPing
	target, err := e.resolveTarget(ctx, config.Target)
	if err != nil {
		category, message := classifyICMPSetupError(ctx, err)
		return failedICMPExecution(category, message, "", emptyICMPMeasurement(job)), nil
	}
	resolvedIP := target.Unmap().String()
	if err := ctx.Err(); err != nil {
		category, message := classifyICMPSetupError(ctx, err)
		return failedICMPExecution(category, message, resolvedIP, emptyICMPMeasurement(job)), nil
	}
	family := familyForIP(target)
	connection, datagram, err := e.backend.Open(family)
	if err != nil {
		category, message := classifyICMPSocketError(ctx, err)
		return failedICMPExecution(category, message, resolvedIP, emptyICMPMeasurement(job)), nil
	}
	defer connection.Close()

	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return failedICMPExecution("network_error", "ICMP socket deadline could not be set", resolvedIP, emptyICMPMeasurement(job)), nil
		}
	}
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-watchDone:
		}
	}()

	nonce := make([]byte, icmpNonceBytes)
	random := e.random
	if random == nil {
		random = rand.Read
	}
	if read, err := random(nonce); err != nil || read != len(nonce) {
		if ctx.Err() != nil {
			category, message := classifyICMPSetupError(ctx, ctx.Err())
			return failedICMPExecution(category, message, resolvedIP, emptyICMPMeasurement(job)), nil
		}
		return failedICMPExecution("network_error", "ICMP probe nonce could not be created", resolvedIP, emptyICMPMeasurement(job)), nil
	}
	payload := append(append([]byte(nil), icmpPayloadPrefix...), nonce...)
	id := int(binary.BigEndian.Uint16(nonce[:2]))
	clock := e.now
	if clock == nil {
		clock = time.Now
	}

	measurement := emptyICMPMeasurement(job)
	sentAt := make([]time.Time, config.Count+1)
	destination := icmpDestination(target, datagram)
	for sequence := 1; sequence <= config.Count; sequence++ {
		packet, marshalErr := (&icmp.Message{
			Type: echoRequestType(family),
			Code: 0,
			Body: &icmp.Echo{ID: id, Seq: sequence, Data: payload},
		}).Marshal(nil)
		if marshalErr != nil {
			return failedICMPExecution("network_error", "ICMP request could not be encoded", resolvedIP, measurement), nil
		}
		sentAt[sequence] = clock()
		if _, writeErr := connection.WriteTo(packet, destination); writeErr != nil {
			category, message := classifyICMPSocketError(ctx, writeErr)
			return failedICMPExecution(category, message, resolvedIP, measurement), nil
		}
	}

	latencies := make([]float64, 0, config.Count)
	received := make([]bool, config.Count+1)
	buffer := make([]byte, 1500)
	for len(latencies) < config.Count {
		read, source, readErr := connection.ReadFrom(buffer)
		if readErr != nil {
			measurement = icmpMeasurement(config.Count, latencies)
			if errors.Is(ctx.Err(), context.Canceled) {
				return failedICMPExecution("canceled", "ICMP probe was canceled", resolvedIP, measurement), nil
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || isTimeoutError(readErr) {
				if len(latencies) > 0 {
					return successfulICMPExecution(resolvedIP, measurement), nil
				}
				return failedICMPExecution("timeout", "ICMP probe timed out", resolvedIP, measurement), nil
			}
			category, message := classifyICMPSocketError(ctx, readErr)
			return failedICMPExecution(category, message, resolvedIP, measurement), nil
		}

		sourceIP, ok := addressIP(source)
		if !ok || comparableIP(sourceIP).Compare(comparableIP(target)) != 0 {
			continue
		}
		message, parseErr := icmp.ParseMessage(icmpProtocol(family), buffer[:read])
		if parseErr != nil || message.Type != echoReplyType(family) || message.Code != 0 {
			continue
		}
		echo, ok := message.Body.(*icmp.Echo)
		if !ok || echo.Seq < 1 || echo.Seq > config.Count || received[echo.Seq] || !bytes.Equal(echo.Data, payload) {
			continue
		}
		received[echo.Seq] = true
		latency := clock().Sub(sentAt[echo.Seq])
		if latency < 0 {
			latency = 0
		}
		latencies = append(latencies, float64(latency)/float64(time.Millisecond))
	}

	measurement = icmpMeasurement(config.Count, latencies)
	if errors.Is(ctx.Err(), context.Canceled) {
		return failedICMPExecution("canceled", "ICMP probe was canceled", resolvedIP, measurement), nil
	}
	return successfulICMPExecution(resolvedIP, measurement), nil
}

func (e *ICMPExecutor) resolveTarget(ctx context.Context, target string) (netip.Addr, error) {
	if address, err := netip.ParseAddr(target); err == nil {
		address = address.Unmap()
		if !e.backend.Supports(familyForIP(address)) {
			return netip.Addr{}, errICMPUnsupportedFamily
		}
		return address, nil
	}
	resolver := e.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", target)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, address := range addresses {
		address = address.Unmap()
		if e.backend.Supports(familyForIP(address)) {
			return address, nil
		}
	}
	return netip.Addr{}, errICMPUnsupportedFamily
}

func emptyICMPMeasurement(job protocol.Job) protocol.ICMPPingResult {
	sent := 1
	if job.Config.ICMPPing != nil && job.Config.ICMPPing.Count >= 1 && job.Config.ICMPPing.Count <= 10 {
		sent = job.Config.ICMPPing.Count
	}
	return icmpMeasurement(sent, nil)
}

func icmpMeasurement(sent int, latencies []float64) protocol.ICMPPingResult {
	result := protocol.ICMPPingResult{
		Sent:              sent,
		Received:          len(latencies),
		PacketLossPercent: 100 * float64(sent-len(latencies)) / float64(sent),
	}
	if len(latencies) == 0 {
		return result
	}
	result.LatencyMinMS = latencies[0]
	result.LatencyMaxMS = latencies[0]
	for _, latency := range latencies {
		result.LatencyAvgMS += latency
		if latency < result.LatencyMinMS {
			result.LatencyMinMS = latency
		}
		if latency > result.LatencyMaxMS {
			result.LatencyMaxMS = latency
		}
	}
	result.LatencyAvgMS /= float64(len(latencies))
	return result
}

func successfulICMPExecution(resolvedIP string, measurement protocol.ICMPPingResult) Execution {
	return Execution{
		Success:    measurement.Received > 0,
		ResolvedIP: resolvedIP,
		Result:     protocol.ProbeResult{ICMPPing: &measurement},
	}
}

func failedICMPExecution(category, message, resolvedIP string, measurement protocol.ICMPPingResult) Execution {
	return Execution{
		Success:       false,
		ResolvedIP:    resolvedIP,
		ErrorCategory: category,
		ErrorMessage:  message,
		Result:        protocol.ProbeResult{ICMPPing: &measurement},
	}
}

func classifyICMPSetupError(ctx context.Context, err error) (string, string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "ICMP probe timed out"
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "canceled", "ICMP probe was canceled"
	}
	if errors.Is(err, errICMPUnsupportedFamily) {
		return "unsupported_address_family", "ICMP target address family is unavailable"
	}
	return "dns_error", "ICMP probe hostname resolution failed"
}

func classifyICMPSocketError(ctx context.Context, err error) (string, string) {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "canceled", "ICMP probe was canceled"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || isTimeoutError(err) {
		return "timeout", "ICMP probe timed out"
	}
	if errors.Is(err, errICMPUnsupportedFamily) {
		return "unsupported_address_family", "ICMP target address family is unavailable"
	}
	if isPermissionError(err) {
		return "permission_denied", "ICMP socket permission was denied"
	}
	return "network_error", "ICMP network operation failed"
}

func isTimeoutError(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded) || func() bool {
		var networkError net.Error
		return errors.As(err, &networkError) && networkError.Timeout()
	}()
}

func isPermissionError(err error) bool {
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return true
	}
	const wsaAccessDenied syscall.Errno = 10013
	var errno syscall.Errno
	return runtime.GOOS == "windows" && errors.As(err, &errno) && errno == wsaAccessDenied
}

func familyForIP(address netip.Addr) icmpFamily {
	if address.Unmap().Is4() {
		return icmpFamilyIPv4
	}
	return icmpFamilyIPv6
}

func comparableIP(address netip.Addr) netip.Addr {
	return address.Unmap().WithZone("")
}

func icmpProtocol(family icmpFamily) int {
	if family == icmpFamilyIPv4 {
		return 1
	}
	return 58
}

func echoRequestType(family icmpFamily) icmp.Type {
	if family == icmpFamilyIPv4 {
		return ipv4.ICMPTypeEcho
	}
	return ipv6.ICMPTypeEchoRequest
}

func echoReplyType(family icmpFamily) icmp.Type {
	if family == icmpFamilyIPv4 {
		return ipv4.ICMPTypeEchoReply
	}
	return ipv6.ICMPTypeEchoReply
}

func icmpDestination(address netip.Addr, datagram bool) net.Addr {
	zone := address.Zone()
	ip := net.IP(address.AsSlice())
	if datagram {
		return &net.UDPAddr{IP: ip, Zone: zone}
	}
	return &net.IPAddr{IP: ip, Zone: zone}
}

func addressIP(address net.Addr) (netip.Addr, bool) {
	var ip net.IP
	switch value := address.(type) {
	case *net.IPAddr:
		ip = value.IP
	case *net.UDPAddr:
		ip = value.IP
	default:
		return netip.Addr{}, false
	}
	parsed, ok := netip.AddrFromSlice(ip)
	return parsed.Unmap(), ok
}

type icmpSocketMode struct {
	network  string
	address  string
	datagram bool
}

type icmpListenFunc func(string, string) (icmpPacketConn, error)

type socketICMPBackend struct {
	modes  map[icmpFamily]icmpSocketMode
	listen icmpListenFunc
}

func detectICMPSocketBackend(candidates map[icmpFamily][]icmpSocketMode, listen icmpListenFunc) *socketICMPBackend {
	backend := &socketICMPBackend{modes: make(map[icmpFamily]icmpSocketMode), listen: listen}
	for family, familyCandidates := range candidates {
		for _, candidate := range familyCandidates {
			connection, err := listen(candidate.network, candidate.address)
			if err != nil {
				continue
			}
			_ = connection.Close()
			backend.modes[family] = candidate
			break
		}
	}
	return backend
}

func (b *socketICMPBackend) Supports(family icmpFamily) bool {
	if b == nil {
		return false
	}
	_, ok := b.modes[family]
	return ok
}

func (b *socketICMPBackend) Open(family icmpFamily) (icmpPacketConn, bool, error) {
	if b == nil || !b.Supports(family) {
		return nil, false, errICMPUnsupportedFamily
	}
	mode := b.modes[family]
	connection, err := b.listen(mode.network, mode.address)
	return connection, mode.datagram, err
}

func listenICMPPacket(network, address string) (icmpPacketConn, error) {
	return icmp.ListenPacket(network, address)
}
