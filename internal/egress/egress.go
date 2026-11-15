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

const siteTimeout = 5 * time.Second
const MaxCheckTime = 3 * siteTimeout

type site struct {
	name     string
	endpoint [2]string
	accept   string
	parse    func([]byte) string
}

type Checker struct {
	sites     []site
	resolver  func(context.Context, string, string) ([]net.IP, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	tlsConfig *tls.Config
	// Only package tests can override destination validation for controlled TLS.
	allowDestination func(string, string) bool
	noRoute          func(string) bool
}

func NewChecker() *Checker {
	d := &net.Dialer{Timeout: siteTimeout}
	r := &net.Resolver{PreferGo: true}
	return &Checker{
		sites: []site{
			{protocol.EgressSiteIPify, [2]string{"https://api.ipify.org?format=json", "https://api6.ipify.org?format=json"}, "application/json", parseIPify},
			{protocol.EgressSiteIcanhazip, [2]string{"https://ipv4.icanhazip.com", "https://ipv6.icanhazip.com"}, "text/plain", parseIcanhazip},
			{protocol.EgressSiteCloudflare, [2]string{"https://one.one.one.one/cdn-cgi/trace", "https://one.one.one.one/cdn-cgi/trace"}, "text/plain", parseCloudflare},
		},
		resolver: r.LookupIP, dial: d.DialContext, allowDestination: protocol.PublicDestinationIP, noRoute: noUsableRoute,
	}
}

func (c *Checker) Check(ctx context.Context, family string) protocol.EgressObservation {
	return c.check(ctx, family, siteTimeout)
}

func (c *Checker) check(ctx context.Context, family string, perSite time.Duration) protocol.EgressObservation {
	ctx, cancel := context.WithTimeout(ctx, MaxCheckTime)
	defer cancel()
	deadline, _ := ctx.Deadline()
	result := protocol.UnknownEgress(family, "timeout")
	for i, current := range c.sites {
		if ctx.Err() != nil || i > 0 && time.Until(deadline) < perSite {
			break
		}
		attempt, cancelAttempt := context.WithTimeout(ctx, perSite)
		result = c.checkSite(attempt, family, current)
		cancelAttempt()
		result.Tried = i + 1
		if result.Status != "unknown" {
			break
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

func (s site) request(ctx context.Context, family string) (*http.Request, error) {
	index := 0
	if family == "ipv6" {
		index = 1
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint[index], nil)
	if err == nil {
		req.Header.Set("Accept", s.accept)
	}
	return req, err
}

func (c *Checker) siteClient(ctx context.Context, family string) *http.Client {
	network, ipNetwork := "tcp4", "ip4"
	if family == "ipv6" {
		network, ipNetwork = "tcp6", "ip6"
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxConnsPerHost: 1, TLSClientConfig: c.tlsConfig,
		TLSHandshakeTimeout: siteTimeout, ResponseHeaderTimeout: siteTimeout}
	transport.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := c.resolver(ctx, ipNetwork, host)
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
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *Checker) checkSite(ctx context.Context, family string, current site) protocol.EgressObservation {
	req, err := current.request(ctx, family)
	if err != nil {
		return protocol.UnknownEgress(family, "network")
	}
	client := c.siteClient(ctx, family)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return c.classifyFailure(family, err, "network")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return protocol.UnknownEgress(family, "http_status")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return c.classifyFailure(family, err, "invalid_response")
	}
	if len(data) <= 1024 {
		if ip := current.parse(data); protocol.PublicEgressIP(ip, family) {
			return protocol.EgressObservation{Family: family, Status: "available", IP: ip, Reason: "verified", Site: current.name}
		}
	}
	return protocol.UnknownEgress(family, "invalid_response")
}

func (c *Checker) classifyFailure(family string, err error, fallback string) protocol.EgressObservation {
	var dns *net.DNSError
	var tlsErr *tls.CertificateVerificationError
	var netErr net.Error
	switch {
	case errors.As(err, &dns):
		if c.noRoute(family) {
			return protocol.EgressObservation{Family: family, Status: "unavailable", Reason: "no_route"}
		}
		fallback = "dns"
	case errors.As(err, &tlsErr):
		fallback = "tls"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		fallback = "timeout"
	case errors.Is(err, syscall.EAFNOSUPPORT):
		return protocol.EgressObservation{Family: family, Status: "unavailable", Reason: "family_unavailable"}
	case (errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EADDRNOTAVAIL)) && c.noRoute(family):
		return protocol.EgressObservation{Family: family, Status: "unavailable", Reason: "no_route"}
	}
	return protocol.UnknownEgress(family, fallback)
}

func parseIPify(data []byte) string {
	var body struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	return body.IP
}

func parseIcanhazip(data []byte) string {
	line, hasNewline := strings.CutSuffix(string(data), "\n")
	if hasNewline {
		line = strings.TrimSuffix(line, "\r")
	}
	return line
}

func parseCloudflare(data []byte) string {
	var ip string
	found := false
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "ip="); ok {
			if found {
				return ""
			}
			found, ip = true, strings.TrimSuffix(value, "\r")
		}
	}
	return ip
}

// Linux route tables are namespace-local. Missing/unreadable/ambiguous data is
// not proof. After a DNS or socket failure, absence of ANY up,
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
		// Local, multicast and link-local routes alone cannot reach a public site.
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
