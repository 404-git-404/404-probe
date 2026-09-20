package agent

import (
	"testing"
	"time"
)

func TestIdlePollDelayIsBoundedAndResetsExternally(t *testing.T) {
	interval := 10 * time.Second
	wants := []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	for emptyCycles, want := range wants {
		if got := idlePollDelay(interval, emptyCycles); got != want {
			t.Fatalf("empty cycles=%d delay=%s want=%s", emptyCycles, got, want)
		}
	}
	if got := idlePollDelay(interval, 0); got != interval {
		t.Fatalf("reset delay=%s", got)
	}
}
