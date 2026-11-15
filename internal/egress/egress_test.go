package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func controlledChecker(t *testing.T, family string, handler http.HandlerFunc) *Checker {
	t.Helper()
	network, address, ip := "tcp4", "127.0.0.1:0", "127.0.0.1"
	if family == "ipv6" {
		network, address, ip = "tcp6", "[::1]:0", "::1"
	}
	l, err := net.Listen(network, address)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(handler)
	s.Listener.Close()
	s.Listener = l
	s.StartTLS()
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	c := NewChecker()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	endpoint := "https://example.com:" + port
	c.sites = c.sites[:1]
	c.sites[0].endpoint = [2]string{endpoint, endpoint}
	c.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	c.resolver = func(_ context.Context, requested, host string) ([]net.IP, error) {
		if requested != map[string]string{"ipv4": "ip4", "ipv6": "ip6"}[family] || host != "example.com" {
			t.Error("wrong DNS family/host", requested, host)
		}
		return []net.IP{net.ParseIP(ip)}, nil
	}
	c.allowDestination = func(text, f string) bool { return text == ip && f == family }
	return c
}

func TestM10NativeHTTPSFamilies(t *testing.T) {
	for _, family := range []string{"ipv4", "ipv6"} {
		t.Run(family, func(t *testing.T) {
			ip := "8.8.4.4"
			if family == "ipv6" {
				ip = "2606:4700:4700::1111"
			}
			c := controlledChecker(t, family, func(w http.ResponseWriter, r *http.Request) {
				if r.TLS == nil {
					t.Error("not TLS")
				}
				fmt.Fprintf(w, `{"ip":%q}`, ip)
			})
			v := c.Check(context.Background(), family)
			if v.Status != "available" || v.IP != ip {
				t.Fatalf("%+v", v)
			}
		})
	}
}

func TestM10HTTPSFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		code               int
	}{
		{"service", `{}`, "http_status", 503}, {"rate", `{}`, "http_status", 429}, {"redirect", `{}`, "http_status", 302}, {"private", `{"ip":"10.0.0.1"}`, "invalid_response", 200}, {"wrong_family", `{"ip":"2606:4700::1111"}`, "invalid_response", 200}, {"oversize", strings.Repeat("x", 1025), "invalid_response", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := controlledChecker(t, "ipv4", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://uncontrolled.invalid/")
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			})
			v := c.Check(context.Background(), "ipv4")
			if v.Status != "unknown" || v.Reason != tc.reason {
				t.Fatal(v)
			}
		})
	}
	t.Run("TLS verification", func(t *testing.T) {
		c := controlledChecker(t, "ipv4", func(w http.ResponseWriter, r *http.Request) {})
		c.tlsConfig = nil
		v := c.Check(context.Background(), "ipv4")
		if v.Status != "unknown" || v.Reason != "tls" {
			t.Fatal(v)
		}
	})
	t.Run("DNS", func(t *testing.T) {
		c := NewChecker()
		c.noRoute = func(string) bool { return false }
		c.resolver = func(context.Context, string, string) ([]net.IP, error) {
			return nil, &net.DNSError{Err: "controlled", IsNotFound: true}
		}
		v := c.Check(context.Background(), "ipv6")
		if v.Status != "unknown" || v.Reason != "dns" {
			t.Fatal(v)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		c := controlledChecker(t, "ipv6", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		v := c.Check(ctx, "ipv6")
		if v.Status != "unknown" || v.Reason != "timeout" {
			t.Fatal(v)
		}
	})
	t.Run("body timeout after headers", func(t *testing.T) {
		c := controlledChecker(t, "ipv6", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		v := c.Check(ctx, "ipv6")
		if v.Status != "unknown" || v.Reason != "timeout" {
			t.Fatal(v)
		}
	})
	// The well-known NAT64 prefix is judged by the IPv4 address inside it.
	t.Run("NAT64 destination with a public IPv4 is dialed", func(t *testing.T) {
		c := NewChecker()
		c.resolver = func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("64:ff9b::808:808")}, nil
		}
		dialed := 0
		c.dial = func(context.Context, string, string) (net.Conn, error) {
			dialed++
			return nil, errors.New("stop here")
		}
		if v := c.Check(context.Background(), "ipv6"); v.Status != "unknown" || dialed != 3 {
			t.Fatal(v, dialed)
		}
	})
	t.Run("NAT64 destination with a private IPv4 is refused", func(t *testing.T) {
		for _, address := range []string{"64:ff9b::a00:1", "64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::c0a8:101", "64:ff9b:1::808:808", "64:ff9b::1:808:808"} {
			c := NewChecker()
			c.resolver = func(context.Context, string, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP(address)}, nil
			}
			c.dial = func(context.Context, string, string) (net.Conn, error) {
				t.Errorf("dialed %s", address)
				return nil, errors.New("unexpected")
			}
			if v := c.Check(context.Background(), "ipv6"); v.Status != "unknown" {
				t.Fatal(address, v)
			}
		}
	})
}

