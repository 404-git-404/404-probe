package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

type m11CanceledEOFBody struct{ context.Context }

func (b m11CanceledEOFBody) Read([]byte) (int, error) { <-b.Done(); return 0, io.EOF }
func (m11CanceledEOFBody) Close() error               { return nil }

func TestM11YouTubeCleanEOFDoesNotEraseCancellation(t *testing.T) {
	for _, deadline := range []bool{true, false} {
		t.Run(map[bool]string{true: "deadline", false: "cancel"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := "canceled"
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
				want = "timeout"
			}
			defer cancel()
			f := googleNetworkFetcher{transport: googleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if !deadline {
					cancel()
				}
				return &http.Response{StatusCode: 200, Request: r, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: m11CanceledEOFBody{ctx}}, nil
			})}
			_, category := f.fetch(ctx, googleTargets[0])
			if category != want {
				t.Fatal(category, want)
			}
		})
	}
}

func TestM11YouTubeEvidence(t *testing.T) {
	for _, tc := range []struct{ name, body, region, reason string }{
		{"US", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "US", ""},
		{"JP whitespace", youtubeFixturePage("{\n\"INNERTUBE_CONTEXT_GL\" : \"JP\"\n}"), "JP", ""},
		{"HK nested", youtubeFixturePage(`{"INNERTUBE_CONTEXT":{"client":{"gl":"HK"}}}`), "HK", ""},
		{"CN", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"CN"}`), "CN", ""},
		{"DE escaped", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"D\u0045"}`), "DE", ""},
		{"unrelated google.cn", strings.Replace(youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "</body>", `<a href="https://www.google.cn">irrelevant</a></body>`, 1), "US", ""},
		{"conflict", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US","contentRegion":"CN"}`), "", "region_conflict"},
		{"nested conflict", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"JP","INNERTUBE_CONTEXT":{"client":{"gl":"US"}}}`), "", "region_conflict"},
		{"duplicate key", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US","INNERTUBE_CONTEXT_GL":"CN"}`), "", "invalid_configuration"},
		{"invalid ISO", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"ZZ"}`), "", "invalid_region"},
		{"wrong value type", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":123}`), "", "invalid_region"},
		{"content alone", youtubeFixturePage(`{"contentRegion":"CN"}`), "", "missing_evidence"},
		{"bare old JSON", `{"INNERTUBE_CONTEXT_GL":"CN"}`, "", "response_truncated"},
		{"unrelated object", `<html><head><title>YouTube</title><script>var unrelated={"INNERTUBE_CONTEXT_GL":"CN"};</script></head></html>`, "", "missing_evidence"},
		{"comment", `<html><head><title>YouTube</title><script>/* ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"}); */</script></head></html>`, "", "missing_evidence"},
		{"string", `<html><head><title>YouTube</title><script>var note=';ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"});';</script></head></html>`, "", "missing_evidence"},
		{"regex", `<html><head><title>YouTube</title><script>var note=/;ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"});/;</script></head></html>`, "", "missing_evidence"},
		{"dead function", `<html><head><title>YouTube</title><script>function old(){ytcfg.set({"INNERTUBE_CONTEXT_GL":"CN"});}</script></head></html>`, "", "missing_evidence"},
		{"truncated", strings.TrimSuffix(youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "</html>"), "", "response_truncated"},
		{"consent", strings.Replace(youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "YouTube Premium", "Before you continue to YouTube", 1), "", "consent_page"},
		{"challenge", strings.Replace(youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "</body>", `<div class="g-recaptcha"></div></body>`, 1), "", "challenge_page"},
		{"title alone", `<html><head><title>YouTube Premium</title></head></html>`, "", "missing_evidence"},
		{"two configs conflict", strings.Replace(youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "</script>", `;ytcfg.set({"INNERTUBE_CONTEXT_GL":"JP"});</script>`, 1), "", "region_conflict"},
		{"two configs agree", strings.Replace(youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "</script>", `;ytcfg.set({"INNERTUBE_CONTEXT_GL":"US"});</script>`, 1), "US", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseYouTube(htmlResponse(200, googleTargets[0].url, tc.body))
			if got.Region != tc.region || got.Error.Category != tc.reason {
				t.Fatalf("%+v", got)
			}
			if tc.region == "" && got.Status != protocol.YouTubeUnknown {
				t.Fatal(got)
			}
			if tc.region == "CN" && got.Status != protocol.YouTubeCN {
				t.Fatal(got)
			}
			if err := (protocol.GoogleStatusResult{YouTube: got, Search: protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown}, SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}, Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown}}).Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Real TCP4/TLS with a normally verified www.youtube.com certificate. Only the
// private test dial/resolve seams map this approved host to a loopback server.
func m11HTTPS(t *testing.T, handler http.HandlerFunc) (googleNetworkFetcher, *atomic.Int32) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"www.youtube.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(handler)
	s.Listener.Close()
	s.Listener = l
	s.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	s.StartTLS()
	t.Cleanup(s.Close)
	var calls atomic.Int32
	f := googleNetworkFetcher{roots: roots, addressAllowed: func(ip netip.Addr) bool { return ip == netip.MustParseAddr("127.0.0.1") }, resolve: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip4" || host != "www.youtube.com" {
			t.Error("wrong DNS path", network, host)
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}, dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		if network != "tcp4" || address != "127.0.0.1:443" {
			t.Error("wrong socket path", network, address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
	}}
	return f, &calls
}

