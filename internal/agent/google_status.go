package agent

import (
	"context"
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

// Canonical source audited for this port:
// https://github.com/404-git-404/404-vps-tools/blob/b8830f4c86ddebc0be19bd4fadb07ca5a586dfb2/gg-status
// blob 50c5afa7f6fae163e48040c4624e0638c6250840.
const (
	googleStatusUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
	googleStatusBodyLimit = 2 << 20
)

type googleService string

const (
	googleYouTube googleService = "youtube"
	googleSearch  googleService = "search"
	googleSignIn  googleService = "signin"
	googleGemini  googleService = "gemini"
)

type googleTarget struct {
	service googleService
	url     string
	cookie  string
}

var googleTargets = []googleTarget{
	{googleYouTube, "https://www.youtube.com/premium", "CONSENT=YES+cb.20220301-11-p0.en+FX+700"},
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
	// Keep legal legacy wire/DB slots without executing the removed checks.
	result := protocol.GoogleStatusResult{
		Search: protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown},
		SignIn: protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown},
		Gemini: protocol.GeminiResult{Status: protocol.GeminiUnknown},
	}
	for _, target := range googleTargets {
		response, category := e.fetcher.fetch(ctx, target)
		switch target.service {
		case googleYouTube:
			result.YouTube = parseYouTube(response)
			if result.YouTube.Status == protocol.YouTubeUnknown {
				result.YouTube.Error.Category = firstCategory(category, "unrecognized_response")
			}
		}
	}
	return Execution{Success: true, Result: protocol.ProbeResult{GoogleStatus: &result}}, nil
}

func firstCategory(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

type googleNetworkFetcher struct{ transport http.RoundTripper }

var (
	errGoogleRedirectLimit  = errors.New("Google Status redirect limit exceeded")
	errGoogleUnsafeRedirect = errors.New("Google Status redirect target is not allowed")
)

func (f googleNetworkFetcher) fetch(ctx context.Context, target googleTarget) (googleHTTPResponse, string) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSHandshakeTimeout = 3 * time.Second
	defer transport.CloseIdleConnections()
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport.DialContext = func(ctx context.Context, _ string, address string) (net.Conn, error) {
		connectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(connectCtx, "ip4", host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			if !googlePublicIPv4(ip) {
				continue
			}
			connection, err := dialer.DialContext(connectCtx, "tcp4", net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("no public IPv4 address")
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
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return googleHTTPResponse{}, "timeout"
		}
		if errors.Is(err, errGoogleRedirectLimit) {
			return googleHTTPResponse{}, "redirect_limit"
		}
		if errors.Is(err, errGoogleUnsafeRedirect) {
			return googleHTTPResponse{}, "unexpected_redirect"
		}
		return googleHTTPResponse{}, "network_error"
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, googleStatusBodyLimit+1))
	if err != nil {
		return googleHTTPResponse{}, "response_read_error"
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
	host := strings.ToLower(target.Hostname())
	switch service {
	case googleYouTube:
		return host == "www.youtube.com"
	case googleSearch:
		return googleSearchRedirectHosts[host]
	case googleSignIn:
		return host == "accounts.google.com"
	case googleGemini:
		return host == "gemini.google.com"
	default:
		return false
	}
}

// An explicit transport allowlist is intentionally stricter than the canonical
// parser's syntactic suffix rule. Unlisted redirects fail UNKNOWN.
var googleSearchRedirectHosts = map[string]bool{
	"www.google.com": true, "www.google.co.jp": true, "www.google.com.hk": true,
	"www.google.co.uk": true, "www.google.de": true, "www.google.fr": true,
	"www.google.ca": true, "www.google.com.au": true, "www.google.co.in": true,
	"www.google.com.sg": true, "www.google.com.tw": true, "www.google.co.kr": true,
	"www.google.com.br": true, "www.google.es": true, "www.google.it": true,
	"www.google.nl": true, "www.google.co.nz": true, "www.google.ch": true,
}

func googlePublicIPv4(ip netip.Addr) bool {
	return ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) &&
		!netip.MustParsePrefix("198.18.0.0/15").Contains(ip) &&
		!netip.MustParsePrefix("192.0.0.0/24").Contains(ip)
}

