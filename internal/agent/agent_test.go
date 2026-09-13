package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
	appserver "404-probe/internal/server"
	"404-probe/internal/storage"
)

type reportCollectorFunc func(context.Context) (protocol.Report, error)

func (f reportCollectorFunc) Collect(ctx context.Context) (protocol.Report, error) { return f(ctx) }

func staticReportCollector(context.Context) (protocol.Report, error) {
	return protocol.Report{}, nil
}

func TestConfigRequiresHTTPSUnlessExplicitlyAllowed(t *testing.T) {
	base := Config{ServerURL: "http://example.test", AgentID: "id", Token: "token", Interval: 10 * time.Second, Timeout: 5 * time.Second, StatePath: filepath.Join(t.TempDir(), "epoch")}
	if base.Validate() == nil {
		t.Fatal("plain HTTP should be rejected")
	}
	base.AllowInsecureHTTP = true
	if err := base.Validate(); err != nil {
		t.Fatalf("explicit insecure HTTP rejected: %v", err)
	}
	base.ServerURL = "https://example.test"
	base.AllowInsecureHTTP = false
	if err := base.Validate(); err != nil {
		t.Fatalf("HTTPS rejected: %v", err)
	}
	base.CountryCode = "ZZ"
	if base.Validate() == nil {
		t.Fatal("invalid persisted country code accepted")
	}
	base.CountryCode = "US"
	if err := base.Validate(); err != nil {
		t.Fatalf("valid persisted country code rejected: %v", err)
	}
}

func TestPersistentEpochIncrementsAcrossRunnerStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent", "epoch")
	config := Config{ServerURL: "https://example.test", AgentID: "id", Token: "token", Interval: time.Second, Timeout: time.Second, StatePath: path}
	first, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.epoch != 1 || second.epoch != 2 {
		t.Fatalf("epochs=%d,%d", first.epoch, second.epoch)
	}
	if first.sessionID == second.sessionID {
		t.Fatal("separate epochs reused a session ID")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "2\n" {
		t.Fatalf("persisted state=%q err=%v", data, err)
	}
}

func TestPersistentEpochRejectsCorruptionAndOverflow(t *testing.T) {
	for name, contents := range map[string]string{"empty": "", "corrupt": "not-a-number\n", "overflow": "18446744073709551615\n"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "epoch")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := nextEpoch(path); err == nil {
				t.Fatal("invalid persistent epoch was accepted")
			}
		})
	}
}

func TestPersistentEpochConcurrentStartsAreUnique(t *testing.T) {
	const starts = 24
	path := filepath.Join(t.TempDir(), "epoch")
	epochs := make(chan uint64, starts)
	errors := make(chan error, starts)
	var wg sync.WaitGroup
	for range starts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			epoch, err := nextEpoch(path)
			if err != nil {
				errors <- err
				return
			}
			epochs <- epoch
		}()
	}
	wg.Wait()
	close(errors)
	close(epochs)
	for err := range errors {
		t.Fatal(err)
	}
	got := make([]uint64, 0, starts)
	for epoch := range epochs {
		got = append(got, epoch)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i, epoch := range got {
		if want := uint64(i + 1); epoch != want {
			t.Fatalf("epochs=%v; index %d got %d want %d", got, i, epoch, want)
		}
	}
}

