package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"404-probe/internal/protocol"
)

const (
	maxHTTPProbeBodyBytes = 64 * 1024
	maxHTTPRedirects      = 5
)

var (
	errHTTPRedirectLimit  = errors.New("HTTP probe redirect limit exceeded")
	errHTTPUnsafeRedirect = errors.New("HTTP probe redirect target is not allowed")
)

// HTTPExecutor executes only HTTP probe jobs. It intentionally supports no
// custom headers or TLS overrides.
type HTTPExecutor struct {
	client *http.Client
}

func NewHTTPExecutor() *HTTPExecutor {
	transport := http.DefaultTransport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	return &HTTPExecutor{client: &http.Client{
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > maxHTTPRedirects {
				return errHTTPRedirectLimit
			}
			redirectConfig := protocol.ProbeConfig{HTTP: &protocol.HTTPConfig{URL: request.URL.String(), Method: request.Method}}
			if err := redirectConfig.Validate(protocol.ProbeTypeHTTP); err != nil {
				return errHTTPUnsafeRedirect
			}
			request.Header.Del("Referer")
			return nil
		},
	}}
}

func (*HTTPExecutor) SupportedProbeTypes() []protocol.ProbeType {
	return []protocol.ProbeType{protocol.ProbeTypeHTTP}
}

func (e *HTTPExecutor) Execute(ctx context.Context, job protocol.Job) (Execution, error) {
	if job.ProbeType != protocol.ProbeTypeHTTP {
		return Execution{}, fmt.Errorf("%w: %s", ErrUnsupportedProbeType, job.ProbeType)
	}
	if err := job.Config.Validate(protocol.ProbeTypeHTTP); err != nil {
		return failedHTTPExecution("invalid_config", "HTTP probe configuration is invalid", protocol.HTTPResult{}), nil
	}
	config := job.Config.HTTP

	parsed, err := url.Parse(config.URL)
	if err != nil {
		return failedHTTPExecution("invalid_config", "HTTP probe configuration is invalid", protocol.HTTPResult{}), nil
	}
	timings := newHTTPProbeTimings(parsed.Hostname())
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, timings.trace()), config.Method, config.URL, nil)
	if err != nil {
		return failedHTTPExecution("invalid_config", "HTTP probe configuration is invalid", protocol.HTTPResult{}), nil
	}

	started := time.Now()
	timings.start(started)
	client := e.client
	if client == nil {
		client = NewHTTPExecutor().client
	}
	response, err := client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		measurement, resolvedIP := timings.measurement(time.Since(started), 0, 0, false)
		category, message := classifyHTTPProbeError(ctx, err)
		execution := failedHTTPExecution(category, message, measurement)
		execution.ResolvedIP = resolvedIP
		return execution, nil
	}
	defer response.Body.Close()

	read, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxHTTPProbeBodyBytes+1))
	truncated := read > maxHTTPProbeBodyBytes
	measurement, resolvedIP := timings.measurement(time.Since(started), response.StatusCode, uint64(read), truncated)
	if readErr != nil {
		category, message := classifyHTTPProbeError(ctx, readErr)
		if category == "network_error" {
			category = "response_read_error"
			message = "HTTP response body could not be read"
		}
		execution := failedHTTPExecution(category, message, measurement)
		execution.ResolvedIP = resolvedIP
		return execution, nil
	}
	if truncated {
		execution := failedHTTPExecution("response_body_too_large", "HTTP response body exceeds 64 KiB", measurement)
		execution.ResolvedIP = resolvedIP
		return execution, nil
	}
	if config.ExpectedStatus != nil && response.StatusCode != *config.ExpectedStatus {
		execution := failedHTTPExecution("unexpected_status", "HTTP response status did not match expected_status", measurement)
		execution.ResolvedIP = resolvedIP
		return execution, nil
	}
	return Execution{Success: true, ResolvedIP: resolvedIP, Result: protocol.ProbeResult{HTTP: &measurement}}, nil
}

func failedHTTPExecution(category, message string, measurement protocol.HTTPResult) Execution {
	return Execution{
		Success:       false,
		ErrorCategory: category,
		ErrorMessage:  message,
		Result:        protocol.ProbeResult{HTTP: &measurement},
	}
}