func validGoogleSearchHost(host string) bool {
	const prefix = "www.google."
	if !strings.HasPrefix(host, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(host, prefix)
	if len(suffix) == 2 || len(suffix) == 3 {
		return lowerLetters(suffix)
	}
	parts := strings.Split(suffix, ".")
	return len(parts) == 2 && (parts[0] == "com" || parts[0] == "co") && len(parts[1]) == 2 && lowerLetters(parts[1])
}

func lowerLetters(value string) bool {
	for _, char := range value {
		if char < 'a' || char > 'z' {
			return false
		}
	}
	return value != ""
}

func validHTML(response googleHTTPResponse) bool {
	return len(response.body) != 0 && strings.HasPrefix(response.contentType, "text/html")
}

func parseYouTube(response googleHTTPResponse) protocol.YouTubeResult {
	unknown := protocol.YouTubeResult{Status: protocol.YouTubeUnknown}
	if !validHTML(response) || response.status != http.StatusOK ||
		!(response.finalURL == "https://www.youtube.com" || strings.HasPrefix(response.finalURL, "https://www.youtube.com/")) {
		return unknown
	}
	body := string(response.body)
	if strings.Contains(body, "<title>Before you continue to YouTube</title>") || strings.Contains(body, "consent.youtube.com/save") {
		return unknown
	}
	cn := strings.Contains(body, "www.google.cn")
	inner, innerCN := scanYouTubeRegion(body, "INNERTUBE_CONTEXT_GL")
	content, contentCN := scanYouTubeRegion(body, "contentRegion")
	if cn || innerCN || contentCN {
		value := true
		return protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: "CN", SentToChina: &value}
	}
	region := inner
	if region == "" {
		region = content
	}
	if region == "" {
		return unknown
	}
	value := false
	return protocol.YouTubeResult{Status: protocol.YouTubeNotCN, Region: region, SentToChina: &value}
}

func scanYouTubeRegion(body, key string) (string, bool) {
	needle := `"` + key + `"`
	first := ""
	for offset := 0; ; {
		relative := strings.Index(body[offset:], needle)
		if relative < 0 {
			return first, false
		}
		position := offset + relative + len(needle)
		for position < len(body) && strings.ContainsRune(" \t\r\n\v\f", rune(body[position])) {
			position++
		}
		if position >= len(body) || body[position] != ':' {
			offset = position
			continue
		}
		position++
		for position < len(body) && strings.ContainsRune(" \t\r\n\v\f", rune(body[position])) {
			position++
		}
		if position+3 >= len(body) || body[position] != '"' || body[position+3] != '"' {
			offset = position
			continue
		}
		value := body[position+1 : position+3]
		if !upperRegion(value, 2) {
			offset = position + 1
			continue
		}
		if value == "CN" {
			return first, true
		}
		if first == "" {
			first = value
		}
		offset = position + 4
	}
}

func parseGoogleSearch(response googleHTTPResponse) protocol.GoogleSearchResult {
	unknown := protocol.GoogleSearchResult{Status: protocol.GoogleSearchUnknown}
	if !validHTML(response) {
		return unknown
	}
	parsed, err := url.Parse(response.finalURL)
	if err != nil || parsed.Scheme != "https" || !validGoogleSearchHost(strings.ToLower(parsed.Hostname())) || parsed.Port() != "" {
		return unknown
	}
	lower := strings.ToLower(string(response.body))
	path := parsed.EscapedPath()
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	if strings.HasPrefix(path, "/sorry/") || strings.Contains(lower, "unusual traffic from your computer network") ||
		strings.Contains(lower, "our systems have detected unusual traffic") || strings.Contains(lower, "recaptcha/api") || strings.Contains(lower, "g-recaptcha") {
		return protocol.GoogleSearchResult{Status: protocol.GoogleSearchChallenge}
	}
	if strings.Contains(lower, "unaddressed abuse") || strings.Contains(lower, "your request has been blocked") ||
		strings.Contains(lower, "this request has been blocked") || strings.Contains(lower, "<title>access denied</title>") ||
		response.status == 403 || response.status == 429 || response.status == 451 {
		return protocol.GoogleSearchResult{Status: protocol.GoogleSearchBlocked}
	}
	if response.status == 200 && (path == "/search" || strings.HasPrefix(path, "/search?")) && strings.Contains(lower, "google search</title>") &&
		(strings.Contains(lower, "/search?") || strings.Contains(lower, `action="/search"`) || strings.Contains(lower, `name="q"`)) {
		return protocol.GoogleSearchResult{Status: protocol.GoogleSearchOK}
	}
	return unknown
}

