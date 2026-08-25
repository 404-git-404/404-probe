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

	"404-probe/internal/protocol"
)

const remoteTestJobID = "22222222222222222222222222222222"

func TestRemoteProbeListFiltersTableAndSummaryBoundary(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		wantQuery := "agent_id=" + remoteTestAgentBID + "&created_after=100&created_before=200&cursor=page&finished_after=300&finished_before=400&limit=3&probe_type=http&schedule_id=" + remoteTestScheduleID + "&status=finished&success=true"
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/control/jobs" || request.URL.RawQuery != wantQuery {
			t.Errorf("request=%s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"items":[%s],"next_cursor":"next","config":{"secret":true}}`, remoteTestJobSummaryJSON())
	}))
	defer server.Close()
	args := []string{"probe", "list", "--server", server.URL, "--allow-insecure-http", "--control-token-file", tokenFile,
		"--agent-id", remoteTestAgentBID, "--schedule-id", remoteTestScheduleID, "--probe-type", "http", "--status", "finished",
		"--success", "true", "--created-after", "100", "--created-before", "200", "--finished-after", "300", "--finished-before", "400",
		"--limit", "3", "--cursor", "page"}
	var output bytes.Buffer
	if err := runRemoteCommand(args, remoteClientOptions{}, &output); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{remoteTestJobID, remoteTestAgentBID, remoteTestScheduleID, "finished", "true", "12.5ms", "next_cursor: next"} {
		if !strings.Contains(output.String(), value) {
			t.Fatalf("missing %q:\n%s", value, output.String())
		}
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), token) || requests.Load() != 1 {
		t.Fatalf("requests=%d output=%q", requests.Load(), output.String())
	}

	var jsonOutput bytes.Buffer
	if err := runRemoteCommand(append(append([]string{}, args...), "--json"), remoteClientOptions{}, &jsonOutput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jsonOutput.String(), `"config"`) || !strings.Contains(jsonOutput.String(), `"result_summary"`) {
		t.Fatalf("JSON=%s", jsonOutput.String())
	}
}

