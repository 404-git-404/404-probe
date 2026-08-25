package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/auth"
)

const remoteTestAgentID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestValidateRemoteOrigin(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		allowHTTP bool
		want      string
		wantError bool
	}{
		{name: "HTTPS host", value: "https://example.com", want: "https://example.com"},
		{name: "HTTPS port", value: "https://example.com:8443/", want: "https://example.com:8443"},
		{name: "HTTP localhost", value: "http://localhost:8080", allowHTTP: true, want: "http://localhost:8080"},
		{name: "HTTP IPv4 loopback", value: "http://127.0.0.2", allowHTTP: true, want: "http://127.0.0.2"},
		{name: "HTTP IPv6 loopback", value: "http://[::1]:8080", allowHTTP: true, want: "http://[::1]:8080"},
		{name: "missing", wantError: true},
		{name: "surrounding whitespace", value: " https://example.com", wantError: true},
		{name: "scheme case", value: "HTTPS://example.com", want: "https://example.com"},
		{name: "no scheme", value: "example.com", wantError: true},
		{name: "wrong scheme", value: "ftp://example.com", wantError: true},
		{name: "HTTP without opt in", value: "http://localhost", wantError: true},
		{name: "HTTP public", value: "http://example.com", allowHTTP: true, wantError: true},
		{name: "HTTP private IPv4", value: "http://192.168.1.2", allowHTTP: true, wantError: true},
		{name: "HTTP unspecified IPv6", value: "http://[::]", allowHTTP: true, wantError: true},
		{name: "userinfo", value: "https://user:secret@example.com", wantError: true},
		{name: "path", value: "https://example.com/api", wantError: true},
		{name: "query", value: "https://example.com/?x=1", wantError: true},
		{name: "empty query", value: "https://example.com?", wantError: true},
		{name: "fragment", value: "https://example.com/#x", wantError: true},
		{name: "empty fragment", value: "https://example.com#", wantError: true},
		{name: "empty port", value: "https://example.com:", wantError: true},
		{name: "invalid port", value: "https://example.com:bad", wantError: true},
		{name: "port zero", value: "https://example.com:0", wantError: true},
		{name: "port too large", value: "https://example.com:65536", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateRemoteOrigin(test.value, test.allowHTTP)
			if test.wantError {
				if err == nil {
					t.Fatalf("origin=%q", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("origin=%q error=%v", got, err)
			}
		})
	}
}

func TestRemoteAgentListTableRequestAndPagination(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	lastSeen := int64(1_700_000_001_000)
	nextCursor := "next_page_cursor"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/control/agents" ||
			request.URL.RawQuery != "cursor=page_cursor&limit=7&status=online" {
			t.Errorf("request=%s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers=%v", request.Header)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, `{"items":[{"agent_id":%q,"name":"alpha","revoked":false,"created_at":1700000000000,"online":true,"last_seen":%d}],"next_cursor":%q}`,
			remoteTestAgentID, lastSeen, nextCursor)
	}))
	defer server.Close()

	var output bytes.Buffer
	err := runRemoteCommand([]string{"agent", "list", "--server", server.URL, "--allow-insecure-http",
		"--control-token-file", tokenFile, "--status", "online", "--limit", "7", "--cursor", "page_cursor"},
		remoteClientOptions{}, &output)
	if err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, value := range []string{"ID", "NAME", remoteTestAgentID, "alpha", "online", "next_cursor: " + nextCursor} {
		if !strings.Contains(got, value) {
			t.Fatalf("output missing %q:\n%s", value, got)
		}
	}
	if strings.Contains(got, token) || requests.Load() != 1 {
		t.Fatalf("requests=%d output=%q", requests.Load(), got)
	}
}

