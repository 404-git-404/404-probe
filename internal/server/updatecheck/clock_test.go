package updatecheck

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"404-probe/internal/buildinfo"
)

func TestFutureCheckedAtTenYearsIsRechecked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-check.json")
	if err := save(path, State{Enabled: true, Latest: "v1.1.0", CheckedAt: time.Now().AddDate(10, 0, 0).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	c := New(path, buildinfo.Info{Version: "v1.0.9", Commit: "x"}, slog.New(slog.NewTextHandler(&logs, nil)))
	defer c.Close()
	if got := c.Snapshot(); got.CheckedAt != 0 || got.Latest != "v1.1.0" || !got.Newer {
		t.Fatal(got)
	}
	if strings.Count(logs.String(), "future checked_at") != 1 {
		t.Fatal(logs.String())
	}
	if delay := c.initialDelay(); delay < time.Minute || delay > 10*time.Minute {
		t.Fatal(delay)
	}
	// A normalized cache can be saved (e.g. by toggling) and loaded again.
	if _, err := c.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if s, err := load(path); err != nil || s.CheckedAt != 0 || s.Latest != "v1.1.0" {
		t.Fatal(s, err)
	}
	t.Log("ten-year future time normalized to 0; last validated release retained; warning emitted once")
}

func TestCheckedAtClockRollbackAndTwelveHourBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		offset   time.Duration
		requests int32
	}{{"rollback_two_days", 48 * time.Hour, 1}, {"just_before_twelve_hours", -interval + time.Millisecond, 0}, {"exactly_twelve_hours", -interval, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			c, count := testChecker(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"v1.1.0"}`) })
			now := time.Now().Truncate(time.Millisecond)
			c.now = func() time.Time { return now }
			c.delay = func() time.Duration { return time.Minute }
			c.state.CheckedAt = now.Add(tc.offset).UnixMilli()
			c.state.Latest = "v1.0.10"
			if delay := c.initialDelay(); delay < 0 || delay > interval {
				t.Fatal(delay)
			}
			if tc.offset > 0 && c.initialDelay() != interval {
				t.Fatal(c.initialDelay())
			}
			if err := c.check(t.Context()); err != nil {
				t.Fatal(err)
			}
			if count.Load() != tc.requests {
				t.Fatal(count.Load())
			}
			t.Logf("offset=%s request_count=%d; delay capped at 12h", tc.offset, count.Load())
		})
	}
}
