package storage

import (
	"context"
	"database/sql"

	"404-probe/internal/protocol"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDashboardNetworkSlotsStatesAndSamples(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	read := func(at time.Time) [3]DashboardNetworkSlot {
		t.Helper()
		data, err := k.s.ReadDashboardData(k.ctx, at, 30*time.Second, false)
		if err != nil || len(data.Records) != 1 {
			t.Fatal(err)
		}
		return data.Records[0].NetworkSlots
	}
	if got := read(k.now); got[0].State != "no_data" || got[1].State != "no_target" || got[2].State != "no_target" {
		t.Fatal(got)
	}
	at := k.now.Add(time.Minute)
	upload := func(seq int, family, proto, kind string, stamp time.Time) {
		t.Helper()
		sample := k.sample(cfg, "telecom", family, proto, seq, stamp, kind)
		if family == "ipv6" {
			sample.ResolvedIP = "2606:4700:4700::1111"
		}
		if proto == "icmp" {
			ms := 3.0
			sample.Outcome = "success"
			sample.LatencyMS = &ms
			sample.ResolvedIP = "93.184.216.34"
			sample.Sent = 3
			sample.Received = 3
			sample.DurationMS = 2005
			sample.FinishedAt = sample.StartedAt + 2005
			d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, IntervalMS: cfg.EffectiveIntervalMS, DNSMS: 5, DNSOutcome: "success", ProbeComplete: true, WindowComplete: true, Requested: 3, WindowMS: 2000}
			for i := 1; i <= 3; i++ {
				sent := float64(5 + (i-1)*200)
				d.Echoes = append(d.Echoes, protocol.QualityEcho{Seq: i, Status: "reply", RTTMS: &ms, SentMS: &sent})
			}
			sample.Details = d.Encode()
		}
		if acks := k.upload(stamp.Add(time.Second), sample); acks[0] != "committed" {
			t.Fatal(acks)
		}
	}
	upload(1, "ipv4", "tcp", "success", at)
	upload(2, "ipv6", "tcp", "success", at.Add(30*time.Second))
	upload(3, "ipv4", "icmp", "not_found", at.Add(60*time.Second))
	upload(4, "ipv4", "tcp", "dns_timeout", at.Add(90*time.Second))
	for _, tc := range []struct {
		age   time.Duration
		state string
	}{{time.Minute, "ok"}, {5 * time.Minute, "ok"}, {5*time.Minute + time.Millisecond, "stale"}} {
		got := read(at.Add(tc.age))[0]
		if got.State != tc.state || got.SampledAt != at.UnixMilli() || got.LatestMS == nil {
			t.Fatal(got)
		}
	}
	// A warm value also crosses the inclusive one-hour boundary immediately.
	if got := read(at.Add(time.Hour))[0]; got.State != "stale" || got.LatestMS == nil {
		t.Fatal(got)
	}
	if got := read(at.Add(time.Hour + time.Millisecond))[0]; got.State != "no_data" || got.LatestMS != nil || got.SampledAt != 0 {
		t.Fatal(got)
	}
	k.s.dashboardSlots.entries = nil
	if got := read(at.Add(time.Hour + time.Millisecond))[0]; got.State != "no_data" || got.LatestMS != nil {
		t.Fatal(got)
	}
	// Labels come from current settings; the previously successful sample stays.
	k.now = at.Add(2 * time.Minute)
	renamed := v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp")
	renamed.Region = "New region"
	renamed.ISP = "New ISP"
	k.save(30000, renamed)
	if got := read(k.now)[0]; got.TargetName != "New region New ISP" || got.SampledAt != at.UnixMilli() {
		t.Fatal(got)
	}
	k.enable(false)
	if got := read(k.now)[0]; got.State != "no_target" || got.LatestMS != nil || got.SampledAt != 0 {
		t.Fatal(got)
	}
	if _, err := k.s.db.Exec(`UPDATE quality_capability SET excluded_reason='container' WHERE agent_id=?`, k.id); err != nil {
		t.Fatal(err)
	}
	if got := read(k.now); got[0].State != "unsupported" || got[1].State != "unsupported" {
		t.Fatal(got)
	}
}

type countingSlotReader struct {
	db    *sql.DB
	calls int
}

func (q *countingSlotReader) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.calls++
	return q.db.QueryContext(ctx, query, args...)
}

func TestDashboardNetworkSlotsBatchQueryCount(t *testing.T) {

	k := v4Base(t, true)
	s := k.s
	for _, n := range []int{1, 200} {
		for i := 1; i < n; i++ {
			id := fmt.Sprintf("%032x", i)
			if err := s.AddAgent(t.Context(), id, "synthetic", []byte(fmt.Sprintf("hash-%d", i)), k.now); err != nil {
				t.Fatal(err)
			}
			device := &v4Kit{t: t, s: s, id: id, now: k.now, epoch: 1, session: "batch", ctx: k.ctx}
			device.negotiate(v4Caps(protocol.QualityIPv6Present))
			device.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp"))
			device.enable(true)
		}
		q := &countingSlotReader{db: s.db}
		cache := &dashboardSlotCache{}
		slots, err := readDashboardNetworkSlots(t.Context(), q, k.now, cache)
		if err != nil || len(slots) != n || q.calls != 2 {
			t.Fatal(len(slots), q.calls, err)
		}
		q.calls = 0
		if _, err := readDashboardNetworkSlots(t.Context(), q, k.now.Add(time.Second), cache); err != nil || q.calls != 1 {
			t.Fatal("cache hit", err, q.calls)
		}
		q.calls = 0
		if _, err := readDashboardNetworkSlots(t.Context(), q, k.now.Add(5*time.Second), cache); err != nil || q.calls != 2 {
			t.Fatal("cache expiry", err, q.calls)
		}
		for _, v := range slots {
			for i, slot := range v {
				if slot.Slot != i+1 {
					t.Fatal(v)
				}
			}
		}
	}
	// Query plan must seek the existing per-target/time index, never sort or scan
	// all raw history separately for each device.
	rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+fmt.Sprintf(dashboardSlotSampleSQL, "(?)"), "target", time.Now().Add(-time.Hour).UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "quality_fact_neighbor") {
			found = true
		}
		if strings.Contains(detail, "SCAN fact") {
			t.Fatal(detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("missing indexed sample lookup")
	}
}