func TestRemoteAgentListJSONPreservesEnvelope(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	server := remoteJSONServer(t, http.StatusOK,
		`{"items":[],"next_cursor":null,"ignored":"server-only"}`)
	defer server.Close()

	var output bytes.Buffer
	err := runRemoteCommand([]string{"agent", "list", "--server", server.URL, "--allow-insecure-http",
		"--control-token-file", tokenFile, "--json"}, remoteClientOptions{}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "{\"items\":[],\"next_cursor\":null}\n" {
		t.Fatalf("output=%q", output.String())
	}
}

func TestRemoteAgentGetTableAndJSON(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	response := fmt.Sprintf(`{"agent_id":%q,"name":"alpha","revoked":false,"created_at":1700000000000,"online":true,"last_seen":1700000001000,"state":{"hostname":"probe-host","os":"linux","arch":"amd64","uptime":10,"cpu_percent":5,"load1":1,"load5":2,"load15":3,"ram_used":10,"ram_total":20,"ram_percent":50,"swap_used":1,"swap_total":2,"swap_percent":50,"disk_used":30,"disk_total":60,"disk_percent":50,"rx_rate":4,"tx_rate":5,"rx_total":100,"tx_total":200,"collected_at":1700000001000},"secret":"ignored"}`, remoteTestAgentID)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.EscapedPath() != "/api/v1/control/agents/"+remoteTestAgentID || request.URL.RawQuery != "" {
			t.Errorf("URL=%s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, response)
	}))
	defer server.Close()

	common := []string{"agent", "get", remoteTestAgentID, "--server", server.URL, "--allow-insecure-http", "--control-token-file", tokenFile}
	var table bytes.Buffer
	if err := runRemoteCommand(common, remoteClientOptions{}, &table); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"AGENT ID", remoteTestAgentID, "HOSTNAME", "probe-host", "CPU", "5%", "RX TOTAL", "100"} {
		if !strings.Contains(table.String(), value) {
			t.Fatalf("table missing %q:\n%s", value, table.String())
		}
	}

	var jsonOutput bytes.Buffer
	if err := runRemoteCommand(append(common, "--json"), remoteClientOptions{}, &jsonOutput); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["agent_id"] != remoteTestAgentID || decoded["secret"] != nil || decoded["state"] == nil {
		t.Fatalf("JSON=%s", jsonOutput.String())
	}
	if strings.Contains(table.String()+jsonOutput.String(), token) || requests.Load() != 2 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestRemoteCommandValidationAndIsolation(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	base := []string{"agent", "list", "--server", "https://example.com", "--control-token-file", tokenFile}
	tests := []struct {
		name string
		args []string
	}{
		{name: "empty"},
		{name: "probe namespace", args: []string{"probe", "list"}},
		{name: "unknown operation", args: []string{"agent", "delete"}},
		{name: "DB isolation", args: append(append([]string{}, base...), "--db", "x")},
		{name: "inline token forbidden", args: append(append([]string{}, base...), "--token", "secret")},
		{name: "position", args: append(append([]string{}, base...), "extra")},
		{name: "empty status", args: append(append([]string{}, base...), "--status=")},
		{name: "bad status", args: append(append([]string{}, base...), "--status", "active")},
		{name: "low limit", args: append(append([]string{}, base...), "--limit", "0")},
		{name: "high limit", args: append(append([]string{}, base...), "--limit", "101")},
		{name: "empty cursor", args: append(append([]string{}, base...), "--cursor=")},
		{name: "large cursor", args: append(append([]string{}, base...), "--cursor", strings.Repeat("x", maxRemoteCursorBytes+1))},
		{name: "get missing ID", args: []string{"agent", "get", "--server", "https://example.com"}},
		{name: "get uppercase ID", args: []string{"agent", "get", strings.Repeat("A", 32)}},
		{name: "get short ID", args: []string{"agent", "get", "aa"}},
		{name: "missing server", args: []string{"agent", "list", "--control-token-file", tokenFile}},
		{name: "missing token file", args: []string{"agent", "list", "--server", "https://example.com"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := runRemoteCommand(test.args, remoteClientOptions{}, io.Discard); err == nil {
				t.Fatal("invalid command accepted")
			}
		})
	}
}

func TestRemoteHumanOutputReplacesControlCharacters(t *testing.T) {
	value := "safe\x1b]52;clipboard\a\ntext"
	got := remoteHumanText(value)
	if strings.ContainsAny(got, "\x1b\a\n") || got != "safe�]52;clipboard��text" {
		t.Fatalf("text=%q", got)
	}
}

func TestRemoteRedirectIsRefusedWithoutCredentialForwarding(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		targetRequests.Add(1)
		if request.Header.Get("Authorization") != "" {
			t.Error("redirect target received authorization")
		}
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Error("origin did not receive authorization")
		}
		http.Redirect(w, request, target.URL, http.StatusFound)
	}))
	defer origin.Close()

	err := runRemoteCommand([]string{"agent", "list", "--server", origin.URL, "--allow-insecure-http",
		"--control-token-file", tokenFile}, remoteClientOptions{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "redirect status 302") || targetRequests.Load() != 0 {
		t.Fatalf("requests=%d error=%v", targetRequests.Load(), err)
	}
}

