package agent

import (
	"404-probe/internal/protocol"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"golang.org/x/net/icmp"
	"net"
	"net/netip"
	"strconv"
	"time"
)

func qualityMS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func qualityResolve(ctx context.Context, resolver icmpResolver, t protocol.QualityTarget, d *protocol.QualityDetails) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(t.Host); err == nil {
		d.DNSOutcome = "literal"
		if qualityIPFamily(ip.String(), t.Family) {
			return ip.Unmap(), nil
		}
		d.DNSOutcome = "no_address"
		return netip.Addr{}, errICMPUnsupportedFamily
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	network := "ip4"
	if t.Family == "ipv6" {
		network = "ip6"
	}
	start := time.Now()
	ips, err := resolver.LookupNetIP(ctx, network, t.Host)
	d.DNSMS = qualityMS(time.Since(start))
	if err != nil {
		d.DNSOutcome = "dns_error"
		if isDNSNotFound(err) && ctx.Err() == nil && t.Source == "user" {
			// Only user-entered targets, which only a negotiated Server accepts, get the new value.
			d.DNSOutcome = "not_found"
		} else if ctx.Err() != nil {
			d.DNSOutcome = "canceled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				d.DNSOutcome = "dns_timeout"
			}
		} else {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				d.DNSOutcome = "dns_timeout"
			}
		}
		return netip.Addr{}, err
	}
	for _, ip := range ips {
		if qualityIPFamily(ip.String(), t.Family) {
			d.DNSOutcome = "success"
			return ip.Unmap(), nil
		}
	}
	d.DNSOutcome = "no_address"
	return netip.Addr{}, errICMPUnsupportedFamily
}
func qualityDNSFailure(d *protocol.QualityDetails) string {
	d.ErrorStage = "dns"
	d.ErrorCode = d.DNSOutcome
	d.ProbeComplete = d.DNSOutcome != "canceled"
	if d.DNSOutcome == "canceled" {
		return "canceled"
	}
	return "dns_error"
}

func qualityTCPConnectOutcome(ctx context.Context, err error) string {
	category, _ := classifyTCPProbeError(ctx, err)
	// The socket poller can report its deadline before ctx.Err is updated.
	// Normalize that timeout only for staged quality; keep legacy Job behavior.
	if category == "network_error" {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return "timeout"
		}
	}
	return qualityCategory(category)
}
func (e *TCPExecutor) qualityStagedProbe(ctx context.Context, t protocol.QualityTarget) qualityProbeResult {
	d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics}
	out := qualityProbeResult{}
	ip, err := qualityResolve(ctx, e.qualityResolver, t, &d)
	if err != nil {
		out.outcome = qualityDNSFailure(&d)
		out.details = d.Encode()
		return out
	}
	if !qualityAddressPolicy(ip) {
		out.outcome = "unsafe_address"
		d.ErrorStage = "local"
		d.ErrorCode = "unsafe_address"
		out.details = d.Encode()
		return out
	}
	out.ip = ip.String()
	if ctx.Err() != nil {
		out.outcome = "canceled"
		d.ErrorStage = "local"
		d.ErrorCode = "canceled"
		out.details = d.Encode()
		return out
	}
	dial := e.qualityDial
	if dial == nil {
		dial = qualityDefaultDial()
	}
	network := "tcp4"
	if t.Family == "ipv6" {
		network = "tcp6"
	}
	start := time.Now()
	connection, err := dial(ctx, network, net.JoinHostPort(ip.String(), strconv.Itoa(t.Port)))
	ms := qualityMS(time.Since(start))
	d.ConnectAttempted = true
	d.ConnectMS = &ms
	if err != nil {
		out.outcome = qualityTCPConnectOutcome(ctx, err)
		d.ErrorStage = "connect"
		d.ErrorCode = out.outcome
		d.ConnectComplete = out.outcome == "timeout" || out.outcome == "refused" || out.outcome == "network_error"
		d.ProbeComplete = d.ConnectComplete
	} else {
		d.ConnectComplete = true
		d.ProbeComplete = true
		out.outcome = "success"
		out.latency = &ms
		_ = connection.Close()
	}
	out.details = d.Encode()
	return out
}

