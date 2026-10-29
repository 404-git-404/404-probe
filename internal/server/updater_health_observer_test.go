package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"404-probe/internal/buildinfo"
	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

// Optional live acceptance observer, isolated from the installed Server. The
// caller supplies a private SQLite backup with the existing Agent credential
// hashes. No original database or administrator password is read or changed.
// Only metric reports and Web reads are served; no jobs/upgrades are launched.
func TestUpdaterHealthLiveObserver(t *testing.T) {
	path := os.Getenv("V110_OBSERVER_DB")
	if path == "" {
		t.Skip("live acceptance observer not requested")
	}
	root := os.Getenv("V110_OBSERVER_ROOT")
	if os.Geteuid() == 0 || root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(path) != root {
		t.Fatal("observer requires non-root private fixture database")
	}
	store, err := storage.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app, err := NewApp(store, 30e9, nil, WithWebAuthentication(WebAuthenticationConfig{PasswordHash: webTestPasswordHash(t), PublicOrigin: "http://127.0.0.1:19080", AllowInsecureHTTP: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown()
	// Synthetic panel target; payload download and upgrades are never performed.
	app.buildInfo = buildinfo.Info{Version: "v1.0.10", Commit: strings.Repeat("a", 40)}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/report" {
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, r)
			if recorder.Code == 200 {
				var response protocol.ReportResponse
				if json.Unmarshal(recorder.Body.Bytes(), &response) != nil {
					http.Error(w, "invalid observation response", 500)
					return
				}
				response.NetworkQuality = nil
				response.Capabilities = protocol.ReportCapabilities{AgentVersionReport: true, LinuxMetricsReport: true, UpdaterHealthReport: true, AgentUpgradeV2: true}
				writeJSON(w, 200, response)
				return
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/agent/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.URL.Path != "/login" && !(r.Method == http.MethodPost && r.URL.Path == "/api/v1/web/expenses/estimate") {
			http.Error(w, "observation only", http.StatusMethodNotAllowed)
			return
		}
		app.Handler().ServeHTTP(w, r)
	})
	t.Log("OBSERVATION_READY: loopback panel, synthetic test login, report-only Agent API")
	if err = http.ListenAndServe("127.0.0.1:19080", handler); err != nil {
		t.Fatal(err)
	}
}