func TestPostParsesReportResponse(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantAccept bool
		wantReason string
		wantError  bool
	}{
		{name: "accepted", body: `{"accepted":true}`, wantAccept: true},
		{name: "rejected", body: `{"accepted":false,"reason":"stale epoch"}`, wantReason: "stale epoch"},
		{name: "malformed", body: `{"accepted":`, wantError: true},
		{name: "missing accepted", body: `{}`, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			runner, err := New(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := runner.post(context.Background(), protocol.Report{})
			if (err != nil) != tt.wantError {
				t.Fatalf("response=%+v err=%v wantError=%t", response, err, tt.wantError)
			}
			if err == nil && (response.Accepted != tt.wantAccept || response.Reason != tt.wantReason) {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}

func TestVersionReportWaitsForServerCapability(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accepted":true,"capabilities":{"agent_version_report":true,"agent_upgrade":true}}`)
	}))
	defer server.Close()
	runner, err := New(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second,
		AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch"), AgentVersion: "v0.8.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(staticReportCollector)
	sequence := uint64(0)
	if _, err := runner.sendReport(context.Background(), &sequence); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.sendReport(context.Background(), &sequence); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies=%d", len(bodies))
	}
	if _, exists := bodies[0]["agent_version"]; exists {
		t.Fatalf("first report sent unnegotiated version: %v", bodies[0])
	}
	if bodies[1]["agent_version"] != "v0.8.0" || !runner.upgradeAPISupported.Load() || !runner.versionReportAccepted.Load() {
		t.Fatalf("second report=%v upgrade=%t accepted=%t", bodies[1], runner.upgradeAPISupported.Load(), runner.versionReportAccepted.Load())
	}
}

func TestNewReportFieldsWaitForServerCapabilities(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{LinuxMetricsReport: true, CountryCodeReport: true}})
	}))
	defer server.Close()
	runner, err := New(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch"), CountryCode: "US"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(func(ctx context.Context) (protocol.Report, error) {
		report, err := staticReportCollector(ctx)
		steal, readRate, writeRate, busy := 2.5, 1024.0, 2048.0, 40.0
		report.CPUCores = 4
		report.CPUStealPercent, report.DiskReadRate, report.DiskWriteRate, report.DiskBusyPercent = &steal, &readRate, &writeRate, &busy
		return report, err
	})
	sequence := uint64(0)
	if _, err := runner.sendReport(context.Background(), &sequence); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.sendReport(context.Background(), &sequence); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies=%d", len(bodies))
	}
	for _, field := range []string{"cpu_cores", "cpu_steal_percent", "disk_read_rate", "disk_write_rate", "disk_busy_percent", "country_code"} {
		if _, exists := bodies[0][field]; exists {
			t.Fatalf("first report sent unnegotiated %s: %v", field, bodies[0])
		}
	}
	if bodies[1]["country_code"] != "US" || bodies[1]["cpu_cores"] != float64(4) || bodies[1]["cpu_steal_percent"] != 2.5 || bodies[1]["disk_read_rate"] != 1024.0 || bodies[1]["disk_write_rate"] != 2048.0 || bodies[1]["disk_busy_percent"] != 40.0 {
		t.Fatalf("second report did not send negotiated fields: %v", bodies[1])
	}
}

func TestNewReportRemainsAcceptedByStrictV092Server(t *testing.T) {
	type legacyReport struct {
		AgentID             string  `json:"agent_id"`
		Epoch               uint64  `json:"epoch"`
		SessionID           string  `json:"session_id"`
		Sequence            uint64  `json:"sequence"`
		CollectedAt         int64   `json:"collected_at"`
		Hostname            string  `json:"hostname"`
		OS                  string  `json:"os"`
		Arch                string  `json:"arch"`
		AgentVersion        string  `json:"agent_version,omitempty"`
		AgentUpgradeCapable bool    `json:"agent_upgrade_capable,omitempty"`
		BootID              string  `json:"boot_id"`
		Uptime              uint64  `json:"uptime"`
		CPUPercent          float64 `json:"cpu_percent"`
		Load1               float64 `json:"load1"`
		Load5               float64 `json:"load5"`
		Load15              float64 `json:"load15"`
		RAMUsed             uint64  `json:"ram_used"`
		RAMTotal            uint64  `json:"ram_total"`
		RAMPercent          float64 `json:"ram_percent"`
		SwapUsed            uint64  `json:"swap_used"`
		SwapTotal           uint64  `json:"swap_total"`
		SwapPercent         float64 `json:"swap_percent"`
		DiskUsed            uint64  `json:"disk_used"`
		DiskTotal           uint64  `json:"disk_total"`
		DiskPercent         float64 `json:"disk_percent"`
		RXBytes             uint64  `json:"rx_bytes"`
		TXBytes             uint64  `json:"tx_bytes"`
	}
	decodeErrors := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var report legacyReport
		if err := decoder.Decode(&report); err != nil {
			decodeErrors <- err
			http.Error(w, "invalid report", http.StatusBadRequest)
			return
		}
		decodeErrors <- nil
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true})
	}))
	defer server.Close()
	runner, err := New(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch"), CountryCode: "US"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(func(ctx context.Context) (protocol.Report, error) {
		report, err := staticReportCollector(ctx)
		steal, readRate, writeRate, busy := 2.5, 1024.0, 2048.0, 40.0
		report.CPUCores = 4
		report.CPUStealPercent, report.DiskReadRate, report.DiskWriteRate, report.DiskBusyPercent = &steal, &readRate, &writeRate, &busy
		return report, err
	})
	sequence := uint64(0)
	for i := 0; i < 2; i++ {
		accepted, err := runner.sendReport(context.Background(), &sequence)
		if err != nil || !accepted {
			t.Fatalf("report %d accepted=%t err=%v", i+1, accepted, err)
		}
		if err := <-decodeErrors; err != nil {
			t.Fatalf("strict v0.9.2 decoder rejected report %d: %v", i+1, err)
		}
	}
}

