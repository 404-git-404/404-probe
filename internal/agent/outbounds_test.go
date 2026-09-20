package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestOutboundBackoffCannotCrossHeartbeatDeadline(t *testing.T) {
	published := time.Unix(1000, 0)
	tests := []struct {
		name  string
		now   time.Time
		delay time.Duration
		want  time.Duration
	}{
		{name: "negative jitter wake", now: published.Add(9 * time.Minute / 2), delay: 9 * time.Minute / 2, want: 30 * time.Second},
		{name: "at deadline", now: published.Add(outboundHeartbeatInterval), delay: 5 * time.Minute, want: 0},
		{name: "ordinary interval", now: published.Add(time.Minute), delay: 30 * time.Second, want: 30 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := capOutboundDelayAtHeartbeat(test.now, published, test.delay, outboundHeartbeatInterval); got != test.want {
				t.Fatalf("delay=%s want=%s", got, test.want)
			}
		})
	}
}

func TestOutboundPublishFailureAfterHeartbeatKeepsBoundedBackoffAndRecovers(t *testing.T) {
	var timesMu sync.Mutex
	postTimes := make([]time.Time, 0, 5)
	recovered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxies":
			_, _ = io.WriteString(w, `{"proxies":{"proxy":{"type":"Selector","name":"proxy","now":"hk","all":["hk","jp"]}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/outbounds":
			timesMu.Lock()
			postTimes = append(postTimes, time.Now())
			attempt := len(postTimes)
			timesMu.Unlock()
			if attempt == 2 || attempt == 3 {
				http.Error(w, "temporary", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
			if attempt >= 4 {
				select {
				case recovered <- struct{}{}:
				default:
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := newJobTestRunner(t, server.URL, time.Second, time.Hour, UnsupportedExecutor{})
	runner.config.ClashAPIURL = server.URL
	runner.config.OutboundInterval = 100 * time.Millisecond
	runner.client = server.Client()
	runner.outboundHeartbeatInterval = 20 * time.Millisecond
	runner.outboundRetrySteps = []time.Duration{8 * time.Millisecond, 12 * time.Millisecond, 20 * time.Millisecond}
	runner.outboundJitter = func(delay time.Duration) time.Duration { return delay }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.runOutboundDiscovery(ctx)
		close(done)
	}()
	select {
	case <-recovered:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("outbound publisher did not recover")
	}
	cancel()
	<-done
	timesMu.Lock()
	defer timesMu.Unlock()
	if len(postTimes) < 4 || len(postTimes) > 5 {
		t.Fatalf("publish attempts=%d, want 4 or 5", len(postTimes))
	}
	if delay := postTimes[1].Sub(postTimes[0]); delay < 15*time.Millisecond {
		t.Fatalf("heartbeat retry started too early after %s", delay)
	}
	if delay := postTimes[2].Sub(postTimes[1]); delay < 7*time.Millisecond {
		t.Fatalf("first failure had no bounded backoff: %s", delay)
	}
	if delay := postTimes[3].Sub(postTimes[2]); delay < 10*time.Millisecond {
		t.Fatalf("second failure had no bounded backoff: %s", delay)
	}
}

func TestOutboundDelayWithoutPublishedSnapshotUsesBackoff(t *testing.T) {
	if got := capOutboundDelayAtHeartbeat(time.Unix(1000, 0), time.Time{}, 5*time.Minute, outboundHeartbeatInterval); got != 5*time.Minute {
		t.Fatalf("delay=%s", got)
	}
}
