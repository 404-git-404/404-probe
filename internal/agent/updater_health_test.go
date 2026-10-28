package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/platformsupport"
	"404-probe/internal/protocol"
)

func TestUpdaterHealthOnlyReportedAfterNegotiation(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		reports := make(chan protocol.Report, 2)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var report protocol.Report
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Error(err)
			}
			reports <- report
			_ = json.NewEncoder(w).Encode(protocol.ReportResponse{Accepted: true, Capabilities: protocol.ReportCapabilities{AgentVersionReport: true, UpdaterHealthReport: advertised}})
		}))
		r, err := newWithPlatform(Config{ServerURL: server.URL, AgentID: "agent", Token: "synthetic", AgentVersion: "v1.0.9", Interval: time.Hour, Timeout: time.Second, StatePath: filepath.Join(t.TempDir(), "epoch"), AllowInsecureHTTP: true}, nil, NewProbeExecutor(), platformsupport.Policy{DeferredUnsupported: true})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		r.collector = platformMetrics{}
		var seq uint64
		for i := 0; i < 2; i++ {
			if ok, err := r.sendReport(context.Background(), &seq); err != nil || !ok {
				t.Fatal(ok, err)
			}
		}
		first, second := <-reports, <-reports
		if first.AgentUpdaterHealth != nil {
			t.Fatal("unnegotiated health sent")
		}
		if (second.AgentUpdaterHealth != nil) != advertised {
			t.Fatal(advertised, second.AgentUpdaterHealth)
		}
		if advertised && (second.AgentUpdaterHealth.Status != "unsupported" || second.AgentUpgradeCapable) {
			t.Fatal(second.AgentUpdaterHealth)
		}
		server.Close()
	}
}
