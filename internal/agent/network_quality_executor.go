package agent

import (
	"404-probe/internal/protocol"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"golang.org/x/net/icmp"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"
)

type qualityProbeResult struct {
	outcome, ip    string
	latency        *float64
	sent, received int
}
type qualityProbeFunc func(context.Context, protocol.QualityTarget) qualityProbeResult

func qualityCategory(category string) string {
	switch category {
	case "connection_refused":
		return "refused"
	case "unsupported_address_family":
		return "unsupported"
	case "dns_error", "timeout", "canceled", "permission_denied", "invalid_config", "unsupported", "refused":
		return category
	default:
		return "network_error"
	}
}
func qualityIPFamily(ip, family string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && (a.Unmap().Is4() == (family == "ipv4"))
}
func (e *TCPExecutor) qualityProbe(ctx context.Context, t protocol.QualityTarget) qualityProbeResult {
	network := "tcp4"
	if t.Family == "ipv6" {
		network = "tcp6"
	}
	dial := e.dialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	start := time.Now()
	connection, err := dial(ctx, network, net.JoinHostPort(t.Host, strconv.Itoa(t.Port)))
	if err != nil {
		category, _ := classifyTCPProbeError(ctx, err)
		return qualityProbeResult{outcome: qualityCategory(category)}
	}
	ip := remoteIP(connection.RemoteAddr())
	_ = connection.Close()
	if !qualityIPFamily(ip, t.Family) {
		return qualityProbeResult{outcome: "network_error"}
	}
	latency := float64(time.Since(start)) / float64(time.Millisecond)
	return qualityProbeResult{outcome: "success", ip: ip, latency: &latency}
}
func (e *ICMPExecutor) qualityProbe(ctx context.Context, t protocol.QualityTarget) qualityProbeResult {
	out := qualityProbeResult{outcome: "unsupported"}
	family := icmpFamilyIPv4
	network := "ip4"
	if t.Family == "ipv6" {
		family = icmpFamilyIPv6
		network = "ip6"
	}
	if e == nil || e.backend == nil || !e.backend.Supports(family) {
		return out
	}
	address, err := netip.ParseAddr(t.Host)
	if err != nil {
		resolver := e.resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		ips, lookupErr := resolver.LookupNetIP(ctx, network, t.Host)
		if lookupErr != nil {
			category, _ := classifyICMPSetupError(ctx, lookupErr)
			out.outcome = qualityCategory(category)
			return out
		}
		for _, ip := range ips {
			if familyForIP(ip) == family {
				address = ip.Unmap()
				break
			}
		}
	}
	if !address.IsValid() || familyForIP(address) != family {
		return out
	}
	out.ip = address.Unmap().String()
	if ctx.Err() != nil {
		category, _ := classifyICMPSetupError(ctx, ctx.Err())
		out.outcome = qualityCategory(category)
		return out
	}
	connection, datagram, err := e.backend.Open(family)
	if err != nil {
		category, _ := classifyICMPSocketError(ctx, err)
		out.outcome = qualityCategory(category)
		return out
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if connection.SetDeadline(deadline) != nil {
			out.outcome = "network_error"
			return out
		}
	}
	// Wait for the cancellation watcher before returning: no orphan socket worker.
	watchStop := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-watchStop:
		}
	}()
	defer func() { close(watchStop); <-watchExited }()
	nonce := make([]byte, icmpNonceBytes)
	random := e.random
	if random == nil {
		random = rand.Read
	}
	if n, err := random(nonce); err != nil || n != len(nonce) {
		out.outcome = "network_error"
		return out
	}
	payload := append(append([]byte(nil), icmpPayloadPrefix...), nonce...)
	id := int(binary.BigEndian.Uint16(nonce[:2]))
	packet, err := (&icmp.Message{Type: echoRequestType(family), Code: 0, Body: &icmp.Echo{ID: id, Seq: 1, Data: payload}}).Marshal(nil)
	if err != nil {
		out.outcome = "network_error"
		return out
	}
	clock := e.now
	if clock == nil {
		clock = time.Now
	}
	sentAt := clock()
	n, err := connection.WriteTo(packet, icmpDestination(address, datagram))
	if err == nil && n != len(packet) {
		err = io.ErrShortWrite
	}
	if err != nil {
		category, _ := classifyICMPSocketError(ctx, err)
		out.outcome = qualityCategory(category)
		return out
	}
	out.sent = 1
	buffer := make([]byte, 1500)
	for {
		n, source, err := connection.ReadFrom(buffer)
		if err != nil {
			category, _ := classifyICMPSocketError(ctx, err)
			out.outcome = qualityCategory(category)
			return out
		}
		ip, ok := addressIP(source)
		if !ok || comparableIP(ip) != comparableIP(address) {
			continue
		}
		message, err := icmp.ParseMessage(icmpProtocol(family), buffer[:n])
		if err != nil || message.Type != echoReplyType(family) || message.Code != 0 {
			continue
		}
		echo, ok := message.Body.(*icmp.Echo)
		if !ok || echo.Seq != 1 || (!datagram && echo.ID != id) || !bytes.Equal(echo.Data, payload) {
			continue
		}
		if ctx.Err() != nil {
			category, _ := classifyICMPSocketError(ctx, ctx.Err())
			out.outcome = qualityCategory(category)
			return out
		}
		latency := clock().Sub(sentAt)
		if latency < 0 {
			out.outcome = "network_error"
			return out
		}
		ms := float64(latency) / float64(time.Millisecond)
		out.outcome = "success"
		out.latency = &ms
		out.received = 1
		return out
	}
}
