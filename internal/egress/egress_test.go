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
	"syscall"
	"testing"
	"time"
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
	c.endpoint = [2]string{endpoint, endpoint}
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
	t.Run("NAT64 destination", func(t *testing.T) {
		c := NewChecker()
		c.resolver = func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("64:ff9b::808:808")}, nil
		}
		c.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Error("dialed NAT64")
			return nil, errors.New("unexpected")
		}
		if v := c.Check(context.Background(), "ipv6"); v.Status != "unknown" {
			t.Fatal(v)
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
