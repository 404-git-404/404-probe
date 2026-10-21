package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

const (
	googleStatusUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
	googleStatusBodyLimit = 2 << 20
)

type googleService string

const (
	googleYouTube googleService = "youtube"
)

type googleTarget struct {
	service googleService
	url     string
	cookie  string
}

var googleTargets = []googleTarget{
	{googleYouTube, "https://www.youtube.com/premium", ""},
}

type googleHTTPResponse struct {
	status      int
	finalURL    string
	contentType string
	body        []byte
}

type googleStatusFetcher interface {
	fetch(context.Context, googleTarget) (googleHTTPResponse, string)
}

type GoogleStatusExecutor struct{ fetcher googleStatusFetcher }

func NewGoogleStatusExecutor() *GoogleStatusExecutor {
	return &GoogleStatusExecutor{fetcher: googleNetworkFetcher{}}
}

func (*GoogleStatusExecutor) SupportedProbeTypes() []protocol.ProbeType {
	return []protocol.ProbeType{protocol.ProbeTypeGoogleStatus}
}

func (*GoogleStatusExecutor) SupportsGoogleStatus() bool { return true }

func (e *GoogleStatusExecutor) Execute(ctx context.Context, job protocol.Job) (Execution, error) {
	if job.ProbeType != protocol.ProbeTypeGoogleStatus || job.Config.GoogleStatus == nil {
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	// Keep legal legacy wire/DB slots without executing the removed checks.
	result := protocol.GoogleStatusResult{
		Search: protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown},
		SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown},
		Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown},
	}
	for _, target := range googleTargets {
		response, category := e.fetcher.fetch(ctx, target)
		if err := ctx.Err(); err != nil {
			response, category = googleHTTPResponse{}, googleFetchCategory(ctx, err)
		}
		switch target.service {
		case googleYouTube:
			result.YouTube = parseYouTube(response)
			if err := ctx.Err(); err != nil {
				result.YouTube = protocol.YouTubeResult{Status: protocol.YouTubeUnknown}
				category = googleFetchCategory(ctx, err)
			}
			if result.YouTube.Status == protocol.YouTubeUnknown {
				result.YouTube.Error.Category = firstCategory(category, firstCategory(result.YouTube.Error.Category, "unrecognized_response"))
			}
		}
	}
	if result.YouTube.Status == protocol.YouTubeUnknown {
		reason := result.YouTube.Error.Category
		result.YouTube.Error.Category = protocol.CompatibleYouTubeCategory(reason)
		return Execution{Success: false, ErrorCategory: "youtube_" + reason, Result: protocol.ProbeResult{GoogleStatus: &result}}, nil
	}
	return Execution{Success: true, Result: protocol.ProbeResult{GoogleStatus: &result}}, nil
}

// A Server that did not advertise ReportCapabilities.GoogleStatusChinaSignal only accepts the older
// cn shape (Region CN, no premium notice); a cn page region and the notice stay on this Agent.
func googleStatusOlderServerShape(result *protocol.GoogleStatusResult) {
	if result == nil || result.YouTube.Status != protocol.YouTubeCN {
		return
	}
	result.YouTube.Region = "CN"
	result.YouTube.PremiumNotice = nil
}