func parseGoogleSignIn(response googleHTTPResponse) protocol.GoogleSignInResult {
	unknown := protocol.GoogleSignInResult{Status: protocol.GoogleSignInUnknown}
	final, err := url.Parse(response.finalURL)
	if !validHTML(response) || err != nil || final.Scheme != "https" || final.Hostname() != "accounts.google.com" || final.Port() != "" {
		return unknown
	}
	lower := strings.ToLower(string(response.body))
	if strings.Contains(response.finalURL, "/challenge/") || strings.Contains(response.finalURL, "/signin/challenge") ||
		strings.Contains(response.finalURL, "/signin/v2/challenge") || strings.Contains(lower, "<title>verify your identity</title>") ||
		strings.Contains(lower, "unusual traffic from your computer network") {
		return protocol.GoogleSignInResult{Status: protocol.GoogleSignInChallenge}
	}
	if strings.Contains(lower, "your request has been blocked") || strings.Contains(lower, "this request has been blocked") ||
		strings.Contains(lower, "<title>access denied</title>") || response.status == 403 || response.status == 429 || response.status == 451 {
		return protocol.GoogleSignInResult{Status: protocol.GoogleSignInBlocked}
	}
	if response.status == 200 && strings.Contains(response.finalURL, "/signin/identifier") &&
		strings.Contains(lower, "<title>sign in - google accounts</title>") && strings.Contains(lower, `id="identifierid"`) &&
		strings.Contains(lower, "identifiernext") && strings.Contains(lower, `name="identifier"`) {
		return protocol.GoogleSignInResult{Status: protocol.GoogleSignInReachable}
	}
	return unknown
}

// Legacy parser retained for compatibility fixtures; the current executor never calls it.
func parseGemini(response googleHTTPResponse) protocol.GeminiResult {
	unknown := protocol.GeminiResult{Status: protocol.GeminiUnknown}
	if !validHTML(response) || !(response.finalURL == "https://gemini.google.com" || strings.HasPrefix(response.finalURL, "https://gemini.google.com/")) {
		return unknown
	}
	body := string(response.body)
	lower := strings.ToLower(body)
	if strings.Contains(body, "45631641,null,false") || strings.Contains(lower, "gemini is not currently supported in your country") ||
		strings.Contains(lower, "gemini is not available in your location") || strings.Contains(lower, "gemini is not available in your country") ||
		strings.Contains(lower, "gemini does not currently support your country/region") || strings.Contains(lower, "this country is not supported") ||
		response.status == 403 || response.status == 451 {
		return protocol.GeminiResult{Status: protocol.GeminiBlocked}
	}
	if response.status == 200 && strings.Contains(lower, "google gemini</title>") && strings.Contains(body, "45631641,null,true") {
		return protocol.GeminiResult{Status: protocol.GeminiAvailable, Region: scanGeminiRegion(body)}
	}
	return unknown
}

func scanGeminiRegion(body string) string {
	const needle = `,2,1,200,"`
	for offset := 0; ; {
		relative := strings.Index(body[offset:], needle)
		if relative < 0 {
			return ""
		}
		position := offset + relative + len(needle)
		if position+3 < len(body) && upperRegion(body[position:position+3], 3) && body[position+3] == '"' {
			return body[position : position+3]
		}
		offset = position
	}
}

func upperRegion(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, char := range value {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}
