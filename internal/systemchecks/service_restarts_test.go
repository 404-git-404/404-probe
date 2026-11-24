package systemchecks

import (
	"testing"
	"time"
)

func TestServiceRestartWindow(t *testing.T) {
	start := time.Now()
	var tracker serviceRestartTracker
	for _, tc := range []struct {
		minute        int
		counter, want uint64
		absent        bool
	}{
		{0, 20, 0, false}, {1, 21, 1, false}, {2, 22, 2, false}, {3, 23, 3, false}, {4, 0, 3, false},
		{11, 0, 2, false}, {12, 0, 1, true}, {13, 26, 0, false}, {14, 26, 0, false},
	} {
		counters := map[string]uint64{}
		if !tc.absent {
			counters["unwatched.service"] = tc.counter
		}
		results := tracker.observe(start.Add(time.Duration(tc.minute)*time.Minute), counters)
		if tc.want == 0 && len(results) != 0 || tc.want != 0 && (len(results) != 1 || results[0].Unit != "unwatched.service" || results[0].Count != tc.want) {
			t.Fatalf("minute %d: got %+v, want %d", tc.minute, results, tc.want)
		}
	}
}
