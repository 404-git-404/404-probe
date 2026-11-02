package updatecheck

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
)

func testChecker(t *testing.T, handler http.HandlerFunc) (*Checker, *atomic.Int32) {
	t.Helper()
	count := new(atomic.Int32)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1); handler(w, r) }))
	t.Cleanup(s.Close)
	c := newChecker(State{Enabled: true}, buildinfo.Info{Version: "v1.0.9", Commit: "local"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.fetchURL = s.URL
	transport := s.Client().Transport
	c.client.Transport = transport
	t.Cleanup(c.Close)
	return c, count
}

func TestFetchSafety(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"stable", `{"tag_name":"v1.0.10","html_url":"https://evil.invalid"}`, 200, true},
		{"limit", `{"tag_name":"v1.1.0"}` + strings.Repeat(" ", maxBody-len(`{"tag_name":"v1.1.0"}`)), 200, true},
		{"oversize", strings.Repeat(" ", maxBody+1), 200, false},
		{"non200", `{"tag_name":"v1.1.0"}`, 503, false},
		{"bad", "{}", 200, false}, {"rc", `{"tag_name":"v1.1.0-rc.1"}`, 200, false},
		{"leading_zero", `{"tag_name":"v1.01.0"}`, 200, false}, {"overflow", `{"tag_name":"v18446744073709551616.0.0"}`, 200, false},
		{"malformed", "[", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testChecker(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("User-Agent") != userAgent || r.URL.String() != "/" {
					t.Errorf("request %s %s %v", r.Method, r.URL, r.Header)
				}
				for key := range r.Header {
					if key != "User-Agent" && key != "Accept" && key != "Accept-Encoding" {
						t.Errorf("unexpected header %s", key)
					}
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := c.fetch(t.Context())
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	t.Run("https_only", func(t *testing.T) {
		c, count := testChecker(t, func(http.ResponseWriter, *http.Request) {})
		c.fetchURL = strings.Replace(c.fetchURL, "https:", "http:", 1)
		if _, err := c.fetch(t.Context()); err == nil || count.Load() != 0 {
			t.Fatal(err, count.Load())
		}
	})
	t.Run("redirect", func(t *testing.T) {
		c, count := testChecker(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/next", 302) })
		if _, err := c.fetch(t.Context()); err == nil || count.Load() != 1 {
			t.Fatal(err, count.Load())
		}
	})
	t.Run("timeout", func(t *testing.T) {
		c, _ := testChecker(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		c.client.Timeout = 25 * time.Millisecond
		if _, err := c.fetch(t.Context()); err == nil {
			t.Fatal("no timeout")
		}
	})
}

func TestCacheEligibilityAndFailures(t *testing.T) {
	fail := false
	c, count := testChecker(t, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"tag_name":"v1.0.10"}`)
	})
	if c.Snapshot().Newer {
		t.Fatal("no success yet")
	}
	if err := c.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	good := c.Snapshot()
	if !good.Newer || good.Latest != "v1.0.10" {
		t.Fatal(good)
	}
	for i := 0; i < 20; i++ {
		c.Snapshot()
	}
	if count.Load() != 1 {
		t.Fatal(count.Load())
	}
	for _, info := range []buildinfo.Info{{Version: "dev"}, {Version: "v1.0.9", Commit: "x", Dirty: true}, {Version: "v1.1.0-rc.1", Commit: "x"}, {Version: "v1.1.0", Commit: "x"}, {Version: "v1.0.10", Commit: "x"}} {
		c.info = info
		if c.Snapshot().Newer {
			t.Fatal(info)
		}
	}
	c.info = buildinfo.Info{Version: "v1.0.9", Commit: "x"}
	if err := c.check(t.Context()); err != nil || count.Load() != 1 {
		t.Fatal("12 hour gate", err, count.Load())
	}
	c.now = func() time.Time { return time.UnixMilli(good.CheckedAt).Add(interval) }
	fail = true
	if err := c.check(t.Context()); err == nil {
		t.Fatal("missing failure")
	}
	if c.Snapshot() != good {
		t.Fatal(c.Snapshot(), good)
	}
	c.state = State{Enabled: true}
	if err := c.check(t.Context()); err == nil || c.Snapshot().Newer {
		t.Fatal("first failure", err)
	}
}

func TestDisableAndShutdownCancel(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			c, count := testChecker(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(canceled) })
			c.delay = func() time.Duration { return time.Millisecond }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c.Start(ctx)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("not started")
			}
			if shutdown {
				cancel()
				c.Close()
			} else {
				if _, err := c.SetEnabled(false); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("not canceled")
			}
			if !shutdown {
				for i := 0; i < 10; i++ {
					if err := c.check(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				if c.Snapshot().Newer {
					t.Fatal("disabled")
				}
			}
			if count.Load() != 1 {
				t.Fatal(count.Load())
			}
		})
	}
}

func TestReenableDelayAndRandomRange(t *testing.T) {
	c, count := testChecker(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"v1.1.0"}`) })
	if _, err := c.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := c.check(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 0 {
		t.Fatal(count.Load())
	}
	if _, err := c.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	c.delay = func() time.Duration { return time.Millisecond }
	c.Start(t.Context())
	deadline := time.Now().Add(time.Second)
	for count.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if count.Load() != 1 {
		t.Fatal(count.Load())
	}
	for i := 0; i < 100; i++ {
		d := randomDelay()
		if d < time.Minute || d > 10*time.Minute {
			t.Fatal(d)
		}
	}
}