func TestInvalidAutomaticCountryFallsBackToUnknown(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{CountryCodeReport: true}})
	}))
	defer server.Close()
	runner, err := New(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(func(ctx context.Context) (protocol.Report, error) {
		report, err := staticReportCollector(ctx)
		report.CountryCode = "ZZ"
		return report, err
	})
	sequence := uint64(0)
	for i := 0; i < 2; i++ {
		if accepted, err := runner.sendReport(context.Background(), &sequence); err != nil || !accepted {
			t.Fatalf("report %d accepted=%t err=%v", i+1, accepted, err)
		}
	}
	if _, exists := bodies[1]["country_code"]; exists {
		t.Fatalf("invalid automatic country was reported instead of unknown: %v", bodies[1])
	}
}

func TestGoogleStatusNegotiationResetsWhenServerDowngrades(t *testing.T) {
	responses := []string{`{"accepted":true}`, `{"accepted":true,"capabilities":{"google_status":true}}`, `{"accepted":true}`}
	index := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, exists := body["google_status"]; exists {
			t.Error("new report field must never be sent")
		}
		_, _ = io.WriteString(w, responses[index])
		index++
	}))
	defer server.Close()
	runner, err := New(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second,
		AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(staticReportCollector)
	sequence := uint64(0)
	for _, want := range []bool{false, true, false} {
		if _, err := runner.sendReport(context.Background(), &sequence); err != nil {
			t.Fatal(err)
		}
		if got := runner.googleStatusSupported.Load(); got != want {
			t.Fatalf("negotiation=%t want=%t", got, want)
		}
	}
}

func TestPostClassifiesOnlyExplicitLifecycleSignals(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantRevoked  bool
		wantDisabled bool
	}{
		{name: "revoked", status: http.StatusUnauthorized, body: `{"error":{"code":"agent_revoked","message":"agent credential has been revoked"}}`, wantRevoked: true},
		{name: "disabled", status: http.StatusLocked, body: `{"error":{"code":"agent_disabled","message":"agent has been disabled"}}`, wantDisabled: true},
		{name: "unknown token", status: http.StatusUnauthorized, body: `{"error":"invalid agent token"}`},
		{name: "server error cannot revoke", status: http.StatusInternalServerError, body: `{"error":{"code":"agent_revoked"}}`},
		{name: "wrong status cannot disable", status: http.StatusUnauthorized, body: `{"error":{"code":"agent_disabled"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			runner, err := New(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Second,
				Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.post(context.Background(), protocol.Report{})
			if errors.Is(err, ErrAgentRevoked) != tt.wantRevoked || errors.Is(err, ErrAgentDisabled) != tt.wantDisabled {
				t.Fatalf("error=%v want revoked=%t disabled=%t", err, tt.wantRevoked, tt.wantDisabled)
			}
		})
	}
}

func TestRunStopsCleanlyAndLogsOnceAfterRevocation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"agent_revoked","message":"agent credential has been revoked"}}`)
	}))
	defer server.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	runner, err := NewWithExecutor(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Hour,
		Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, logger, UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(staticReportCollector)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run returned revoked failure: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("report requests=%d, want 1", requests.Load())
	}
	if count := strings.Count(logs.String(), "agent credential has been revoked; stopping"); count != 1 {
		t.Fatalf("stop log count=%d logs=%s", count, logs.String())
	}
}

