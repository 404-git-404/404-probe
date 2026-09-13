package geoip

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLookupCountryCodeSuccessIsBoundedAndMinimal(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.Header.Get("Accept") != "application/json" || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Errorf("request method=%s accept=%q", request.Method, request.Header.Get("Accept"))
		}
		_, _ = fmt.Fprint(w, `{"success":true,"country_code":"JP"}`)
	}))
	defer server.Close()
	code, err := LookupCountryCode(context.Background(), Client(time.Second), server.URL)
	if err != nil || code != "JP" || requests.Load() != 1 {
		t.Fatalf("code=%q requests=%d err=%v", code, requests.Load(), err)
	}
}

func TestClientDoesNotUseEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:1234")
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:1234")
	client := Client(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || client.Jar != nil || client.CheckRedirect == nil || client.Timeout != time.Second {
		t.Fatalf("unsafe lookup client: %#v", client)
	}
	if Endpoint != "https://ipwho.is/?fields=success,country_code" {
		t.Fatalf("unexpected production endpoint %q", Endpoint)
	}
}

func TestLookupCountryCodeFailsClosedWithoutRetry(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
	}{
		{name: "provider failure", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"success":false,"country_code":"US"}`)
		}},
		{name: "invalid country", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"success":true,"country_code":"ZZ"}`)
		}},
		{name: "malformed", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `{`) }},
		{name: "oversized", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
		}},
		{name: "redirect", handler: func(w http.ResponseWriter, request *http.Request) {
			http.Redirect(w, request, "/elsewhere", http.StatusFound)
		}},
		{name: "timeout", timeout: 10 * time.Millisecond, handler: func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(50 * time.Millisecond)
			_, _ = fmt.Fprint(w, `{"success":true,"country_code":"US"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				test.handler(w, request)
			}))
			defer server.Close()
			timeout := test.timeout
			if timeout == 0 {
				timeout = time.Second
			}
			code, err := LookupCountryCode(context.Background(), Client(timeout), server.URL)
			if code != "" || !errors.Is(err, ErrLookupFailed) || requests.Load() != 1 {
				t.Fatalf("code=%q requests=%d err=%v", code, requests.Load(), err)
			}
		})
	}
}