func TestM10UnavailableNeedsProof(t *testing.T) {
	for _, proof := range []bool{false, true} {
		c := NewChecker()
		c.resolver = func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("2606:4700::1111")}, nil
		}
		c.dial = func(context.Context, string, string) (net.Conn, error) { return nil, syscall.ENETUNREACH }
		c.noRoute = func(string) bool { return proof }
		v := c.Check(context.Background(), "ipv6")
		want := "unknown"
		if proof {
			want = "unavailable"
		}
		if v.Status != want {
			t.Fatal(v)
		}
	}
	if !routeTableEmpty("Iface Destination Gateway Flags\n", "ipv4") || routeTableEmpty("garbage", "ipv4") || routeTableEmpty(strings.Repeat("0", 32)+" 00 "+strings.Repeat("0", 32)+" 00 "+strings.Repeat("0", 32)+" 00000001 00000000 00000000 00000001 eth0", "ipv6") {
		t.Fatal("route parser")
	}
}

// A Checker from the production constructor never dials a resolved non-public destination.
func TestNewCheckerNeverDialsNonPublicDestinations(t *testing.T) {
	for _, test := range []struct{ family, address string }{
		{"ipv4", "127.0.0.1"}, {"ipv4", "10.0.0.5"}, {"ipv4", "169.254.169.254"}, {"ipv4", "192.168.1.1"}, {"ipv4", "100.64.0.1"}, {"ipv4", "198.51.100.7"},
		{"ipv6", "::1"}, {"ipv6", "fc00::1"}, {"ipv6", "fe80::1"}, {"ipv6", "2001:db8::1"},
	} {
		c := NewChecker()
		var hosts []string
		c.resolver = func(_ context.Context, network, host string) ([]net.IP, error) {
			if network != map[string]string{"ipv4": "ip4", "ipv6": "ip6"}[test.family] {
				t.Errorf("resolver network %q for %s", network, test.family)
			}
			hosts = append(hosts, host)
			return []net.IP{net.ParseIP(test.address)}, nil
		}
		c.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Errorf("dialed %s", test.address)
			return nil, errors.New("unexpected")
		}
		if v := c.Check(context.Background(), test.family); v.Status != "unknown" || v.Tried != 3 {
			t.Errorf("%s: %+v", test.address, v)
		}
		want := "api.ipify.org,ipv4.icanhazip.com,one.one.one.one"
		if test.family == "ipv6" {
			want = "api6.ipify.org,ipv6.icanhazip.com,one.one.one.one"
		}
		if strings.Join(hosts, ",") != want {
			t.Fatalf("%s hosts: %v", test.family, hosts)
		}
	}
}

func TestPublicEgressFallbackAfterSiteFailure(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
	}{
		{name: "deadline"},
		{name: "connection"},
		{name: "certificate"},
		{name: "malformed JSON", body: "{", status: 200},
		{name: "5xx", status: 503},
		{name: "redirect", status: 302},
		{name: "oversized", body: strings.Repeat("x", 1025), status: 200},
		{name: "private", body: `{"ip":"10.0.0.1"}`, status: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := controlledChecker(t, "ipv4", func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/ipify":
					w.Header().Set("Location", "/icanhazip")
					w.WriteHeader(tt.status)
					fmt.Fprint(w, tt.body)
				case "/icanhazip":
					fmt.Fprint(w, "8.8.4.4\n")
				default:
					t.Errorf("unexpected HTTP request: %s", r.URL.Path)
				}
			})
			base := c.sites[0].endpoint[0]
			c.sites = NewChecker().sites
			for i := range c.sites {
				c.sites[i].endpoint[0] = base + "/" + c.sites[i].name
			}
			c.resolver = func(context.Context, string, string) ([]net.IP, error) {
				return []net.IP{net.IPv4(127, 0, 0, 1)}, nil
			}
			if tt.name == "certificate" {
				c.sites[0].endpoint[0] = strings.Replace(c.sites[0].endpoint[0], "example.com", "wrong.example", 1)
			}
			var dials atomic.Int32
			dial := c.dial
			c.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if dials.Add(1) == 1 {
					switch tt.name {
					case "deadline":
						<-ctx.Done()
						return nil, ctx.Err()
					case "connection":
						return nil, syscall.ECONNREFUSED
					}
				}
				return dial(ctx, network, addr)
			}
			got := c.check(context.Background(), "ipv4", 250*time.Millisecond)
			if got.Site != protocol.EgressSiteIcanhazip || got.IP != "8.8.4.4" || got.Tried != 2 || dials.Load() != 2 {
				t.Fatalf("got %+v with %d dials", got, dials.Load())
			}
		})
	}
}

