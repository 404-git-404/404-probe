package agent

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestStartupDiskFailureReportsOnceAndHoldsRestart(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != protocol.StartupDiskFailurePath ||
			r.Header.Get("Authorization") != "Bearer synthetic-token" || r.ContentLength != 0 {
			t.Error("unexpected startup failure request")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	blocked := filepath.Join(t.TempDir(), "blocked-directory")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	config := Config{ServerURL: server.URL, AgentID: "synthetic-agent", Token: "synthetic-token",
		Interval: time.Second, Timeout: time.Second, AllowInsecureHTTP: true, StatePath: filepath.Join(blocked, "state")}
	_, err := New(config, slog.Default())
	if err == nil {
		t.Fatal("started with an unusable state directory")
	}
	interval := 40 * time.Millisecond
	started := time.Now()
	reportStartupFailure(t.Context(), config, slog.Default(), err, interval)
	if got := requests.Load(); got != 1 || time.Since(started) < interval {
		t.Fatalf("alert requests=%d after %v, want one after at least %v", got, time.Since(started), interval)
	}

	config.StatePath = filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(config.StatePath, []byte("invalid epoch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = New(config, slog.Default())
	if err == nil {
		t.Fatal("started with an invalid epoch")
	}
	reportStartupFailure(t.Context(), config, slog.Default(), err, interval)
	if got := requests.Load(); got != 1 {
		t.Fatalf("invalid epoch sent a disk alert, requests=%d", got)
	}
}
