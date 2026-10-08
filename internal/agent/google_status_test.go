package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"404-probe/internal/protocol"
)

func htmlResponse(status int, finalURL, body string) googleHTTPResponse {
	return googleHTTPResponse{status: status, finalURL: finalURL, contentType: "text/html; charset=utf-8", body: []byte(body)}
}

// Only tests execute the audited upstream parser. Product code has no shell
// dependency; fixtures are fed directly to awk without making network requests.
func assertCanonicalGoogle(t *testing.T, mode string, response googleHTTPResponse, want string) {
	t.Helper()
	awk, err := exec.LookPath("awk")
	if err != nil {
		t.Log("canonical comparison requires awk; run Linux gate")
		return
	}
	source, err := os.ReadFile("testdata/gg-status.canonical")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(source), "\r\n", "\n")
	_, parser, found := strings.Cut(text, "awk -v mode=\"$check_mode\" '")
	if !found {
		t.Fatal("canonical parser start missing")
	}
	parser, _, found = strings.Cut(parser, "\n  '); then")
	if !found {
		t.Fatal("canonical parser end missing")
	}
	command := exec.Command(awk, "-v", "mode="+mode, parser)
	command.Env = append(os.Environ(), "LC_ALL=C")
	command.Stdin = strings.NewReader(fmt.Sprintf("%s\n__GG_STATUS_METADATA__:%d|%s|%s", response.body, response.status, response.finalURL, response.contentType))
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != want {
		t.Fatalf("canonical=%q Go=%q err=%v", output, want, err)
	}
}

func TestCanonicalYouTubeFixtures(t *testing.T) {
	tests := []struct {
		name     string
		response googleHTTPResponse
		status   protocol.YouTubeStatus
		region   string
	}{
		{"inner US", htmlResponse(200, "https://www.youtube.com/premium", `{"INNERTUBE_CONTEXT_GL":"US"}`), protocol.YouTubeNotCN, "US"},
		{"inner JP", htmlResponse(200, "https://www.youtube.com/premium", `{"INNERTUBE_CONTEXT_GL" : "JP"}`), protocol.YouTubeNotCN, "JP"},
		{"content HK", htmlResponse(200, "https://www.youtube.com/premium", `{"contentRegion":"HK"}`), protocol.YouTubeNotCN, "HK"},
		{"CN region", htmlResponse(200, "https://www.youtube.com/premium", `{"contentRegion":"CN"}`), protocol.YouTubeCN, "CN"},
		{"google cn", htmlResponse(200, "https://www.youtube.com/premium", `www.google.cn`), protocol.YouTubeCN, "CN"},
		{"CN wins", htmlResponse(200, "https://www.youtube.com/premium", `{"INNERTUBE_CONTEXT_GL":"JP","contentRegion":"CN"}`), protocol.YouTubeCN, "CN"},
		{"consent title", htmlResponse(200, "https://www.youtube.com/premium", `<title>Before you continue to YouTube</title>{"INNERTUBE_CONTEXT_GL":"JP"}`), protocol.YouTubeUnknown, ""},
		{"consent action", htmlResponse(200, "https://www.youtube.com/premium", `consent.youtube.com/save`), protocol.YouTubeUnknown, ""},
		{"malformed", htmlResponse(200, "https://www.youtube.com/premium", `{"INNERTUBE_CONTEXT_GL" "JP"}`), protocol.YouTubeUnknown, ""},
		{"no evidence", htmlResponse(200, "https://www.youtube.com/premium", `<html></html>`), protocol.YouTubeUnknown, ""},
		{"invalid region", htmlResponse(200, "https://www.youtube.com/premium", `{"contentRegion":"USA"}`), protocol.YouTubeUnknown, ""},
		{"wrong type", googleHTTPResponse{200, "https://www.youtube.com/", "application/json", []byte(`{"contentRegion":"JP"}`)}, protocol.YouTubeUnknown, ""},
		{"wrong host", htmlResponse(200, "https://youtube.example/", `{"contentRegion":"JP"}`), protocol.YouTubeUnknown, ""},
		{"non 200", htmlResponse(503, "https://www.youtube.com/", `{"contentRegion":"JP"}`), protocol.YouTubeUnknown, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseYouTube(test.response)
			label := "YouTube: " + strings.ToUpper(string(got.Status))
			if got.Status == protocol.YouTubeNotCN {
				label += " [" + got.Region + "]"
			}
			assertCanonicalGoogle(t, "youtube", test.response, label)
			if got.Status != test.status || got.Region != test.region {
				t.Fatalf("got=%+v want status=%s region=%s", got, test.status, test.region)
			}
		})
	}
}

