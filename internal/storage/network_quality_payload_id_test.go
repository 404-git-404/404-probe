package storage

import (
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// The stored payload of a user target carries no id: the row does. Every place that rebuilds a target from a
// payload has to restore it (R07 stage E: history read models in D1, then the backfill and TCP Mini).

// userTCPSample is a successful connect of the given duration.
func (k *v4Kit) userTCPSample(cfg protocol.QualityV4Config, seq int, at time.Time, ms float64) protocol.QualitySample {
	k.t.Helper()
	s := k.sample(cfg, "telecom", "ipv4", "tcp", seq, at, "success")
	d, err := protocol.DecodeQualityDetails(s.Details)
	if err != nil {
		k.t.Fatal(err)
	}
	d.ConnectMS = &ms
	s.LatencyMS = &ms
	s.DurationMS = ms + d.DNSMS + 1
	s.FinishedAt = s.StartedAt + int64(s.DurationMS) + 1
	s.Details = d.Encode()
	return s
}

func TestBackfillRebuildsUserTargetsWithTheirID(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	at := k.now.Add(time.Minute)
	if acks := k.upload(at.Add(time.Second), k.userTCPSample(cfg, 1, at, 20)); acks[0] != "committed" {
		t.Fatalf("upload: %v", acks)
	}
	id := k.targetID(cfg, "telecom", "ipv4", "tcp")
	count := func() int {
		card, err := k.s.QualityCardHistory(k.ctx, k.id, id, at.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return card.Current[0].Window.Count
	}
	if count() != 1 {
		t.Fatalf("before: %d samples", count())
	}
	// A database that still has history to aggregate gets a user-target sample: the backfill reads its payload.
	for round := 1; round <= 2; round++ {
		if _, err := k.s.db.Exec(`UPDATE quality_backfill SET complete=0,last_raw_id=0 WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		n, more, err := k.s.BackfillQualityBatch(k.ctx, at.Add(3*time.Minute))
		if err != nil || n != 1 || more {
			t.Fatalf("round %d: backfill of a user target: n=%d more=%v err=%v", round, n, more, err)
		}
		if got := count(); got != 1 {
			t.Fatalf("round %d: the backfill changed the aggregate (%d samples)", round, got)
		}
	}
	var series int
	if err := k.s.db.QueryRow(`SELECT COUNT(*) FROM quality_series WHERE agent_id=? AND target_id=?`, k.id, id).Scan(&series); err != nil || series != 1 {
		t.Fatalf("series for the target: %d %v", series, err)
	}
}