// Error quotes are attributable only when destination, sequence, identity and
// the full unpredictable payload survived the quote. Short quotes are ignored.
func qualityQuotedEcho(m *icmp.Message, f icmpFamily, ip netip.Addr, id int, datagram bool, payload []byte) int {
	var b []byte
	switch body := m.Body.(type) {
	case *icmp.DstUnreach:
		b = body.Data
	case *icmp.TimeExceeded:
		b = body.Data
	case *icmp.PacketTooBig:
		b = body.Data
	case *icmp.ParamProb:
		b = body.Data
	default:
		return 0
	}
	offset := 0
	if f == icmpFamilyIPv4 {
		if len(b) < 20 || b[0]>>4 != 4 || b[9] != 1 {
			return 0
		}
		offset = int(b[0]&15) * 4
		if offset < 20 || offset > len(b) {
			return 0
		}
		dst, ok := netip.AddrFromSlice(b[16:20])
		if !ok || dst.Unmap() != ip.Unmap() {
			return 0
		}
	} else {
		if len(b) < 40 || b[0]>>4 != 6 || b[6] != 58 {
			return 0
		}
		offset = 40
		dst, ok := netip.AddrFromSlice(b[24:40])
		if !ok || dst != ip {
			return 0
		}
	}
	original, err := icmp.ParseMessage(icmpProtocol(f), b[offset:])
	if err != nil || original.Type != echoRequestType(f) || original.Code != 0 {
		return 0
	}
	echo, ok := original.Body.(*icmp.Echo)
	if !ok || echo.Seq < 1 || echo.Seq > 3 || !datagram && echo.ID != id || !bytes.Equal(echo.Data, payload) {
		return 0
	}
	return echo.Seq
}
func (e *ICMPExecutor) qualityStagedProbe(ctx context.Context, t protocol.QualityTarget) (out qualityProbeResult) {
	d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, Requested: 3, WindowMS: 2000, Echoes: []protocol.QualityEcho{{Seq: 1, Status: "not_sent"}, {Seq: 2, Status: "not_sent"}, {Seq: 3, Status: "not_sent"}}}
	defer func() { out.details = d.Encode() }()
	start := time.Now()
	fail := func(stage, code string) {
		d.ErrorStage = stage
		d.ErrorCode = code
		out.outcome = code
		if code == "short_write" || code == "clock_error" {
			out.outcome = "network_error"
		}
		for i := range d.Echoes {
			p := &d.Echoes[i]
			if p.Status == "pending" {
				p.Status = "local_error"
				if code == "canceled" {
					p.Status = "canceled"
				}
			}
		}
	}
	if e == nil || e.backend == nil {
		d.DNSOutcome = "literal"
		fail("socket", "unsupported")
		return out
	}
	ip, err := qualityResolve(ctx, e.resolver, t, &d)
	if err != nil {
		out.outcome = qualityDNSFailure(&d)
		d.ProbeComplete = false
		return out
	}
	if !qualityAddressPolicy(ip) {
		fail("local", "unsafe_address")
		return out
	}
	out.ip = ip.String()
	family := familyForIP(ip)
	if !e.backend.Supports(family) {
		code := "unsupported"
		if reasons, ok := e.backend.(interface{ Failure(icmpFamily) error }); ok {
			if reason := reasons.Failure(family); reason != nil {
				category, _ := classifyICMPSocketError(ctx, reason)
				code = qualityCategory(category)
			}
		}
		fail("socket", code)
		return out
	}
	c, datagram, err := e.backend.Open(family)
	if err != nil {
		category, _ := classifyICMPSocketError(ctx, err)
		fail("socket", qualityCategory(category))
		return out
	}
	defer c.Close()
	stopped := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			_ = c.SetDeadline(time.Now())
		case <-stopped:
		}
	}()
	defer func() { close(stopped); <-exited }()
	nonce := make([]byte, icmpNonceBytes)
	random := e.random
	if random == nil {
		random = rand.Read
	}
	if n, err := random(nonce); err != nil || n != len(nonce) {
		fail("local", "network_error")
		return out
	}
	payload := append(append([]byte(nil), icmpPayloadPrefix...), nonce...)
	id := int(binary.BigEndian.Uint16(nonce[:2]))
	var sentAt [3]time.Time
	sentCount := 0
	nextSend := time.Now()
	buffer := make([]byte, 1500)
	for {
		now := time.Now()
		if ctx.Err() != nil {
			fail("local", "canceled")
			return out
		}
		for i := 0; i < sentCount; i++ {
			if d.Echoes[i].Status == "pending" && !now.Before(sentAt[i].Add(2*time.Second)) {
				d.Echoes[i].Status = "timeout"
			}
		}
		if sentCount < 3 && !now.Before(nextSend) {
			// SetDeadline also affects writes. The prior read deadline may be the
			// 200ms scheduling wakeup, so restore the bounded write window first.
			writeDeadline := now.Add(2 * time.Second)
			if limit, ok := ctx.Deadline(); ok && limit.Before(writeDeadline) {
				writeDeadline = limit
			}
			if err := c.SetDeadline(writeDeadline); err != nil {
				fail("send", "network_error")
				return out
			}
			packet, err := (&icmp.Message{Type: echoRequestType(family), Body: &icmp.Echo{ID: id, Seq: sentCount + 1, Data: payload}}).Marshal(nil)
			if err != nil {
				fail("local", "network_error")
				return out
			}
			sentAt[sentCount] = time.Now()
			n, err := c.WriteTo(packet, icmpDestination(ip, datagram))
			if err != nil {
				category, _ := classifyICMPSocketError(ctx, err)
				fail("send", qualityCategory(category))
				return out
			}
			if n != len(packet) {
				fail("send", "short_write")
				return out
			}
			// A late wake or write must not make the following packet catch up
			// to the round's original schedule. Keep actual sends spaced apart.
			nextSend = time.Now().Add(200 * time.Millisecond)
			offset := qualityMS(sentAt[sentCount].Sub(start))
			d.Echoes[sentCount].SentMS = &offset
			d.Echoes[sentCount].Status = "pending"
			sentCount++
			out.sent++
			continue
		}
		pending := false
		deadline := now.Add(2 * time.Second)
		if sentCount < 3 {
			deadline = nextSend
		}
		for i := 0; i < sentCount; i++ {
			if d.Echoes[i].Status == "pending" {
				pending = true
				if sentAt[i].Add(2 * time.Second).Before(deadline) {
					deadline = sentAt[i].Add(2 * time.Second)
				}
			}
		}
		if sentCount == 3 && !pending {
			d.WindowComplete = true
			d.ProbeComplete = true
			out.outcome = "no_echo_reply"
			if out.received > 0 {
				out.outcome = "partial"
			}
			if out.received == 3 {
				out.outcome = "success"
				sum := 0.0
				for _, p := range d.Echoes {
					sum += *p.RTTMS
				}
				mean := sum / 3
				out.latency = &mean
			}
			return out
		}
		if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
			deadline = limit
		}
		if err := c.SetDeadline(deadline); err != nil {
			fail("receive", "network_error")
			return out
		}
		n, source, err := c.ReadFrom(buffer)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && ctx.Err() == nil {
				continue
			}
			category, _ := classifyICMPSocketError(ctx, err)
			fail("receive", qualityCategory(category))
			return out
		}
		m, err := icmp.ParseMessage(icmpProtocol(family), buffer[:n])
		if err != nil {
			continue
		}
		seq := 0
		reply := false
		if m.Type == echoReplyType(family) && m.Code == 0 {
			sourceIP, ok := addressIP(source)
			echo, echoOK := m.Body.(*icmp.Echo)
			if !ok || comparableIP(sourceIP) != comparableIP(ip) || !echoOK || echo.Seq < 1 || echo.Seq > 3 || !datagram && echo.ID != id || !bytes.Equal(echo.Data, payload) {
				continue
			}
			seq = echo.Seq
			reply = true
		} else {
			seq = qualityQuotedEcho(m, family, ip, id, datagram, payload)
		}
		if seq < 1 || seq > sentCount || d.Echoes[seq-1].Status != "pending" || !time.Now().Before(sentAt[seq-1].Add(2*time.Second)) {
			continue
		}
		if ctx.Err() != nil {
			fail("local", "canceled")
			return out
		}
		if reply {
			ms := qualityMS(time.Since(sentAt[seq-1]))
			d.Echoes[seq-1].RTTMS = &ms
			d.Echoes[seq-1].Status = "reply"
			out.received++
		} else {
			d.Echoes[seq-1].Status = "icmp_error"
			d.ErrorStage = "receive"
			d.ErrorCode = "icmp_error"
		}
	}
}