func TestPublicEgressFallbackLastReasonOrInsufficientBudget(t *testing.T) {
	c := controlledChecker(t, "ipv4", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ip=not-an-address\n")
	})
	cloudflare := NewChecker().sites[2]
	cloudflare.endpoint = c.sites[0].endpoint
	c.sites = []site{c.sites[0], c.sites[0], cloudflare}
	c.sites[0].endpoint[0] = "https://dns.example/"
	c.sites[1].endpoint[0] = "https://network.example/"
	c.noRoute = func(string) bool { return false }
	resolve, dial := c.resolver, c.dial
	var lookups atomic.Int32
	c.resolver = func(ctx context.Context, network, host string) ([]net.IP, error) {
		lookups.Add(1)
		switch host {
		case "dns.example":
			return nil, &net.DNSError{IsNotFound: true}
		case "network.example":
			return []net.IP{net.IPv4(8, 8, 8, 8)}, nil
		}
		return resolve(ctx, network, host)
	}
	c.allowDestination = func(string, string) bool { return true }
	c.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "8.8.8.8:") {
			return nil, syscall.ECONNREFUSED
		}
		return dial(ctx, network, addr)
	}
	if got := c.Check(context.Background(), "ipv4"); got.Status != "unknown" || got.Reason != "invalid_response" || got.Tried != 3 || lookups.Load() != 3 {
		t.Fatalf("all failed: %+v (%d lookups)", got, lookups.Load())
	}
	lookups.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := c.Check(ctx, "ipv4"); got.Status != "unknown" || got.Reason != "dns" || got.Tried != 1 || lookups.Load() != 1 {
		t.Fatalf("limited budget: %+v (%d lookups)", got, lookups.Load())
	}
}

func TestPublicEgressUnavailableStopsFallback(t *testing.T) {
	const defaultRoute = "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000001 00000000 00000000 00000001 eth0"
	for _, tt := range []struct {
		name, route, reason string
		resolveErr, dialErr error
		tried, dials        int
	}{
		{"DNS and no route", "", "no_route", &net.DNSError{IsNotFound: true}, nil, 1, 0},
		{"DNS with default route", defaultRoute, "dns", &net.DNSError{IsNotFound: true}, nil, 3, 0},
		{"no address and no route", "", "no_route", nil, syscall.EADDRNOTAVAIL, 1, 1},
		{"no address with default route", defaultRoute, "network", nil, syscall.EADDRNOTAVAIL, 3, 3},
		{"family unavailable", defaultRoute, "family_unavailable", nil, syscall.EAFNOSUPPORT, 1, 1},
		{"network unreachable and no route", "", "no_route", nil, syscall.ENETUNREACH, 1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := NewChecker()
			var lookups, dials int
			c.noRoute = func(family string) bool { return routeTableEmpty(tt.route, family) }
			c.resolver = func(context.Context, string, string) ([]net.IP, error) {
				lookups++
				return []net.IP{net.ParseIP("2606:4700::1111")}, tt.resolveErr
			}
			c.dial = func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, tt.dialErr
			}
			status := "unknown"
			if tt.tried == 1 {
				status = "unavailable"
			}
			if got := c.Check(context.Background(), "ipv6"); got.Status != status || got.Reason != tt.reason ||
				got.Tried != tt.tried || lookups != tt.tried || dials != tt.dials {
				t.Fatalf("got %+v with %d lookups and %d dials", got, lookups, dials)
			}
		})
	}
}

func TestPublicEgressSiteFormats(t *testing.T) {
	for _, tt := range []struct {
		index  int
		body   string
		accept bool
	}{
		{0, `{"ip":"8.8.4.4"}`, true},
		{0, `{"ip":42}`, false},
		{1, "8.8.4.4", true},
		{1, "8.8.4.4\n", true},
		{1, "8.8.4.4\r\n", true},
		{1, "8.8.4.4\n\n", false},
		{1, "8.8.4.4\n8.8.8.8", false},
		{1, "", false},
		{2, "other=1\nip=8.8.4.4\ntls=TLSv1.3\n", true},
		{2, "other=1\n", false},
		{2, "ip=8.8.4.4\nip=8.8.4.4\n", false},
		{2, "ip=not-an-ip\n", false},
		{2, "ip=127.0.0.1\n", false},
	} {
		t.Run(fmt.Sprintf("%d-%q", tt.index, tt.body), func(t *testing.T) {
			c := controlledChecker(t, "ipv4", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tt.body) })
			current := NewChecker().sites[tt.index]
			current.endpoint = c.sites[0].endpoint
			c.sites[0] = current
			got := c.Check(context.Background(), "ipv4")
			if tt.accept {
				if got.IP != "8.8.4.4" || got.Site != current.name || got.Status != "available" {
					t.Fatal(got)
				}
			} else if got.Reason != "invalid_response" || got.Site != "" || got.IP != "" {
				t.Fatal(got)
			}
		})
	}
}
