package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	remoteTestScheduleID = "11111111111111111111111111111111"
	remoteTestAgentBID   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestRemoteScheduleListRequestFiltersAndTable(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	nextCursor := "schedule_next"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/control/schedules" ||
			request.URL.RawQuery != "agent_id="+remoteTestAgentBID+"&cursor=page&enabled=false&limit=3&probe_type=http" {
			t.Errorf("request=%s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers=%v", request.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"items":[%s],"next_cursor":%q}`, remoteTestScheduleJSON(remoteTestScheduleID, remoteTestAgentBID), nextCursor)
	}))
	defer server.Close()

	var output bytes.Buffer
	err := runRemoteCommand([]string{"schedule", "list", "--server", server.URL, "--allow-insecure-http",
		"--control-token-file", tokenFile, "--agent-id", remoteTestAgentBID, "--enabled", "false",
		"--probe-type", "http", "--limit", "3", "--cursor", "page"}, remoteClientOptions{}, &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"ID", "NAME", "AGENT ID", remoteTestScheduleID, "homepage", remoteTestAgentBID,
		"http", "false", "next_cursor: " + nextCursor} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("output missing %q:\n%s", value, output.String())
		}
	}
	if strings.Contains(output.String(), "secret.example") || strings.Contains(output.String(), token) || requests.Load() != 1 {
		t.Fatalf("requests=%d output=%q", requests.Load(), output.String())
	}
}

func TestRemoteScheduleListJSONPreservesEnvelopeAndTypedConfig(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	server := remoteJSONServer(t, http.StatusOK,
		fmt.Sprintf(`{"items":[%s],"next_cursor":null,"ignored":"server-only"}`, remoteTestScheduleJSON(remoteTestScheduleID, remoteTestAgentBID)))
	defer server.Close()

	var output bytes.Buffer
	if err := runRemoteCommand([]string{"schedule", "list", "--server", server.URL, "--allow-insecure-http",
		"--control-token-file", tokenFile, "--json"}, remoteClientOptions{}, &output); err != nil {
		t.Fatal(err)
	}
	var collection map[string]any
	if err := json.Unmarshal(output.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	items, ok := collection["items"].([]any)
	if !ok || len(items) != 1 || collection["ignored"] != nil || collection["next_cursor"] != nil {
		t.Fatalf("JSON=%s", output.String())
	}
	item := items[0].(map[string]any)
	config := item["config"].(map[string]any)
	if item["schedule_id"] != remoteTestScheduleID || config["url"] != "https://secret.example/health" || config["method"] != "HEAD" {
		t.Fatalf("item=%v", item)
	}
}

func TestRemoteScheduleGetTableAndJSON(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.EscapedPath() != "/api/v1/control/schedules/"+remoteTestScheduleID || request.URL.RawQuery != "" {
			t.Errorf("URL=%s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, remoteTestScheduleJSON(remoteTestScheduleID, remoteTestAgentBID))
	}))
	defer server.Close()
	common := []string{"schedule", "get", remoteTestScheduleID, "--server", server.URL, "--allow-insecure-http", "--control-token-file", tokenFile}

	var table bytes.Buffer
	if err := runRemoteCommand(common, remoteClientOptions{}, &table); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"SCHEDULE ID", remoteTestScheduleID, "CONFIG", "https://secret.example/health", "TIMEOUT", "5000ms", "INTERVAL", "60s"} {
		if !strings.Contains(table.String(), value) {
			t.Fatalf("table missing %q:\n%s", value, table.String())
		}
	}

	var jsonOutput bytes.Buffer
	if err := runRemoteCommand(append(common, "--json"), remoteClientOptions{}, &jsonOutput); err != nil {
		t.Fatal(err)
	}
	var schedule map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &schedule); err != nil || schedule["schedule_id"] != remoteTestScheduleID || schedule["config"] == nil {
		t.Fatalf("JSON=%s error=%v", jsonOutput.String(), err)
	}
	if strings.Contains(table.String()+jsonOutput.String(), token) || requests.Load() != 2 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestRemoteScheduleCommandValidationAndIsolation(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	base := []string{"schedule", "list", "--server", "https://example.com", "--control-token-file", tokenFile}
	tests := []struct {
		name string
		args []string
	}{
		{name: "DB isolation", args: append(append([]string{}, base...), "--db", "x")},
		{name: "positional", args: append(append([]string{}, base...), "extra")},
		{name: "empty agent", args: append(append([]string{}, base...), "--agent-id=")},
		{name: "bad agent", args: append(append([]string{}, base...), "--agent-id", "bad")},
		{name: "empty enabled", args: append(append([]string{}, base...), "--enabled=")},
		{name: "bad enabled", args: append(append([]string{}, base...), "--enabled", "TRUE")},
		{name: "empty type", args: append(append([]string{}, base...), "--probe-type=")},
		{name: "bad type", args: append(append([]string{}, base...), "--probe-type", "dns")},
		{name: "low limit", args: append(append([]string{}, base...), "--limit", "0")},
		{name: "high limit", args: append(append([]string{}, base...), "--limit", "101")},
		{name: "empty cursor", args: append(append([]string{}, base...), "--cursor=")},
		{name: "large cursor", args: append(append([]string{}, base...), "--cursor", strings.Repeat("x", maxRemoteCursorBytes+1))},
		{name: "inline token", args: append(append([]string{}, base...), "--token", "secret")},
		{name: "get missing ID", args: []string{"schedule", "get", "--server", "https://example.com"}},
		{name: "get uppercase ID", args: []string{"schedule", "get", strings.Repeat("A", 32)}},
		{name: "get extra", args: []string{"schedule", "get", remoteTestScheduleID, "extra"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := runRemoteCommand(test.args, remoteClientOptions{}, io.Discard); err == nil {
				t.Fatal("invalid command accepted")
			}
		})
	}
}

func TestRemoteScheduleRejectsInvalidResponse(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing items", body: `{}`, want: "missing items"},
		{name: "empty cursor", body: `{"items":[],"next_cursor":""}`, want: "invalid next cursor"},
		{name: "bad identity", body: `{"items":[{"schedule_id":"bad"}],"next_cursor":null}`, want: "invalid schedule"},
		{name: "bad config", body: fmt.Sprintf(`{"items":[%s],"next_cursor":null}`, strings.Replace(remoteTestScheduleJSON(remoteTestScheduleID, remoteTestAgentBID), `"method":"HEAD"`, `"method":"POST"`, 1)), want: "invalid schedule config"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := remoteJSONServer(t, http.StatusOK, test.body)
			defer server.Close()
			err := runRemoteCommand([]string{"schedule", "list", "--server", server.URL, "--allow-insecure-http",
				"--control-token-file", tokenFile}, remoteClientOptions{}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestNormalizeRemoteScheduleSupportsEveryProbeType(t *testing.T) {
	tests := []struct {
		probeType string
		config    string
	}{
		{probeType: "http", config: `{"url":"https://example.com/health","method":"GET"}`},
		{probeType: "tcp_connect", config: `{"host":"example.com","port":443}`},
		{probeType: "icmp_ping", config: `{"target":"192.0.2.1","count":2}`},
	}
	for _, test := range tests {
		t.Run(test.probeType, func(t *testing.T) {
			body := strings.Replace(remoteTestScheduleJSON(remoteTestScheduleID, remoteTestAgentBID),
				`"probe_type":"http","config":{"url":"https://secret.example/health","method":"HEAD"}`,
				`"probe_type":"`+test.probeType+`","config":`+test.config, 1)
			var schedule remoteScheduleView
			if err := json.Unmarshal([]byte(body), &schedule); err != nil {
				t.Fatal(err)
			}
			if err := normalizeRemoteSchedule(&schedule); err != nil {
				t.Fatal(err)
			}
			if string(schedule.Config) != test.config {
				t.Fatalf("config=%s", schedule.Config)
			}
		})
	}
}

func remoteTestScheduleJSON(scheduleID, agentID string) string {
	return fmt.Sprintf(`{"schedule_id":%q,"agent_id":%q,"name":"homepage","probe_type":"http","config":{"url":"https://secret.example/health","method":"HEAD"},"timeout_ms":5000,"interval_seconds":60,"enabled":false,"next_run_at":1700000060000,"created_at":1700000000000,"updated_at":1700000001000,"ignored":"server-only"}`,
		scheduleID, agentID)
}
