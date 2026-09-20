package agent

import (
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
			if got := capOutboundDelayAtHeartbeat(test.now, published, test.delay); got != test.want {
				t.Fatalf("delay=%s want=%s", got, test.want)
			}
		})
	}
}

func TestOutboundDelayWithoutPublishedSnapshotUsesBackoff(t *testing.T) {
	if got := capOutboundDelayAtHeartbeat(time.Unix(1000, 0), time.Time{}, 5*time.Minute); got != 5*time.Minute {
		t.Fatalf("delay=%s", got)
	}
}