func classifyHTTPProbeError(ctx context.Context, err error) (string, string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "HTTP probe timed out"
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "canceled", "HTTP probe was canceled"
	}
	if errors.Is(err, errHTTPRedirectLimit) {
		return "redirect_limit", "HTTP probe exceeded 5 redirects"
	}
	if errors.Is(err, errHTTPUnsafeRedirect) {
		return "unsafe_redirect", "HTTP probe redirect target is not allowed"
	}
	var certificateError *tls.CertificateVerificationError
	if errors.As(err, &certificateError) {
		return "tls_error", "HTTP probe TLS verification failed"
	}
	return "network_error", "HTTP probe request failed"
}

type httpProbeTimings struct {
	mu sync.Mutex

	requestStarted  time.Time
	dnsStarted      time.Time
	connectStarted  map[string]time.Time
	tlsStarted      time.Time
	dnsDuration     time.Duration
	connectDuration time.Duration
	tlsDuration     time.Duration
	ttfbDuration    time.Duration
	resolvedIP      string
}

func newHTTPProbeTimings(hostname string) *httpProbeTimings {
	timings := &httpProbeTimings{connectStarted: make(map[string]time.Time)}
	if address, err := netip.ParseAddr(hostname); err == nil {
		timings.resolvedIP = address.Unmap().String()
	}
	return timings
}

func (t *httpProbeTimings) start(at time.Time) {
	t.mu.Lock()
	t.requestStarted = at
	t.mu.Unlock()
}

func (t *httpProbeTimings) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			t.mu.Lock()
			t.dnsStarted = time.Now()
			t.mu.Unlock()
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			t.mu.Lock()
			if !t.dnsStarted.IsZero() {
				t.dnsDuration += time.Since(t.dnsStarted)
				t.dnsStarted = time.Time{}
			}
			if t.resolvedIP == "" && len(info.Addrs) > 0 {
				if address, ok := netip.AddrFromSlice(info.Addrs[0].IP); ok {
					t.resolvedIP = address.Unmap().String()
				}
			}
			t.mu.Unlock()
		},
		ConnectStart: func(network, address string) {
			t.mu.Lock()
			t.connectStarted[network+"\x00"+address] = time.Now()
			t.mu.Unlock()
		},
		ConnectDone: func(network, address string, connectErr error) {
			t.mu.Lock()
			key := network + "\x00" + address
			if started, ok := t.connectStarted[key]; ok {
				if connectErr == nil {
					t.connectDuration += time.Since(started)
				}
				delete(t.connectStarted, key)
			}
			t.mu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			host, _, err := net.SplitHostPort(info.Conn.RemoteAddr().String())
			if err != nil {
				return
			}
			address, err := netip.ParseAddr(host)
			if err != nil {
				return
			}
			t.mu.Lock()
			t.resolvedIP = address.Unmap().String()
			t.mu.Unlock()
		},
		TLSHandshakeStart: func() {
			t.mu.Lock()
			t.tlsStarted = time.Now()
			t.mu.Unlock()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			t.mu.Lock()
			if !t.tlsStarted.IsZero() {
				t.tlsDuration += time.Since(t.tlsStarted)
				t.tlsStarted = time.Time{}
			}
			t.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			t.ttfbDuration = time.Since(t.requestStarted)
			t.mu.Unlock()
		},
	}
}

func (t *httpProbeTimings) measurement(total time.Duration, statusCode int, bodyBytes uint64, truncated bool) (protocol.HTTPResult, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return protocol.HTTPResult{
		DNSMS:         durationMilliseconds(t.dnsDuration),
		ConnectMS:     durationMilliseconds(t.connectDuration),
		TLSMS:         durationMilliseconds(t.tlsDuration),
		TTFBMS:        durationMilliseconds(t.ttfbDuration),
		TotalMS:       durationMilliseconds(total),
		StatusCode:    statusCode,
		BodyBytes:     bodyBytes,
		BodyTruncated: truncated,
	}, t.resolvedIP
}

func durationMilliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}
