package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestUpdaterHealthCacheCoalescesAndExpires(t *testing.T) {
	calls := 0
	r := &Runner{config: Config{UpdaterSocket: "test"}, updaterHealthProbe: func(context.Context) protocol.UpdaterHealth {
		calls++
		return protocol.UpdaterHealth{Status: "unreachable"}
	}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h := r.updaterHealth(context.Background()); h.Status != "unreachable" {
				t.Error(h)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatal("concurrent reports repeated timeout", calls)
	}
	r.updaterHealthExpires = time.Now().Add(-time.Second)
	r.updaterHealth(context.Background())
	if calls != 2 {
		t.Fatal("expired cache was not refreshed", calls)
	}
}

func TestUpdaterHealthCanceledConnectFailureNotCached(t *testing.T) {
	calls := 0
	r := &Runner{config: Config{UpdaterSocket: "test"}, updaterHealthProbe: func(ctx context.Context) protocol.UpdaterHealth {
		calls++
		return protocol.UpdaterHealth{Status: "unreachable", Reason: "connect_failed"}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.updaterHealth(ctx)
	if !r.updaterHealthExpires.IsZero() {
		t.Fatal("canceled failure cached")
	}
	r.updaterHealth(context.Background())
	if calls != 2 || r.updaterHealthExpires.IsZero() {
		t.Fatal(calls)
	}
}