func TestRemoteTimeoutAndNoRetry(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		<-request.Context().Done()
	}))
	defer server.Close()

	err := runRemoteCommand([]string{"agent", "list", "--server", server.URL, "--allow-insecure-http",
		"--control-token-file", tokenFile}, remoteClientOptions{Timeout: 20 * time.Millisecond}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "remote request failed") || requests.Load() != 1 {
		t.Fatalf("requests=%d error=%v", requests.Load(), err)
	}
}

func TestRemoteRejectsUntrustedTLSByDefault(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":null}`)
	}))
	defer server.Close()

	args := []string{"agent", "list", "--server", server.URL, "--control-token-file", tokenFile}
	if err := runRemoteCommand(args, remoteClientOptions{}, io.Discard); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	if err := runRemoteCommand(args, remoteClientOptions{Transport: server.Client().Transport}, io.Discard); err != nil {
		t.Fatalf("test transport rejected: %v", err)
	}
}

func TestRemoteResponseSecurityAndProtocolErrors(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        string
	}{
		{name: "wrong content type", status: http.StatusOK, contentType: "text/plain", body: `{}`, want: "Content-Type"},
		{name: "missing content type", status: http.StatusOK, body: `{}`, want: "Content-Type"},
		{name: "malformed JSON", status: http.StatusOK, contentType: "application/json", body: `{`, want: "invalid JSON"},
		{name: "multiple JSON", status: http.StatusOK, contentType: "application/json", body: `{} {}`, want: "invalid JSON"},
		{name: "missing items", status: http.StatusOK, contentType: "application/json", body: `{}`, want: "missing items"},
		{name: "empty next cursor", status: http.StatusOK, contentType: "application/json", body: `{"items":[],"next_cursor":""}`, want: "invalid next cursor"},
		{name: "invalid agent", status: http.StatusOK, contentType: "application/json", body: `{"items":[{"agent_id":"bad"}],"next_cursor":null}`, want: "invalid agent"},
		{name: "structured API error", status: http.StatusBadRequest, contentType: "application/json",
			body: fmt.Sprintf(`{"error":{"code":"invalid_%s","message":"bad %s"}}`, token, token), want: "[REDACTED]"},
		{name: "unstructured API error", status: http.StatusInternalServerError, contentType: "application/json", body: `{}`, want: "HTTP 500"},
		{name: "oversized body", status: http.StatusOK, contentType: "application/json", body: strings.Repeat("x", maxRemoteBodyBytes+1), want: "too large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if test.contentType != "" {
					w.Header().Set("Content-Type", test.contentType)
				}
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			err := runRemoteCommand([]string{"agent", "list", "--server", server.URL, "--allow-insecure-http",
				"--control-token-file", tokenFile}, remoteClientOptions{}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), token) || requests.Load() != 1 {
				t.Fatalf("requests=%d error=%v", requests.Load(), err)
			}
		})
	}
}

func TestLoadControlTokenReturnsCanonicalToken(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	got, err := loadControlToken(tokenFile)
	if err != nil || got != token {
		t.Fatalf("token=%q error=%v", got, err)
	}
}

func remoteTestTokenFile(t *testing.T) (string, string) {
	t.Helper()
	token, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return token, path
}

func remoteJSONServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
}

func TestRemoteQueryEscapesOpaqueCursor(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	cursor := "a/b+c== value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("cursor") != cursor {
			t.Errorf("cursor=%q raw=%q", request.URL.Query().Get("cursor"), request.URL.RawQuery)
		}
		if _, err := url.ParseQuery(request.URL.RawQuery); err != nil {
			t.Errorf("query=%q error=%v", request.URL.RawQuery, err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":null}`)
	}))
	defer server.Close()
	if err := runRemoteCommand([]string{"agent", "list", "--server", server.URL, "--allow-insecure-http",
		"--control-token-file", tokenFile, "--cursor", cursor}, remoteClientOptions{}, io.Discard); err != nil {
		t.Fatal(err)
	}
}