func m11Execute(ctx context.Context, f googleNetworkFetcher) (Execution, error) {
	return (&GoogleStatusExecutor{fetcher: f}).Execute(ctx, protocol.Job{ProbeType: protocol.ProbeTypeGoogleStatus, Config: protocol.ProbeConfig{GoogleStatus: &protocol.GoogleStatusConfig{}}})
}

func TestM11RealHTTPSClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason, region string
		status                     int
	}{
		{"good", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US"}`), "", "US", 200},
		{"CN", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"CN"}`), "", "CN", 200},
		{"conflict", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"US","contentRegion":"CN"}`), "region_conflict", "", 200},
		{"consent", `<html><title>Before you continue to YouTube</title></html>`, "consent_page", "", 200},
		{"challenge", `<html><title>Verify your identity</title></html>`, "challenge_page", "", 200},
		{"empty", `<html><title>YouTube Premium</title></html>`, "missing_evidence", "", 200},
		{"403", `blocked`, "http_forbidden", "", 403}, {"429", `limited`, "rate_limited", "", 429}, {"451", `restricted`, "http_restricted", "", 451}, {"503", `down`, "http_server_error", "", 503},
		{"too big", strings.Repeat("x", googleStatusBodyLimit+1), "response_body_too_large", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, calls := m11HTTPS(t, func(w http.ResponseWriter, r *http.Request) {
				if r.TLS == nil || r.Host != "www.youtube.com" {
					t.Error("not approved HTTPS")
				}
				if r.Header.Get("Cookie") != "" {
					t.Error("unexpected cookie")
				}
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			})
			e, err := m11Execute(context.Background(), f)
			if err != nil {
				t.Fatal(err)
			}
			y := e.Result.GoogleStatus.YouTube
			if y.Region != tc.region || e.Success != (tc.region != "") {
				t.Fatal(e)
			}
			if tc.reason != "" && (e.ErrorCategory != "youtube_"+tc.reason || y.Error.Category != protocol.CompatibleYouTubeCategory(tc.reason)) {
				t.Fatal(e)
			}
			if calls.Load() != 1 {
				t.Fatal("unexpected requests", calls.Load())
			}
		})
	}
	for _, headers := range []bool{false, true} {
		t.Run(map[bool]string{false: "before headers timeout", true: "body timeout"}[headers], func(t *testing.T) {
			f, _ := m11HTTPS(t, func(w http.ResponseWriter, r *http.Request) {
				if headers {
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
			defer cancel()
			e, err := m11Execute(ctx, f)
			if err != nil || e.Success || e.ErrorCategory != "youtube_timeout" {
				t.Fatal(e, err)
			}
		})
	}
	t.Run("TLS", func(t *testing.T) {
		f, _ := m11HTTPS(t, func(w http.ResponseWriter, r *http.Request) {})
		f.roots = x509.NewCertPool()
		e, err := m11Execute(context.Background(), f)
		if err != nil || e.ErrorCategory != "youtube_tls_error" {
			t.Fatal(e, err)
		}
	})
	t.Run("DNS", func(t *testing.T) {
		f := googleNetworkFetcher{resolve: func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, &net.DNSError{Err: "fixture", IsNotFound: true}
		}}
		e, err := m11Execute(context.Background(), f)
		if err != nil || e.ErrorCategory != "youtube_dns_error" {
			t.Fatal(e, err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f, calls := m11HTTPS(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request after cancel") })
		e, err := m11Execute(ctx, f)
		if err != nil || e.ErrorCategory != "youtube_canceled" || calls.Load() != 0 {
			t.Fatal(e, err)
		}
	})
}

func TestM11HTTPSRedirectAndAddressPolicy(t *testing.T) {
	for _, target := range []string{"https://evil.example/", "http://www.youtube.com/premium", "https://www.youtube.com:443/premium", "https://127.0.0.1/", "https://accounts.google.com/", "https://consent.youtube.com/save"} {
		t.Run(target, func(t *testing.T) {
			f, calls := m11HTTPS(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", target); w.WriteHeader(302) })
			e, err := m11Execute(context.Background(), f)
			reason := "youtube_unexpected_redirect"
			if strings.Contains(target, "consent.youtube.com") {
				reason = "youtube_consent_page"
			}
			if err != nil || e.ErrorCategory != reason || calls.Load() != 1 {
				t.Fatal(e, err, calls.Load())
			}
		})
	}
	t.Run("bounded same host", func(t *testing.T) {
		f, _ := m11HTTPS(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://www.youtube.com/premium?again=1")
			w.WriteHeader(302)
		})
		e, err := m11Execute(context.Background(), f)
		if err != nil || e.ErrorCategory != "youtube_redirect_limit" {
			t.Fatal(e, err)
		}
	})
	t.Run("proxy ignored", func(t *testing.T) {
		t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
		f, _ := m11HTTPS(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"JP"}`)))
		})
		e, err := m11Execute(context.Background(), f)
		if err != nil || !e.Success {
			t.Fatal(e, err)
		}
	})
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "192.0.2.1", "198.18.0.1", "100.64.0.1", "::ffff:8.8.8.8", "64:ff9b::808:808"} {
		t.Run(ip, func(t *testing.T) {
			f := googleNetworkFetcher{resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr(ip)}, nil
			}, dial: func(context.Context, string, string) (net.Conn, error) {
				t.Error("unsafe target dialed")
				return nil, context.Canceled
			}}
			e, err := m11Execute(context.Background(), f)
			if err != nil || e.ErrorCategory != "youtube_unsafe_address" {
				t.Fatal(e, err)
			}
		})
	}
}
