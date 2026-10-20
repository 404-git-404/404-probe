package agent

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"404-probe/internal/protocol"
)

func htmlResponse(status int, finalURL, body string) googleHTTPResponse {
	return googleHTTPResponse{status: status, finalURL: finalURL, contentType: "text/html; charset=utf-8", body: []byte(body)}
}

type fixtureGoogleFetcher map[googleService]struct {
	response googleHTTPResponse
	category string
}

func (f fixtureGoogleFetcher) fetch(_ context.Context, target googleTarget) (googleHTTPResponse, string) {
	value := f[target.service]
	return value.response, value.category
}

func TestGoogleStatusServiceFailuresRemainIndependent(t *testing.T) {
	executor := &GoogleStatusExecutor{fetcher: fixtureGoogleFetcher{
		googleYouTube: {htmlResponse(200, "https://www.youtube.com/premium", youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"JP"}`)), ""},
	}}
	job := protocol.Job{ProbeType: protocol.ProbeTypeGoogleStatus, Config: protocol.ProbeConfig{GoogleStatus: &protocol.GoogleStatusConfig{}}}
	execution, err := executor.Execute(context.Background(), job)
	if err != nil || !execution.Success || execution.Result.GoogleStatus.Gemini.Status != protocol.GeminiUnknown || execution.Result.GoogleStatus.Gemini.Error.Category != "" || execution.Result.GoogleStatus.YouTube.Region != "JP" || execution.Result.GoogleStatus.Search.Status != protocol.GoogleSearchUnknown || execution.Result.GoogleStatus.SignIn.Status != protocol.GoogleSignInUnknown || execution.Result.GoogleStatus.Search.Error.Category != "" || execution.Result.GoogleStatus.SignIn.Error.Category != "" {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
}

type recordingGoogleFetcher struct {
	calls    []googleTarget
	category string
}

func (f *recordingGoogleFetcher) fetch(ctx context.Context, target googleTarget) (googleHTTPResponse, string) {
	f.calls = append(f.calls, target)
	if ctx.Err() != nil {
		return googleHTTPResponse{}, "canceled"
	}
	if f.category != "" {
		return googleHTTPResponse{}, f.category
	}
	if target.service == googleYouTube {
		return htmlResponse(200, target.url, youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"JP"}`)), ""
	}
	return htmlResponse(200, target.url, `<title>Google Gemini</title>45631641,null,true`), ""
}

func TestGoogleStatusExecutorOnlyRetainedTargets(t *testing.T) {
	for _, mode := range []string{"success", "failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fetcher := &recordingGoogleFetcher{}
			if mode == "failure" {
				fetcher.category = "timeout"
			}
			if mode == "canceled" {
				cancel()
			}
			executor := &GoogleStatusExecutor{fetcher: fetcher}
			job := protocol.Job{ProbeType: protocol.ProbeTypeGoogleStatus, Config: protocol.ProbeConfig{GoogleStatus: &protocol.GoogleStatusConfig{}}}
			execution, err := executor.Execute(ctx, job)
			if err != nil || execution.Success != (mode == "success") || execution.Result.GoogleStatus.Validate() != nil {
				t.Fatalf("execution=%+v err=%v", execution, err)
			}
			if len(fetcher.calls) != 1 || fetcher.calls[0].service != googleYouTube {
				t.Fatalf("targets=%+v", fetcher.calls)
			}
			if fetcher.calls[0].url != "https://www.youtube.com/premium" {
				t.Fatalf("URLs=%+v", fetcher.calls)
			}
			result := execution.Result.GoogleStatus
			if result.Search.Status != protocol.GoogleSearchUnknown || result.SignIn.Status != protocol.GoogleSignInUnknown || result.Gemini.Status != protocol.GeminiUnknown || result.Search.Error.Category != "" || result.SignIn.Error.Category != "" || result.Gemini.Error.Category != "" {
				t.Fatalf("compatibility slots=%+v", result)
			}
		})
	}
}

func TestGoogleStatusExecutorRetainedRedirectChainOnly(t *testing.T) {
	var hosts []string
	transport := googleRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		hosts = append(hosts, request.URL.Hostname())
		if request.URL.Hostname() != "www.youtube.com" {
			t.Fatalf("removed target requested: %s", request.URL)
		}
		if request.URL.RawQuery == "" {
			return &http.Response{StatusCode: 302, Request: request, Header: http.Header{"Location": []string{"https://www.youtube.com/premium?fixture=redirect"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		body := youtubeFixturePage(`{"INNERTUBE_CONTEXT_GL":"JP"}`)
		return &http.Response{StatusCode: 200, Request: request, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	executor := &GoogleStatusExecutor{fetcher: googleNetworkFetcher{transport: transport}}
	execution, err := executor.Execute(context.Background(), protocol.Job{ProbeType: protocol.ProbeTypeGoogleStatus, Config: protocol.ProbeConfig{GoogleStatus: &protocol.GoogleStatusConfig{}}})
	if err != nil || execution.Result.GoogleStatus.Validate() != nil || execution.Result.GoogleStatus.HasRetainedUnknown() {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	if strings.Join(hosts, "|") != "www.youtube.com|www.youtube.com" {
		t.Fatalf("redirect targets=%v", hosts)
	}
}

func TestGoogleStatusHostAllowlist(t *testing.T) {
	for _, test := range []struct {
		service googleService
		raw     string
		want    bool
	}{
		{googleYouTube, "https://www.youtube.com/", true}, {googleYouTube, "https://evil.example/", false},
		{googleYouTube, "http://www.youtube.com/", false}, {googleYouTube, "https://www.youtube.com:8443/", false},
		{googleService("gemini"), "https://gemini.google.com/", false}, {googleService("search"), "https://www.google.com/search", false},
	} {
		parsed, _ := url.Parse(test.raw)
		got := googleHostAllowed(test.service, parsed)
		if got != test.want {
			t.Errorf("%s got=%t want=%t", test.raw, got, test.want)
		}
	}
}

type googleRoundTripFunc func(*http.Request) (*http.Response, error)

func (f googleRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGoogleStatusFetcherRejectsOversizedAndUnsafeRedirect(t *testing.T) {
	oversized := googleNetworkFetcher{transport: googleRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Request: request, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", googleStatusBodyLimit+1)))}, nil
	})}
	if _, category := oversized.fetch(context.Background(), googleTargets[0]); category != "response_body_too_large" {
		t.Fatalf("oversized category=%q", category)
	}
	unsafe := googleNetworkFetcher{transport: googleRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Request: request, Header: http.Header{"Location": []string{"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader("redirect"))}, nil
	})}
	if _, category := unsafe.fetch(context.Background(), googleTargets[0]); category != "unexpected_redirect" {
		t.Fatalf("redirect category=%q", category)
	}
}

func youtubeFixturePage(config string) string {
	return `<html><head><title>YouTube Premium</title><script>ytcfg.set(` + config + `);</script></head><body></body></html>`
}