func TestDisabledWaitSurvivesTransientFailureWithoutFlooding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		request := requests.Add(1)
		if request == 2 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusLocked)
		_, _ = io.WriteString(w, `{"error":{"code":"agent_disabled"}}`)
		if request == 4 {
			cancel()
		}
	}))
	defer server.Close()
	runner, err := NewWithExecutor(Config{
		ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Hour,
		DisabledInterval: 15 * time.Millisecond, Timeout: time.Second, AllowInsecureHTTP: true,
		StatePath: filepath.Join(t.TempDir(), "epoch"),
	}, nil, UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(staticReportCollector)
	started := time.Now()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("transient disabled wait stopped Agent: %v", err)
	}
	if requests.Load() != 4 || time.Since(started) < 40*time.Millisecond {
		t.Fatalf("disabled requests=%d elapsed=%s", requests.Load(), time.Since(started))
	}
}

func TestRunReportsRetriesTransientTransportFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "connection reset", err: errors.New("connection reset")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			runner, err := New(Config{ServerURL: "https://example.test", AgentID: "agent", Token: "token", Interval: time.Millisecond,
				Timeout: time.Second, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			runner.collector = reportCollectorFunc(staticReportCollector)
			runner.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if requests.Add(1) == 2 {
					cancel()
				}
				return nil, test.err
			})}
			if err := runner.runReports(ctx); err != nil {
				t.Fatalf("transient error stopped reports: %v", err)
			}
			if requests.Load() < 2 {
				t.Fatalf("requests=%d, want retry", requests.Load())
			}
		})
	}
}

func TestRunReportsRetriesServerErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 2 {
			cancel()
		}
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	runner, err := New(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Millisecond,
		Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(staticReportCollector)
	if err := runner.runReports(ctx); err != nil {
		t.Fatalf("server error stopped reports: %v", err)
	}
	if requests.Load() < 2 {
		t.Fatalf("requests=%d, want retry", requests.Load())
	}
}

