package storage

import (
	"context"
	"strings"
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

// A transient high connect time is kept even when the rest of its minute is low (the 30 s cadence of the Agent:
// a slow sample, then a quick one 30 s later). The open minute is rebuilt from the raw samples of the target.
func TestTCPMiniKeepsATransientHighOfAUserTarget(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	minute := k.now.Truncate(time.Minute).Add(time.Minute)
	if _, err := k.s.db.Exec(`INSERT OR REPLACE INTO tcp_mini_state(agent_id,evidence_start,scanned_at,initialized) VALUES(?,?,?,1)`, k.id, minute.Add(-48*time.Hour).UnixMilli(), minute.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// 2100 ms taken at +20 s, received early (accepted future skew); at +25 s it has matured.
	if acks := k.upload(minute.Add(5*time.Second), k.userTCPSample(cfg, 1, minute.Add(20*time.Second), 2100)); acks[0] != "committed" {
		t.Fatalf("high sample: %v", acks)
	}
	if err := k.s.MaintainTCPMini(context.Background(), minute.Add(25*time.Second)); err != nil {
		t.Fatal(err)
	}
	// The next sample, 30 s later, is quick; once the minute is complete its P95 is below the threshold.
	if acks := k.upload(minute.Add(50*time.Second), k.userTCPSample(cfg, 2, minute.Add(50*time.Second), 1)); acks[0] != "committed" {
		t.Fatalf("low sample: %v", acks)
	}
	if err := k.s.MaintainTCPMini(context.Background(), minute.Add(65*time.Second)); err != nil {
		t.Fatal(err)
	}
	status, err := k.s.TCPMini(context.Background(), k.id, "UTC", minute.Add(70*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if status.Peak != 2100 || status.LastHigh == 0 {
		t.Fatalf("the transient high of a user target was lost: %+v", status)
	}
	var source string
	if err := k.s.db.QueryRow(`SELECT source FROM tcp_mini_high WHERE agent_id=?`, k.id).Scan(&source); err != nil || !strings.Contains(source, `"id":"`+k.targetID(cfg, "telecom", "ipv4", "tcp")+`"`) {
		t.Fatalf("the evidence names its target: %v %s", err, source)
	}
}
