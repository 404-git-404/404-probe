// Package egress observes the Agent process's direct network namespace path.
// It never consults Server peers, local NIC addresses, or the browser.
package egress

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"

	"404-probe/internal/protocol"
)

const Timeout = 6 * time.Second

type Checker struct {
	endpoint  [2]string
	resolver  func(context.Context, string, string) ([]net.IP, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig *tls.Config
	// Only package tests can override destination validation for controlled TLS.
	allowDestination func(string, string) bool
	noRoute          func(string) bool
}

func NewChecker() *Checker {
	d := &net.Dialer{Timeout: Timeout}
	r := &net.Resolver{PreferGo: true}
	return &Checker{endpoint: [2]string{"https://api.ipify.org?format=json", "https://api6.ipify.org?format=json"}, resolver: r.LookupIP, dial: d.DialContext, allowDestination: protocol.PublicDestinationIP, noRoute: noUsableRoute}
}

func (c *Checker) Check(ctx context.Context, family string) protocol.EgressObservation {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	index, network := 0, "tcp4"
	if family == "ipv6" {
		index, network = 1, "tcp6"
	}
	result := protocol.UnknownEgress(family, "network")
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSClientConfig: c.tlsConfig, TLSHandshakeTimeout: Timeout, ResponseHeaderTimeout: Timeout}
	transport.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := c.resolver(ctx, map[string]string{"tcp4": "ip4", "tcp6": "ip6"}[network], host)
		if err != nil {
			return nil, err
		}
		var last error
		for i, ip := range ips {
			if i >= 4 {
				break
			}
			text := ip.String()
			if !c.allowDestination(text, family) {
				continue
			}
			conn, err := c.dial(ctx, network, net.JoinHostPort(text, port))
			if err != nil {
				last = err
				continue
			}
			peer, err := netip.ParseAddrPort(conn.RemoteAddr().String())
			if err != nil || peer.Addr().Is4In6() || (family == "ipv4") != peer.Addr().Is4() {
				conn.Close()
				return nil, errors.New("socket family mismatch")
			}
			return conn, nil
		}
		if last == nil {
			last = errors.New("no native public destination")
		}
		return nil, last
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint[index], nil)
	if err == nil {
		req.Header.Set("Accept", "application/json")
		var resp *http.Response
		resp, err = client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			result.Reason = "http_status"
			if resp.StatusCode == http.StatusOK {
				result.Reason = "invalid_response"
				data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1025))
				if readErr != nil {
					err = readErr
				}
				var body struct {
					IP string `json:"ip"`
				}
				if readErr == nil && len(data) <= 1024 && json.Unmarshal(data, &body) == nil && protocol.PublicEgressIP(body.IP, family) {
					result.Status, result.IP, result.Reason = "available", body.IP, "verified"
				}
			}
		}
	}
	if err != nil {
		var dns *net.DNSError
		var tlsErr *tls.CertificateVerificationError
		var netErr net.Error
		switch {
		case errors.As(err, &dns):
			result.Reason = "dns"
		case errors.As(err, &tlsErr):
			result.Reason = "tls"
		case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
			result.Reason = "timeout"
		case errors.Is(err, syscall.EAFNOSUPPORT):
			result.Status, result.Reason = "unavailable", "family_unavailable"
		case errors.Is(err, syscall.ENETUNREACH) && c.noRoute(family):
			result.Status, result.Reason = "unavailable", "no_route"
		}
	}
	result.CheckedAt = time.Now().UnixMilli()
	ttl := protocol.EgressFailureTTL
	if result.Status == "available" {
		ttl = protocol.EgressSuccessTTL
		result.LastSuccess = &protocol.EgressSuccess{IP: result.IP, CheckedAt: result.CheckedAt}
	}
	result.ValidUntil = result.CheckedAt + ttl.Milliseconds()
	return result
}

// Linux route tables are namespace-local. Missing/unreadable/ambiguous data is
// not proof. Combined with a forced socket ENETUNREACH, absence of ANY up,
// non-reject route (apart from local/link-local/multicast) is conservative proof.
func noUsableRoute(family string) bool {
	file := "/proc/net/route"
	if family == "ipv6" {
		file = "/proc/net/ipv6_route"
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	return routeTableEmpty(string(data), family)
}

func routeTableEmpty(data, family string) bool {
	if family == "ipv6" && strings.TrimSpace(data) == "" {
		return true
	}
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if family == "ipv4" {
		if len(lines) == 0 || !strings.HasPrefix(lines[0], "Iface") {
			return false
		}
		// Any route is sufficient to withhold the unavailable conclusion.
		return len(lines) == 1
	}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) != 10 {
			return false
		}
		// Local, multicast and link-local routes alone cannot reach api6.
		if f[0] == strings.Repeat("0", 31)+"1" || strings.HasPrefix(f[0], "ff") || strings.HasPrefix(f[0], "fe80") {
			continue
		}
		// An all-zero reject route on lo is not usable; all other rows count.
		if f[9] == "lo" && f[8] == "00200200" {
			continue
		}
		return false
	}
	return true
}
