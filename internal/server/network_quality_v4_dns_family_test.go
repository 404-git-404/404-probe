package server

import (
	"context"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// One family timing out must not hide a private answer of the other (design 5.2: either side).
func TestR07V4NameCheckFamiliesAreIndependent(t *testing.T) {
	for _, slowNetwork := range []string{"ip4", "ip6"} {
		t.Run("slow "+slowNetwork, func(t *testing.T) {
			a, _, _, done := v4App(t)
			defer done()
			private := map[string]netip.Addr{"ip4": netip.MustParseAddr("10.1.2.3"), "ip6": netip.MustParseAddr("fd00::1")}
			a.qualityLookup = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
				if network == slowNetwork {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				return []netip.Addr{private[network]}, nil
			}
			started := time.Now()
			w := qualityWebRequest(t, a, http.MethodPost, networkTargetsPath, webTargetBody("private.example.com"))
			if w.Code != http.StatusBadRequest || v4Decode(t, w).Error.Details[0].Code != "unsafe_address" {
				t.Fatalf("a private %s answer escaped because the other family stalled: HTTP %d", map[string]string{"ip4": "AAAA", "ip6": "A"}[slowNetwork], w.Code)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("the shared budget was exceeded")
			}
		})
	}
	// Both fail: allowed. Mixed answers in either order: refused.
	a, _, resolver, done := v4App(t)
	defer done()
	if w := qualityWebRequest(t, a, http.MethodPost, networkTargetsPath, webTargetBody("gone.example.com")); w.Code != 201 {
		t.Fatal("both sides failing must be allowed", w.Code)
	}
	resolver.answers["ip4/mix.example.com"] = []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("93.184.216.34")}
	resolver.answers["ip6/mix2.example.com"] = []netip.Addr{netip.MustParseAddr("2606:2800:220:1::1"), netip.MustParseAddr("fd00::2")}
	for _, name := range []string{"mix.example.com", "mix2.example.com"} {
		if w := qualityWebRequest(t, a, http.MethodPost, networkTargetsPath, webTargetBody(name)); w.Code != 400 {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
}

// The ticket counts saves, not lookups: four saves at a time, each with its own budget.
func TestR07V4NameCheckPeakConcurrentSaves(t *testing.T) {
	a, _, resolver, done := v4App(t)
	defer done()
	resolver.block = make(chan struct{})
	var inFlight, peak atomic.Int32
	var mu sync.Mutex
	inner := resolver.lookup
	a.qualityLookup = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		n := inFlight.Add(1)
		mu.Lock()
		if n > peak.Load() {
			peak.Store(n)
		}
		mu.Unlock()
		defer inFlight.Add(-1)
		return inner(ctx, network, host)
	}
	codes := make(chan int, 6)
	for i := 0; i < 6; i++ {
		go func() {
			w := qualityWebRequest(t, a, http.MethodPost, networkTargetsPath, webTargetBody("n"+string(rune('a'+i))+".example.com"))
			codes <- w.Code
		}()
	}
	busy := 0
	for i := 0; i < 6; i++ {
		select {
		case code := <-codes:
			if code == 503 {
				busy++
			}
		case <-time.After(6 * time.Second):
			t.Fatal("request did not finish")
		}
	}
	if busy < 2 {
		t.Fatalf("saves over the limit were not refused: %d busy", busy)
	}
	// At most four saves ask at a time, with two address families each.
	if peak.Load() > 8 {
		t.Fatalf("peak concurrent lookups %d", peak.Load())
	}
	close(resolver.block)
}