func TestCanonicalSearchFixtures(t *testing.T) {
	ok := `<title>Google Search</title><form action="/search"><input name="q">`
	tests := []struct {
		name     string
		response googleHTTPResponse
		want     protocol.GoogleSearchStatus
	}{
		{"ok", htmlResponse(200, "https://www.google.com/search?q=curl", ok), protocol.GoogleSearchOK},
		{"unusual", htmlResponse(200, "https://www.google.com/search", "Our systems have detected unusual traffic"), protocol.GoogleSearchChallenge},
		{"sorry", htmlResponse(200, "https://www.google.com/sorry/index", "x"), protocol.GoogleSearchChallenge},
		{"captcha", htmlResponse(200, "https://www.google.com/search", "g-recaptcha"), protocol.GoogleSearchChallenge},
		{"blocked text", htmlResponse(200, "https://www.google.com/search", "Your request has been blocked"), protocol.GoogleSearchBlocked},
		{"403", htmlResponse(403, "https://www.google.com/search", "x"), protocol.GoogleSearchBlocked},
		{"429", htmlResponse(429, "https://www.google.co.uk/search", "x"), protocol.GoogleSearchBlocked},
		{"451", htmlResponse(451, "https://www.google.jp/search", "x"), protocol.GoogleSearchBlocked},
		{"unknown", htmlResponse(200, "https://www.google.com/search", "x"), protocol.GoogleSearchUnknown},
		{"wrong domain", htmlResponse(200, "https://google.example/search", ok), protocol.GoogleSearchUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseGoogleSearch(test.response).Status; got != test.want {
				t.Fatalf("got=%s want=%s", got, test.want)
			}
			assertCanonicalGoogle(t, "search", test.response, "Google Search: "+strings.ToUpper(string(test.want)))
		})
	}
}

func TestCanonicalSignInFixtures(t *testing.T) {
	valid := `<title>Sign in - Google Accounts</title><input id="identifierId" name="identifier"><button id="identifierNext">`
	tests := []struct {
		name     string
		response googleHTTPResponse
		want     protocol.GoogleSignInStatus
	}{
		{"reachable", htmlResponse(200, "https://accounts.google.com/signin/identifier", valid), protocol.GoogleSignInReachable},
		{"challenge url", htmlResponse(200, "https://accounts.google.com/signin/v2/challenge/pwd", "x"), protocol.GoogleSignInChallenge},
		{"verify", htmlResponse(200, "https://accounts.google.com/signin/identifier", "<title>Verify your identity</title>"), protocol.GoogleSignInChallenge},
		{"blocked", htmlResponse(200, "https://accounts.google.com/signin/identifier", "this request has been blocked"), protocol.GoogleSignInBlocked},
		{"403", htmlResponse(403, "https://accounts.google.com/", "x"), protocol.GoogleSignInBlocked},
		{"429", htmlResponse(429, "https://accounts.google.com/", "x"), protocol.GoogleSignInBlocked},
		{"451", htmlResponse(451, "https://accounts.google.com/", "x"), protocol.GoogleSignInBlocked},
		{"unknown", htmlResponse(200, "https://accounts.google.com/", valid), protocol.GoogleSignInUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseGoogleSignIn(test.response).Status; got != test.want {
				t.Fatalf("got=%s want=%s", got, test.want)
			}
			assertCanonicalGoogle(t, "login", test.response, "Google Sign-in: "+strings.ToUpper(string(test.want)))
		})
	}
}

func TestCanonicalGeminiFixtures(t *testing.T) {
	tests := []struct {
		name     string
		response googleHTTPResponse
		want     protocol.GeminiStatus
		region   string
	}{
		{"available", htmlResponse(200, "https://gemini.google.com/", `<title>Google Gemini</title>45631641,null,true`), protocol.GeminiAvailable, ""},
		{"available region", htmlResponse(200, "https://gemini.google.com/app", `<title>Google Gemini</title>45631641,null,true x,2,1,200,"JPN"`), protocol.GeminiAvailable, "JPN"},
		{"unsupported", htmlResponse(200, "https://gemini.google.com/", "Gemini is not available in your country"), protocol.GeminiBlocked, ""},
		{"marker blocked", htmlResponse(200, "https://gemini.google.com/", "45631641,null,false"), protocol.GeminiBlocked, ""},
		{"403", htmlResponse(403, "https://gemini.google.com/", "x"), protocol.GeminiBlocked, ""},
		{"451", htmlResponse(451, "https://gemini.google.com/", "x"), protocol.GeminiBlocked, ""},
		{"unknown", htmlResponse(200, "https://gemini.google.com/", "x"), protocol.GeminiUnknown, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseGemini(test.response)
			label := "Gemini: " + strings.ToUpper(string(got.Status))
			if got.Region != "" {
				label += " [" + got.Region + "]"
			}
			assertCanonicalGoogle(t, "gemini", test.response, label)
			if got.Status != test.want || got.Region != test.region {
				t.Fatalf("got=%+v", got)
			}
		})
	}
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
		googleYouTube: {htmlResponse(200, "https://www.youtube.com/premium", `{"INNERTUBE_CONTEXT_GL":"JP"}`), ""},
		googleGemini:  {googleHTTPResponse{}, "timeout"},
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
		return htmlResponse(200, target.url, `{"INNERTUBE_CONTEXT_GL":"JP"}`), ""
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
			if err != nil || !execution.Success || execution.Result.GoogleStatus.Validate() != nil {
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
		if request.URL.Path == "/premium" {
			return &http.Response{StatusCode: 302, Request: request, Header: http.Header{"Location": []string{"https://www.youtube.com/fixture"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		body := `{"INNERTUBE_CONTEXT_GL":"JP"}`
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
		{googleSearch, "https://www.google.co.uk/search", true}, {googleSearch, "https://www.google.com.evil.example/", false},
		{googleSignIn, "https://accounts.google.com/", true}, {googleSignIn, "https://accounts.google.com.evil/", false},
		{googleGemini, "https://gemini.google.com/", true}, {googleGemini, "http://gemini.google.com/", false},
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
