package updater

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadMakesProgressBeyondOldTotalLimit(t *testing.T) {
	const window = 160 * time.Millisecond
	const oldTotal = 3 * window
	const block = "0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 15 {
			_, _ = fmt.Fprint(w, block)
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond)
		}
	}))
	defer server.Close()
	engine := &Engine{config: EngineConfig{
		HTTPClient: server.Client(), DownloadAttempts: 1, DownloadHeaderTimeout: 400 * time.Millisecond,
		DownloadStallWindow: window, DownloadMinBytesPerSecond: 100,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	got, err := engine.fetch(ctx, server.URL, 1024)
	if err != nil || string(got) != strings.Repeat(block, 15) || time.Since(started) <= oldTotal {
		t.Fatalf("download got %d bytes in %v, err=%v", len(got), time.Since(started), err)
	}
}

func TestDownloadRealStallResumesPartialBody(t *testing.T) {
	const prefix, suffix = "already downloaded", "remaining"
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch attempts.Add(1) {
		case 1:
			w.Header().Set("Content-Length", fmt.Sprint(len(prefix)+len(suffix)))
			w.Header().Set("ETag", `"asset"`)
			_, _ = fmt.Fprint(w, prefix)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case 2:
			if r.Header.Get("Range") != fmt.Sprintf("bytes=%d-", len(prefix)) || r.Header.Get("If-Range") != `"asset"` {
				t.Errorf("resume headers: %v", r.Header)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", len(prefix), len(prefix)+len(suffix)-1, len(prefix)+len(suffix)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = fmt.Fprint(w, suffix)
		default:
			t.Error("unexpected additional download attempt")
		}
	}))
	defer server.Close()
	engine, _, _, _, _ := testEngine(t, false, false, time.Second)
	engine.config.RetryBaseDelay = time.Millisecond
	if engine.config.DownloadStallWindow != 60*time.Second || engine.config.DownloadMinBytesPerSecond != 1024 {
		t.Fatal("production stall thresholds changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	started := time.Now()
	got, err := engine.fetch(ctx, server.URL, 1024)
	if err != nil || string(got) != prefix+suffix || attempts.Load() != 2 || time.Since(started) < 60*time.Second {
		t.Fatalf("download got %q in %v, attempts=%d, err=%v", got, time.Since(started), attempts.Load(), err)
	}
}

func TestEngineStallExhaustsAttemptsAndPreservesLiveAgent(t *testing.T) {
	engine, request, live, previous, commands := testEngine(t, false, false, time.Second)
	original := engine.config.ReleaseBase
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) != "404-probe-agent-linux-amd64" {
			http.Redirect(w, r, original+r.URL.Path, http.StatusTemporaryRedirect)
			return
		}
		attempts.Add(1)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	engine.config.ReleaseBase = server.URL
	engine.config.DownloadStallWindow = 80 * time.Millisecond
	engine.config.RetryBaseDelay = time.Millisecond
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	state := waitLocalStatus(t, engine, request, "failed")
	if state.FailureCode != "download_stalled" || !strings.Contains(state.FailureMessage, "stalled") || int(attempts.Load()) != engine.config.DownloadAttempts {
		t.Fatalf("state=%+v, attempts=%d", state, attempts.Load())
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != "old-agent" {
		t.Fatalf("live=%q, err=%v", got, err)
	}
	if _, err := os.Stat(previous); !errors.Is(err, os.ErrNotExist) || len(commands.values) != 0 {
		t.Fatalf("previous err=%v, service commands=%v", err, commands.values)
	}
}

func TestEngineDownloadDeadline(t *testing.T) {
	engine, request, _, _, _ := testEngine(t, false, false, time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	engine.config.ReleaseBase = server.URL
	engine.config.DownloadTimeout = 150 * time.Millisecond
	if _, err := engine.Start(request); err != nil {
		t.Fatal(err)
	}
	state := waitLocalStatus(t, engine, request, "failed")
	if state.FailureCode != "download_deadline_exceeded" {
		t.Fatalf("unexpected failure: %+v", state)
	}
}

func TestEngineDownloadTimeoutDefaults(t *testing.T) {
	engine, _, _, _, _ := testEngine(t, false, false, time.Second)
	if engine.config.DownloadTimeout != 30*time.Minute || engine.config.DownloadHeaderTimeout != 30*time.Second {
		t.Fatalf("unexpected total and header defaults: %v, %v", engine.config.DownloadTimeout, engine.config.DownloadHeaderTimeout)
	}
}

func TestDownloadDeadlineSharedAcrossFilesRetryAndFallback(t *testing.T) {
	var fallbackAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/download/v1.2.3/metadata", "/download/v1.2.3/SHA256SUMS":
			time.Sleep(50 * time.Millisecond)
			_, _ = fmt.Fprint(w, "small")
		case "/download/v1.2.3/asset":
			w.WriteHeader(http.StatusBadGateway)
		case "/api/tags/v1.2.3":
			_, _ = fmt.Fprintf(w, `{"tag_name":"v1.2.3","assets":[{"name":"asset","url":"http://%s/api/assets/42"}]}`, r.Host)
		case "/api/assets/42":
			fallbackAttempts.Add(1)
			select {
			case <-time.After(100 * time.Millisecond):
				w.WriteHeader(http.StatusBadGateway)
			case <-r.Context().Done():
			}
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	oldReleaseBase, oldAPIBase := officialReleaseBase, officialReleaseAPIBase
	officialReleaseBase, officialReleaseAPIBase = server.URL+"/download/", server.URL+"/api/"
	defer func() { officialReleaseBase, officialReleaseAPIBase = oldReleaseBase, oldAPIBase }()
	engine := &Engine{config: EngineConfig{
		HTTPClient: server.Client(), DownloadAttempts: 5, RetryBaseDelay: time.Millisecond,
		DownloadHeaderTimeout: time.Second, DownloadStallWindow: time.Second, DownloadMinBytesPerSecond: 1024,
	}}
	ctx, cancel := context.WithTimeoutCause(context.Background(), 270*time.Millisecond, errDownloadDeadline)
	defer cancel()
	base := officialReleaseBase + "v1.2.3/"
	for _, asset := range []string{"metadata", "SHA256SUMS"} {
		if _, err := engine.fetch(ctx, base+asset, 1024); err != nil {
			t.Fatal(err)
		}
	}
	_, err := engine.fetch(ctx, base+"asset", 1024)
	if !errors.Is(err, errDownloadDeadline) || classifyDownloadFailure(err) != "download_deadline_exceeded" || fallbackAttempts.Load() != 2 {
		t.Fatalf("err=%v, fallback attempts=%d", err, fallbackAttempts.Load())
	}
}

func TestDownloadHeaderTimeoutRetries(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		_, _ = fmt.Fprint(w, "retry succeeded")
	}))
	defer server.Close()
	engine := &Engine{config: EngineConfig{
		HTTPClient: server.Client(), DownloadAttempts: 2, RetryBaseDelay: time.Millisecond,
		DownloadHeaderTimeout: 80 * time.Millisecond, DownloadStallWindow: time.Second, DownloadMinBytesPerSecond: 1024,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := engine.fetch(ctx, server.URL, 1024)
	if err != nil || string(got) != "retry succeeded" || attempts.Load() != 2 {
		t.Fatalf("got=%q, attempts=%d, err=%v", got, attempts.Load(), err)
	}
}
