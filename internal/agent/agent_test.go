package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

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