func TestRevocationEndToEndStopsAgentAndPreservesState(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "revocation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agentID, err := auth.NewID()
	if err != nil {
		t.Fatal(err)
	}
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddAgent(ctx, agentID, "smoke", tokenHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	app, err := appserver.NewApp(store, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown()
	accepted := make(chan struct{}, 1)
	handler := app.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		for name, values := range response.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
		if request.URL.Path == "/api/v1/report" && response.Code == http.StatusOK {
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}))
	defer server.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	runner, err := NewWithExecutor(Config{ServerURL: server.URL, AgentID: agentID, Token: token, Interval: 5 * time.Millisecond,
		Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, logger, UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(func(context.Context) (protocol.Report, error) {
		return protocol.Report{Hostname: "smoke-host", OS: "linux", Arch: "amd64", BootID: "boot", Uptime: 1,
			CPUPercent: 1, Load1: 1, Load5: 1, Load15: 1, RAMUsed: 1, RAMTotal: 2, RAMPercent: 50,
			DiskUsed: 1, DiskTotal: 2, DiskPercent: 50, RXBytes: 1, TXBytes: 1}, nil
	})
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent did not complete its initial report")
	}
	if revoked, err := store.RevokeAgent(ctx, agentID, time.Now()); err != nil || !revoked {
		t.Fatalf("revoke=%t err=%v", revoked, err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Agent did not cleanly exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Agent continued reporting after revocation")
	}
	if count := strings.Count(logs.String(), "agent credential has been revoked; stopping"); count != 1 {
		t.Fatalf("stop log count=%d logs=%s", count, logs.String())
	}
	snapshot, err := store.GetAgentSnapshot(ctx, agentID, time.Now(), 30*time.Second)
	if err != nil || !snapshot.Agent.Revoked || snapshot.State == nil || snapshot.State.Hostname != "smoke-host" {
		t.Fatalf("retained snapshot=%+v err=%v", snapshot, err)
	}
}

type lifecycleExecutor struct{ starts *atomic.Int32 }

func TestLegacyClashSecretLogsFixedMessageAndKeepsTelemetry(t *testing.T) {
	var reports atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/report" {
			reports.Add(1)
			_, _ = io.WriteString(w, `{"accepted":true}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var logs bytes.Buffer
	runner, err := NewWithExecutor(Config{
		ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: 5 * time.Millisecond,
		Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch"),
		ClashAPIURL: DefaultClashAPIURL, LegacyClashSecretConfigured: true,
	}, slog.New(slog.NewTextHandler(&logs, nil)), UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(func(context.Context) (protocol.Report, error) {
		return protocol.Report{Hostname: "host", OS: "linux", Arch: "amd64", BootID: "boot", Uptime: 1,
			CPUPercent: 1, Load1: 1, Load5: 1, Load15: 1, RAMUsed: 1, RAMTotal: 2, RAMPercent: 50,
			DiskUsed: 1, DiskTotal: 2, DiskPercent: 50, RXBytes: 1, TXBytes: 1}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	waitForAgentCondition(t, "legacy telemetry", func() bool { return reports.Load() > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "legacy Clash API secret configuration is unsupported; remove it") {
		t.Fatalf("logs=%s", logs.String())
	}
}

func TestInteractiveCapabilityCanDowngradeToLegacyLane(t *testing.T) {
	var reports atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enabled := reports.Add(1) == 1
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{InteractiveControl: enabled}})
	}))
	defer server.Close()
	runner, err := NewWithExecutor(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Second, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch")}, nil, UnsupportedExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(func(context.Context) (protocol.Report, error) {
		return protocol.Report{Hostname: "host", OS: "linux", Arch: "amd64", BootID: "boot", Uptime: 1, RAMTotal: 1, DiskTotal: 1}, nil
	})
	var sequence uint64
	if accepted, err := runner.sendReport(context.Background(), &sequence); err != nil || !accepted || !runner.interactiveControlSupported.Load() {
		t.Fatalf("first accepted=%t capability=%t err=%v", accepted, runner.interactiveControlSupported.Load(), err)
	}
	if accepted, err := runner.sendReport(context.Background(), &sequence); err != nil || !accepted || runner.interactiveControlSupported.Load() {
		t.Fatalf("second accepted=%t capability=%t err=%v", accepted, runner.interactiveControlSupported.Load(), err)
	}
}

func (e lifecycleExecutor) SupportedProbeTypes() []protocol.ProbeType {
	e.starts.Add(1)
	return []protocol.ProbeType{protocol.ProbeTypeTCPConnect}
}

func (lifecycleExecutor) Execute(context.Context, protocol.Job) (Execution, error) {
	return Execution{}, errors.New("unexpected job")
}

func TestDisableResumeAndRevokeLifecycleEndToEnd(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agentID, _ := auth.NewID()
	token, tokenHash, _ := auth.NewToken()
	if err := store.AddAgent(ctx, agentID, "lifecycle", tokenHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	app, err := appserver.NewApp(store, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown()

	var accepted, disabled, claims atomic.Int32
	handler := app.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		for name, values := range response.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
		switch {
		case request.URL.Path == "/api/v1/report" && response.Code == http.StatusOK:
			accepted.Add(1)
		case request.URL.Path == "/api/v1/report" && response.Code == http.StatusLocked:
			disabled.Add(1)
		case request.URL.Path == "/api/v1/agent/jobs/claim":
			claims.Add(1)
		}
	}))
	defer server.Close()
	var clashRequests atomic.Int32
	clashServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		clashRequests.Add(1)
		if request.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Clash authorization=%q", request.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"proxies":{"select":{"type":"Selector","name":"select","now":"a","all":["a","b"]}}}`)
	}))
	defer clashServer.Close()

	var workerStarts atomic.Int32
	runner, err := NewWithExecutor(Config{
		ServerURL: server.URL, AgentID: agentID, Token: token, Interval: 5 * time.Millisecond,
		JobInterval: 5 * time.Millisecond, DisabledInterval: 35 * time.Millisecond,
		Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(t.TempDir(), "epoch"),
		ClashAPIURL: clashServer.URL, OutboundInterval: 5 * time.Millisecond,
	}, nil, lifecycleExecutor{starts: &workerStarts})
	if err != nil {
		t.Fatal(err)
	}
	runner.collector = reportCollectorFunc(func(context.Context) (protocol.Report, error) {
		return protocol.Report{Hostname: "pause-host", OS: "linux", Arch: "amd64", BootID: "boot", Uptime: 1,
			CPUPercent: 1, Load1: 1, Load5: 1, Load15: 1, RAMUsed: 1, RAMTotal: 2, RAMPercent: 50,
			DiskUsed: 1, DiskTotal: 2, DiskPercent: 50, RXBytes: 1, TXBytes: 1}, nil
	})
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()
	waitForAgentCondition(t, "initial reporting and worker", func() bool {
		return accepted.Load() > 0 && claims.Load() > 0 && workerStarts.Load() == 1 && clashRequests.Load() > 0
	})

	if changed, err := store.DisableAgent(ctx, agentID, time.Now()); err != nil || !changed {
		t.Fatalf("disable changed=%t err=%v", changed, err)
	}
	waitForAgentCondition(t, "disabled report", func() bool { return disabled.Load() > 0 })
	time.Sleep(15 * time.Millisecond)
	pausedAccepted, pausedClaims, pausedClash := accepted.Load(), claims.Load(), clashRequests.Load()
	time.Sleep(15 * time.Millisecond)
	if accepted.Load() != pausedAccepted || claims.Load() != pausedClaims || clashRequests.Load() != pausedClash || workerStarts.Load() != 1 {
		t.Fatalf("work continued while disabled: accepted %d->%d claims %d->%d clash %d->%d starts=%d",
			pausedAccepted, accepted.Load(), pausedClaims, claims.Load(), pausedClash, clashRequests.Load(), workerStarts.Load())
	}
	select {
	case err := <-runDone:
		t.Fatalf("disabled Agent exited early: %v", err)
	default:
	}

	if changed, err := store.EnableAgent(ctx, agentID, time.Now()); err != nil || !changed {
		t.Fatalf("enable changed=%t err=%v", changed, err)
	}
	waitForAgentCondition(t, "automatic resume", func() bool {
		return accepted.Load() > pausedAccepted && claims.Load() > pausedClaims && clashRequests.Load() > pausedClash && workerStarts.Load() == 2
	})

	if changed, err := store.DisableAgent(ctx, agentID, time.Now()); err != nil || !changed {
		t.Fatalf("second disable changed=%t err=%v", changed, err)
	}
	disabledBefore := disabled.Load()
	waitForAgentCondition(t, "second disabled report", func() bool { return disabled.Load() > disabledBefore })
	if changed, err := store.RevokeAgent(ctx, agentID, time.Now()); err != nil || !changed {
		t.Fatalf("revoke changed=%t err=%v", changed, err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("revoked while disabled did not exit cleanly: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked while disabled Agent did not exit")
	}
	if workerStarts.Load() != 2 {
		t.Fatalf("worker starts=%d want exactly 2", workerStarts.Load())
	}
}

func waitForAgentCondition(t *testing.T, name string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", name)
}
