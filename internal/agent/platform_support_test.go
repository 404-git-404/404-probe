package agent

import (
	"404-probe/internal/platformsupport"
	"404-probe/internal/protocol"
	"404-probe/internal/updater"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type platformMetrics struct{}

type platformResponseBody struct {
	io.ReadCloser
	done chan struct{}
}

func (b platformResponseBody) Close() error { err := b.ReadCloser.Close(); close(b.done); return err }

type platformTransport struct {
	base http.RoundTripper
	done chan struct{}
}

func (p platformTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := p.base.RoundTrip(r)
	if err == nil && r.URL.Path == "/api/v1/agent/security" {
		response.Body = platformResponseBody{response.Body, p.done}
	}
	return response, err
}

func (platformMetrics) Collect(context.Context) (protocol.Report, error) {
	return protocol.Report{Hostname: "isolated", OS: "Alpine 3.24.2", Arch: "amd64", BootID: "boot", RAMTotal: 1, DiskTotal: 1}, nil
}

func TestAlpineActualReportWorkerAndDeferredCycles(t *testing.T) {
	for _, serverSecurity := range []bool{true, false} {
		t.Run(map[bool]string{true: "server-supported", false: "server-unsupported"}[serverSecurity], func(t *testing.T) {
			dir := t.TempDir()
			export := filepath.Join(dir, "export")
			ack := filepath.Join(dir, "ack")
			if err := os.Mkdir(export, 0700); err != nil {
				t.Fatal(err)
			}
			old := []byte("untrusted old export: must not parse")
			for _, p := range []string{ack, filepath.Join(export, "old.json")} {
				if err := os.WriteFile(p, old, 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Unix sockets have a short address limit, including on Windows.
			socketDir, err := os.MkdirTemp("", "os1b-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
			socket := filepath.Join(socketDir, "old.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal("stale socket fixture: ", err)
			}
			defer listener.Close()
			var claims, posts atomic.Int32
			received := make(chan protocol.SecuritySubmission, 2)
			reports := make(chan protocol.Report, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/report":
					var body protocol.Report
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					reports <- body
					_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{AgentVersionReport: true, ManagementReport: true, AgentUpgrade: true, Security: serverSecurity}})
				case "/api/v1/agent/security":
					posts.Add(1)
					var body protocol.SecuritySubmission
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if err := body.Validate(); err != nil {
						t.Error(err)
					}
					received <- body
					_ = json.NewEncoder(w).Encode(protocol.SecurityResponse{Accepted: true})
				default:
					claims.Add(1)
					http.Error(w, "unexpected claim", 500)
				}
			}))
			defer server.Close()
			r, err := newWithPlatform(Config{ServerURL: server.URL, AgentID: "agent", Token: "token", Interval: time.Hour, Timeout: time.Second, StatePath: filepath.Join(dir, "epoch"), AllowInsecureHTTP: true, UpdaterSocket: socket, SecurityExportDir: export, SecurityAckPath: ack, SecurityInterval: time.Hour, EnableRemoteRemoval: true, AgentVersion: "v1.0.0"}, nil, NewProbeExecutor(), platformsupport.Policy{DeferredUnsupported: true})
			if err != nil {
				t.Fatal(err)
			}
			r.collector = platformMetrics{}
			securityFinished := make(chan struct{})
			r.client.Transport = platformTransport{base: http.DefaultTransport, done: securityFinished}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var seq uint64
			for i := 0; i < 2; i++ {
				if ok, err := r.sendReport(ctx, &seq); err != nil || !ok {
					t.Fatalf("report %v %v", ok, err)
				}
			}
			<-reports
			reported := <-reports
			if reported.AgentUpgradeCapable || reported.Management == nil || reported.Management.RemoteRemoval || r.upgradeAPISupported.Load() || r.updaterAvailable() {
				t.Fatalf("advertised deferred capability: %+v", reported)
			}
			r.upgradeAPISupported.Store(true)
			r.managementAPISupported.Store(true)
			if r.runUpgradeCycle(ctx, upgradeHTTPClient{baseURL: server.URL, client: server.Client()}, updater.Client{Socket: socket}) {
				t.Fatal("upgrade active")
			}
			if r.runRemovalCycle(ctx, agentRemovalHTTPClient{baseURL: server.URL, client: server.Client()}, updater.Client{Socket: socket}) || r.updaterSupportsRemoteRemoval(ctx) {
				t.Fatal("removal active")
			}
			r.runUpgradeWorker(ctx)
			r.runRemovalWorker(ctx)
			done := make(chan struct{})
			go func() { defer close(done); r.runSecurityWorker(ctx) }()
			if serverSecurity {
				select {
				case got := <-received:
					if got.Status != protocol.SecurityStatusUnavailable || got.Reason != "platform_unsupported" || got.Batch != nil || got.Delivery != nil {
						t.Fatalf("wrong status %+v", got)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("report did not unlock security worker")
				}
				select {
				case <-securityFinished:
				case <-time.After(time.Second):
					t.Fatal("security response was not consumed")
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("worker did not exit")
			}
			if claims.Load() != 0 || posts.Load() != map[bool]int32{true: 1, false: 0}[serverSecurity] {
				t.Fatalf("claims=%d security=%d", claims.Load(), posts.Load())
			}
			for _, p := range []string{ack, filepath.Join(export, "old.json")} {
				data, err := os.ReadFile(p)
				if err != nil || !bytes.Equal(data, old) {
					t.Fatalf("old file changed %s", p)
				}
			}
		})
	}
}

func TestAlpineOldServerReasonFailureIsNotSuccess(t *testing.T) {
	var logs bytes.Buffer
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unknown reason", 400) }))
	defer s.Close()
	r := &Runner{deferredUnsupported: true, config: Config{ServerURL: s.URL, SecurityExportDir: filepath.Join(t.TempDir(), "must-not-create")}, client: s.Client(), logger: slog.New(slog.NewTextHandler(&logs, nil)), epoch: 1, sessionID: "session"}
	r.uploadSecurity(context.Background())
	if calls.Load() != 1 || !bytes.Contains(logs.Bytes(), []byte("publish platform security status failed")) {
		t.Fatalf("calls=%d logs=%s", calls.Load(), logs.String())
	}
	if _, err := os.Stat(r.config.SecurityExportDir); !os.IsNotExist(err) {
		t.Fatal("created export")
	}
}
