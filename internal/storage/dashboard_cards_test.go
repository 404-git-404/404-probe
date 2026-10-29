package storage

import (
	"404-probe/internal/protocol"
	"context"
	"reflect"
	"testing"
	"time"
)

func TestDashboardCardBatchesMatchExistingReaders(t *testing.T) {
	s, id, _ := testStore(t, ":memory:")
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(10000, 0)
	negotiateGoogleStatus(t, s, id, now)
	if err := s.SaveOutboundSnapshot(ctx, id, protocol.OutboundSnapshot{Available: true, OrderSource: "config", Selectors: []protocol.OutboundSelector{{Name: "YouTube", Current: "a", Choices: []string{"a", "b"}}}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureGoogleStatusJob(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob(ctx, id, claimRequest(1, "session-1", protocol.ProbeTypeGoogleStatus), now, time.Minute)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitJobResult(ctx, id, job.JobID, resultFor(job, true), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(2 * time.Second), now.Add(25 * time.Hour)} {
		data, err := s.ReadDashboardData(ctx, at, 30*time.Second, false)
		if err != nil || len(data.Records) != 1 {
			t.Fatal(data, err)
		}
		r := data.Records[0]
		g, _, err := s.GetGoogleStatusAt(ctx, id, at)
		if err != nil || !reflect.DeepEqual(r.Google, &g) {
			t.Fatal("google batch drift", r.Google, g, err)
		}
		o, _, err := s.GetOutboundSnapshot(ctx, id)
		if err != nil || !reflect.DeepEqual(r.Outbound, &o) {
			t.Fatal("outbound batch drift", err)
		}
		totals, err := s.TrafficTotals(ctx, id)
		if err != nil || !reflect.DeepEqual(r.TrafficTotals, &totals) {
			t.Fatal("traffic batch drift", err)
		}
		pending, err := s.GoogleStatusPending(ctx, id, at)
		if err != nil || r.GooglePending != pending || !r.GoogleSupported {
			t.Fatal("capability/pending drift", err)
		}
	}
}