func TestRemoteProbeGetTypedDetailTableAndJSON(t *testing.T) {
	token, tokenFile := remoteTestTokenFile(t)
	jobID := strings.Repeat("3", 64)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.EscapedPath() != "/api/v1/control/jobs/"+jobID || request.URL.RawQuery != "" {
			t.Errorf("URL=%s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, remoteTestJobDetailJSON(jobID, "http", `{"url":"https://example.com/health","method":"HEAD"}`,
			`{"dns_ms":1,"connect_ms":2,"tls_ms":3,"ttfb_ms":4,"total_ms":5,"status_code":204,"body_bytes":0,"body_truncated":false}`))
	}))
	defer server.Close()
	common := []string{"probe", "get", jobID, "--server", server.URL, "--allow-insecure-http", "--control-token-file", tokenFile}
	var table bytes.Buffer
	if err := runRemoteCommand(common, remoteClientOptions{}, &table); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"PROBE ID", jobID, "CONFIG", "https://example.com/health", "RESULT", "status_code", "204"} {
		if !strings.Contains(table.String(), value) {
			t.Fatalf("missing %q:\n%s", value, table.String())
		}
	}
	var jsonOutput bytes.Buffer
	if err := runRemoteCommand(append(common, "--json"), remoteClientOptions{}, &jsonOutput); err != nil {
		t.Fatal(err)
	}
	var detail map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &detail); err != nil || detail["job_id"] != jobID || detail["result"] == nil {
		t.Fatalf("JSON=%s error=%v", jsonOutput.String(), err)
	}
	if strings.Contains(table.String()+jsonOutput.String(), token) || requests.Load() != 2 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestRemoteProbeCommandValidationAndIsolation(t *testing.T) {
	_, tokenFile := remoteTestTokenFile(t)
	base := []string{"probe", "list", "--server", "https://example.com", "--control-token-file", tokenFile}
	tests := [][]string{
		append(append([]string{}, base...), "--db", "x"), append(append([]string{}, base...), "--token", "x"), append(append([]string{}, base...), "extra"),
		append(append([]string{}, base...), "--agent-id="), append(append([]string{}, base...), "--agent-id", "bad"),
		append(append([]string{}, base...), "--schedule-id", "bad"), append(append([]string{}, base...), "--probe-type", "dns"),
		append(append([]string{}, base...), "--status", "running"), append(append([]string{}, base...), "--success", "1"),
		append(append([]string{}, base...), "--created-after", "0"), append(append([]string{}, base...), "--created-after", "2", "--created-before", "1"),
		append(append([]string{}, base...), "--status", "queued", "--success", "true"),
		append(append([]string{}, base...), "--status", "leased", "--finished-after", "1"), append(append([]string{}, base...), "--limit", "101"),
		{"probe", "get", "bad"}, {"probe", "get", strings.Repeat("A", 32)}, {"probe", "get", remoteTestJobID, "extra"},
	}
	for index, args := range tests {
		if err := runRemoteCommand(args, remoteClientOptions{}, io.Discard); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}

func TestNormalizeRemoteJobSupportsEveryTypedMeasurement(t *testing.T) {
	tests := []struct{ probeType, config, measurement string }{
		{"http", `{"url":"https://example.com","method":"GET"}`, `{"dns_ms":1,"connect_ms":2,"tls_ms":3,"ttfb_ms":4,"total_ms":5,"status_code":200,"body_bytes":10,"body_truncated":false}`},
		{"tcp_connect", `{"host":"example.com","port":443}`, `{"connect_ms":2}`},
		{"icmp_ping", `{"target":"192.0.2.1","count":2}`, `{"sent":2,"received":2,"packet_loss_percent":0,"latency_min_ms":1,"latency_avg_ms":2,"latency_max_ms":3}`},
	}
	for _, test := range tests {
		t.Run(test.probeType, func(t *testing.T) {
			var job remoteJobView
			if err := json.Unmarshal([]byte(remoteTestJobDetailJSON(remoteTestJobID, test.probeType, test.config, test.measurement)), &job); err != nil {
				t.Fatal(err)
			}
			if err := normalizeRemoteJob(&job); err != nil {
				t.Fatal(err)
			}
			if job.ProbeType != protocol.ProbeType(test.probeType) {
				t.Fatalf("type=%s", job.ProbeType)
			}
		})
	}
}

func TestRemoteProbeRejectsInvalidStateAndMeasurement(t *testing.T) {
	finished := int64(1700000002000)
	bad := remoteJobSummaryView{JobID: remoteTestJobID, AgentID: remoteTestAgentBID, ProbeType: protocol.ProbeTypeHTTP, TimeoutMS: 5000,
		CreatedAt: 1700000000000, NotBefore: 1700000000000, ExpiresAt: 1700000060000, Status: "finished", Attempt: 1, FinishedAt: &finished}
	if err := validateRemoteJobSummary(bad); err == nil {
		t.Fatal("missing summary accepted")
	}
	var job remoteJobView
	body := remoteTestJobDetailJSON(remoteTestJobID, "http", `{"url":"https://example.com","method":"GET"}`, `{"status_code":700}`)
	if err := json.Unmarshal([]byte(body), &job); err != nil {
		t.Fatal(err)
	}
	if err := normalizeRemoteJob(&job); err == nil || !strings.Contains(err.Error(), "measurement") {
		t.Fatalf("error=%v", err)
	}
}

func remoteTestJobSummaryJSON() string {
	return fmt.Sprintf(`{"job_id":%q,"schedule_id":%q,"agent_id":%q,"probe_type":"http","timeout_ms":5000,"created_at":1700000000000,"scheduled_for":1700000000000,"not_before":1700000000000,"expires_at":1700000060000,"status":"finished","attempt":1,"lease":null,"finished_at":1700000002000,"result_summary":{"success":true,"duration_ms":12.5,"error_category":null,"finished_at":1700000001500}}`, remoteTestJobID, remoteTestScheduleID, remoteTestAgentBID)
}

func remoteTestJobDetailJSON(jobID, probeType, config, measurement string) string {
	return fmt.Sprintf(`{"job_id":%q,"agent_id":%q,"probe_type":%q,"config":%s,"timeout_ms":5000,"created_at":1700000000000,"not_before":1700000000000,"expires_at":1700000060000,"status":"finished","attempt":1,"lease":null,"finished_at":1700000002000,"result":{"received_at":1700000002000,"started_at":1700000001000,"finished_at":1700000001500,"duration_ms":500,"success":true,"resolved_ip":"192.0.2.1","error_category":null,"error_message":null,"measurement":%s},"lease_token":"ignored"}`,
		jobID, remoteTestAgentBID, probeType, config, measurement)
}