func firstCategory(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

type googleNetworkFetcher struct {
	transport      http.RoundTripper // Legacy deterministic transport fixtures only.
	resolve        func(context.Context, string, string) ([]netip.Addr, error)
	dial           func(context.Context, string, string) (net.Conn, error)
	roots          *x509.CertPool
	addressAllowed func(netip.Addr) bool // Unexported controlled-TLS fixture seam.
}

var (
	errGoogleRedirectLimit   = errors.New("Google Status redirect limit exceeded")
	errGoogleUnsafeRedirect  = errors.New("Google Status redirect target is not allowed")
	errGoogleConsentRedirect = errors.New("YouTube consent redirect")
	errGoogleUnsafeAddress   = errors.New("no public IPv4 destination")
)

func (f googleNetworkFetcher) fetch(ctx context.Context, target googleTarget) (googleHTTPResponse, string) {
	if target.service != googleYouTube {
		return googleHTTPResponse{}, "invalid_target"
	}
	ctx, cancelRequest := context.WithTimeout(ctx, 6*time.Second)
	defer cancelRequest()
	transport := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 32 << 10, MaxConnsPerHost: 1, TLSClientConfig: &tls.Config{RootCAs: f.roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	dial := f.dial
	if dial == nil {
		dial = dialer.DialContext
	}
	resolve := f.resolve
	if resolve == nil {
		resolve = net.DefaultResolver.LookupNetIP
	}
	allowed := f.addressAllowed
	if allowed == nil {
		allowed = googlePublicIPv4
	}
	transport.DialContext = func(_ context.Context, _ string, address string) (net.Conn, error) {
		connectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := resolve(connectCtx, "ip4", host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for i, ip := range ips {
			if i >= 4 {
				break
			}
			if !allowed(ip) {
				continue
			}
			connection, err := dial(connectCtx, "tcp4", net.JoinHostPort(ip.String(), port))
			if err == nil {
				peer, peerErr := netip.ParseAddrPort(connection.RemoteAddr().String())
				if peerErr != nil || !peer.Addr().Is4() || peer.Addr().Is4In6() {
					connection.Close()
					return nil, errors.New("YouTube socket family mismatch")
				}
				return connection, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errGoogleUnsafeAddress
	}
	transport.DialTLSContext = nil
	var roundTripper http.RoundTripper = transport
	if f.transport != nil {
		roundTripper = f.transport
	}
	client := &http.Client{
		Transport: roundTripper,
		Timeout:   6 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if request.URL.Hostname() == "consent.youtube.com" || request.URL.Hostname() == "consent.google.com" {
				return errGoogleConsentRedirect
			}
			if len(via) > 5 {
				return errGoogleRedirectLimit
			}
			if request.URL.Scheme != "https" || !googleHostAllowed(target.service, request.URL) {
				return errGoogleUnsafeRedirect
			}
			request.Header.Del("Referer")
			return nil
		},
	}
	parsed, _ := url.Parse(target.url)
	if parsed == nil || parsed.Scheme != "https" || !googleHostAllowed(target.service, parsed) {
		return googleHTTPResponse{}, "invalid_target"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.url, nil)
	if err != nil {
		return googleHTTPResponse{}, "invalid_target"
	}
	request.Header.Set("User-Agent", googleStatusUserAgent)
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if target.cookie != "" {
		request.Header.Set("Cookie", target.cookie)
	}
	response, err := client.Do(request)
	if err != nil {
		return googleHTTPResponse{}, googleFetchCategory(ctx, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, googleStatusBodyLimit+1))
	// Cancellation can close a body with a clean EOF. Neither that EOF nor a
	// usable prefix is proof that the request completed before its deadline.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return googleHTTPResponse{}, googleFetchCategory(ctx, ctxErr)
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return googleHTTPResponse{}, "timeout"
	}
	if err != nil {
		category := googleFetchCategory(ctx, err)
		if category == "network_error" {
			category = "response_read_error"
		}
		return googleHTTPResponse{}, category
	}
	if len(body) > googleStatusBodyLimit {
		return googleHTTPResponse{}, "response_body_too_large"
	}
	return googleHTTPResponse{response.StatusCode, response.Request.URL.String(), response.Header.Get("Content-Type"), body}, ""
}

func googleHostAllowed(service googleService, target *url.URL) bool {
	if target == nil || target.Scheme != "https" || target.User != nil || target.Port() != "" {
		return false
	}
	return service == googleYouTube && strings.ToLower(target.Hostname()) == "www.youtube.com"
}

func googlePublicIPv4(ip netip.Addr) bool { return protocol.PublicEgressIP(ip.String(), "ipv4") }

func googleFetchCategory(ctx context.Context, err error) string {
	var netError net.Error
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netError) && netError.Timeout():
		return "timeout"
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errGoogleUnsafeAddress):
		return "unsafe_address"
	case errors.Is(err, errGoogleConsentRedirect):
		return "consent_page"
	case errors.Is(err, errGoogleRedirectLimit):
		return "redirect_limit"
	case errors.Is(err, errGoogleUnsafeRedirect):
		return "unexpected_redirect"
	case errors.As(err, &dns):
		return "dns_error"
	case errors.As(err, &certificate):
		return "tls_error"
	default:
		return "network_error"
	}
}
