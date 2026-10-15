package storage

import (
	"404-probe/internal/protocol"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func a1Capabilities() *protocol.QualityCapabilities {
	c := &protocol.QualityCapabilities{Version: 2, Semantics: protocol.QualityV2Semantics}
	for _, f := range []string{"ipv4", "ipv6"} {
		for _, p := range []string{"tcp", "icmp"} {
			c.Lanes = append(c.Lanes, protocol.QualityLaneCapability{Family: f, Protocol: p, Supported: true})
		}
	}
	return c
}
func a1Report(t *testing.T, s *Store, id string, seq uint64, now time.Time, caps *protocol.QualityCapabilities, os string) {
	t.Helper()
	r := validReport(id, 1, "quality_session", "boot", seq, 1, 1)
	r.NetworkQuality = true
	r.NetworkQualityV2 = caps
	r.OS = os
	if _, ok, _, err := s.ProcessReport(context.Background(), id, r, now); err != nil || !ok {
		t.Fatal(ok, err)
	}
}
func a1LaneConfig(rev string) protocol.QualityV2ConfigUpdate {
	u := protocol.QualityV2ConfigUpdate{ExpectedRevision: rev, Enabled: true, IPv6: true, RegionID: "region-a"}
	for _, s := range []string{"telecom", "unicom", "mobile"} {
		for _, f := range []string{"ipv4", "ipv6"} {
			for _, p := range []string{"tcp", "icmp"} {
				port := 0
				if p == "tcp" {
					port = 80
				}
				u.Lanes = append(u.Lanes, protocol.QualityLaneChoice{QualityChoice: protocol.QualityChoice{Slot: s, Source: "manual", Protocol: p, Host: "127.0.0.1", Port: port}, Family: f, Enabled: true})
			}
		}
	}
	return u
}
func a1Target(t *testing.T, c protocol.QualityConfig, slot, family, p string) protocol.QualityTarget {
	t.Helper()
	for _, x := range c.Targets {
		if x.Slot == slot && x.Family == family && x.Protocol == p {
			return x
		}
	}
	t.Fatal("missing lane", slot, family, p)
	return protocol.QualityTarget{}
}

func TestA1TwelveLanesIndependentRevisionLegacyFenceAndCAS(t *testing.T) {
	s, id, old, now := qualityFixture(t, ":memory:")
	ctx := context.Background()
	a1Report(t, s, id, 2, now, a1Capabilities(), "debian 13")
	u := a1LaneConfig(old.Revision)
	cfg, err := s.UpdateQualityConfigV2(ctx, id, u, qualityTestResolver, now)
	if err != nil || len(cfg.Targets) != 12 || cfg.State != "ready" {
		t.Fatal(cfg, err)
	}
	tcp := a1Target(t, cfg, "telecom", "ipv4", "tcp")
	icmp := a1Target(t, cfg, "telecom", "ipv4", "icmp")
	if tcp.ID == icmp.ID {
		t.Fatal("protocol identities collapsed")
	}
	legacy, err := s.GetQualitySessionConfig(ctx, id, 1, "quality_session")
	if err != nil || len(legacy.Targets) != 6 {
		t.Fatal(legacy, err)
	}
	for _, x := range legacy.Targets {
		if x.Protocol != "tcp" {
			t.Fatal("v2 lane leaked to v1")
		}
	}
	sample := qualitySample(icmp, cfg.Revision, 101, now, 3, "success")
	sample.Sent = 1
	sample.Received = 1
	ack, err := s.SaveQualityBatch(ctx, id, protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}, now)
	if err != nil || ack.Results[0][2] != "unauthorized" {
		t.Fatal("v1 accepted v2-only interval", ack, err)
	}
	batch := protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}
	ack, err = s.SaveQualityBatch(ctx, id, batch, now)
	if err != nil || ack.Results[0][2] != "committed" {
		t.Fatal(ack, err)
	}
	ack, err = s.SaveQualityBatch(ctx, id, batch, now)
	if err != nil || ack.Results[0][2] != "duplicate" {
		t.Fatal(ack, err)
	}
	u.ExpectedRevision = cfg.Revision
	u.Lanes[1].Host = "127.0.0.2"
	u.Lanes[1].Enabled = false
	changed, err := s.UpdateQualityConfigV2(ctx, id, u, qualityTestResolver, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	after := a1Target(t, changed, "telecom", "ipv4", "tcp")
	if after.ID != tcp.ID || after.SlotRevision != tcp.SlotRevision {
		t.Fatal("ICMP edit changed TCP identity/revision")
	}
	if x := a1Target(t, changed, "telecom", "ipv4", "icmp"); x.Status != "paused" || x.ID == icmp.ID {
		t.Fatal("independent pause/endpoint change", x)
	}
	// Omission pauses; history remains addressable under the original identity.
	u.ExpectedRevision = changed.Revision
	u.Lanes = u.Lanes[:1]
	paused, err := s.UpdateQualityConfigV2(ctx, id, u, qualityTestResolver, now.Add(2*time.Second))
	if err != nil || a1Target(t, paused, "mobile", "ipv6", "icmp").Status != "paused" {
		t.Fatal(paused, err)
	}
	history, err := s.QualityHistory(ctx, id, icmp.ID, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(history)
	if !strings.Contains(string(wire), icmp.ID) {
		t.Fatal("history target lost")
	}
	u.ExpectedRevision = paused.Revision
	u.Lanes = a1LaneConfig(paused.Revision).Lanes
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateQualityConfigV2(ctx, id, u, qualityTestResolver, now.Add(3*time.Second))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrQualityConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
}

func TestA1SessionCapabilityMixedRegionAlpineAndDetailsGates(t *testing.T) {
	s, id, cfg, now := qualityFixture(t, ":memory:")
	ctx := context.Background()
	if _, err := s.GetQualitySessionConfig(ctx, id, 1, "quality_session", 2); !errors.Is(err, ErrQualityUnsupported) {
		t.Fatal("v2 before negotiation", err)
	}
	a1Report(t, s, id, 2, now, a1Capabilities(), "debian 13")
	v2, err := s.GetQualitySessionConfig(ctx, id, 1, "quality_session", 2)
	if err != nil || v2.State != "region_selection_required" || v2.Enabled {
		t.Fatal("legacy manual not explicitly mapped", v2, err)
	}
	choices := []protocol.QualityChoice{}
	for i, slot := range []string{"telecom", "unicom", "mobile"} {
		p := "tcp"
		if i == 1 {
			p = "icmp"
		}
		choices = append(choices, protocol.QualityChoice{Slot: slot, Source: "catalog", Region: fmt.Sprintf("region-%d", i), Protocol: p})
	}
	cfg, err = s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: cfg.Revision, Enabled: true, IPv6: true, Choices: choices}, func(c protocol.QualityChoice, f string) (protocol.QualityTarget, error) {
		return protocol.QualityTarget{Host: "127.0.0.1", Port: 0, EndpointVersion: c.Region}, nil
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	v2, err = s.GetQualitySessionConfig(ctx, id, 1, "quality_session", 2)
	if err != nil || v2.Enabled || v2.State != "region_selection_required" {
		t.Fatal(v2, err)
	}
	legacy, err := s.GetQualityConfig(ctx, id)
	if err != nil || !legacy.Enabled || a1Target(t, legacy, "unicom", "ipv4", "icmp").Protocol != "icmp" {
		t.Fatal("legacy mixed/protocol facts lost", legacy, err)
	}
	a1Report(t, s, id, 3, now, nil, "debian 13")
	if _, err := s.GetQualitySessionConfig(ctx, id, 1, "quality_session", 2); !errors.Is(err, ErrQualityUnsupported) {
		t.Fatal("stale v2 retained after legacy report", err)
	}
	caps := a1Capabilities()
	caps.Lanes[1].Supported = false
	caps.Lanes[1].Reason = "unsupported"
	a1Report(t, s, id, 4, now, caps, "debian 13")
	u := a1LaneConfig(cfg.Revision)
	cfg, err = s.UpdateQualityConfigV2(ctx, id, u, qualityTestResolver, now)
	if err != nil {
		t.Fatal(err)
	}
	icmp := a1Target(t, cfg, "unicom", "ipv4", "icmp")
	if icmp.Status != "unavailable" {
		t.Fatal(icmp)
	}
	blocked := qualitySample(icmp, cfg.Revision, 1002, now, 2, "success")
	blocked.Sent = 1
	blocked.Received = 1
	blockedAck, err := s.SaveQualityBatch(ctx, id, protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{blocked}}, now)
	if err != nil || blockedAck.Results[0][2] != "unauthorized" {
		t.Fatal("unsupported runtime lane authorized", blockedAck, err)
	}
	tcp := a1Target(t, cfg, "telecom", "ipv4", "tcp")
	sample := qualitySample(tcp, cfg.Revision, 1001, now, 2, "success")
	sample.Details = json.RawMessage(`{"packets":3}`)
	ack, err := s.SaveQualityBatch(ctx, id, protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}, now)
	if err != nil || ack.Results[0][2] != "invalid" {
		t.Fatal("A2 details accepted", ack, err)
	}
	a1Report(t, s, id, 5, now, nil, "Alpine 3.22")
	for _, version := range []int{1, 2} {
		c, err := s.getQualityConfig(ctx, id, 0, "", version)
		if err != nil || c.Enabled || len(c.Targets) != 0 {
			t.Fatal("Alpine received work", c, err)
		}
	}
	sample.Details = nil
	_, err = s.SaveQualityBatch(ctx, id, protocol.QualityBatch{Version: 1, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}, now)
	if !errors.Is(err, ErrQualityUnsupported) {
		t.Fatal("Alpine authorization remained", err)
	}
	snapshot, err := s.GetAgentSnapshot(ctx, id, now, time.Minute)
	if err != nil || snapshot.State == nil || snapshot.State.Sequence != 5 {
		t.Fatal("base report broken", snapshot, err)
	}
}

func TestA1RetentionCompositeProtocolKey(t *testing.T) {
	s, id, old, now := qualityFixture(t, ":memory:")
	ctx := context.Background()
	a1Report(t, s, id, 2, now, a1Capabilities(), "debian 13")
	u := a1LaneConfig(old.Revision)
	cfg, err := s.UpdateQualityConfigV2(ctx, id, u, qualityTestResolver, now)
	if err != nil {
		t.Fatal(err)
	}
	tcp := a1Target(t, cfg, "telecom", "ipv4", "tcp")
	icmp := a1Target(t, cfg, "telecom", "ipv4", "icmp")
	if tcp.SlotRevision != icmp.SlotRevision {
		t.Fatal("fixture needs same revision across protocols")
	}
	u.ExpectedRevision = cfg.Revision
	u.Lanes[1].Enabled = false
	if _, err := s.UpdateQualityConfigV2(ctx, id, u, qualityTestResolver, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CleanupQualityBatch(ctx, now.Add(49*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var open, closed int
	if err := s.db.QueryRow(`SELECT count(*) FROM quality_intervals WHERE target_id=? AND revision=? AND end_at IS NULL`, tcp.ID, tcp.SlotRevision).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM quality_intervals WHERE target_id=? AND revision=?`, icmp.ID, icmp.SlotRevision).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if open != 1 || closed != 0 {
		t.Fatal("cleanup crossed protocol key", open, closed)
	}
	var bad int
	if err := s.db.QueryRow(`SELECT count(*) FROM quality_usage u WHERE intervals<>(SELECT count(*) FROM quality_intervals i WHERE i.agent_id=u.agent_id)`).Scan(&bad); err != nil || bad != 0 {
		t.Fatal("counter drift", bad, err)
	}
}

func TestA1Migration25MixedProtocolsPreservesRowsAndRollback(t *testing.T) {
	for _, fault := range []string{"", "protocol", "counter"} {
		t.Run(fault, func(t *testing.T) {
			s, id, cfg, now := qualityFixture(t, ":memory:")
			ctx := context.Background()
			cfg, err := s.UpdateQualityConfig(ctx, id, protocol.QualityConfigUpdate{ExpectedRevision: cfg.Revision, Enabled: true, IPv6: true, Choices: []protocol.QualityChoice{{Slot: "unicom", Source: "manual", Protocol: "icmp", Host: "127.0.0.1"}}}, qualityTestResolver, now.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			for i, target := range cfg.Targets {
				sample := qualitySample(target, cfg.Revision, int64(i+1), now, 2, "success")
				if target.Family == "ipv6" {
					sample.ResolvedIP = "::1"
				}
				if target.Protocol == "icmp" {
					sample.Sent = 1
					sample.Received = 1
				}
				if ack := qualitySave(t, s, id, now, sample); ack.Results[0][2] != "committed" {
					t.Fatal(ack)
				}
			}
			// Remove v2-only runtime rows to construct a legal schema25 fixture:
			// v1 retains historical intervals, but has one current protocol per slot.
			if _, err := s.db.Exec(`DELETE FROM quality_intervals WHERE v1_authorized=0`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM quality_slots WHERE protocol<>(SELECT json_extract(c.value,'$.protocol') FROM quality_config f,json_each(f.choices) c WHERE f.agent_id=quality_slots.agent_id AND json_extract(c.value,'$.slot')=quality_slots.slot)`); err != nil {
				t.Fatal(err)
			}
			dropQualityV2FixtureSchema(t, s.db)
			if fault == "protocol" {
				if _, err := s.db.Exec(`UPDATE quality_targets SET payload=json_remove(payload,'$.protocol') WHERE id=?`, qualityTarget(t, cfg, "unicom").ID); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "counter" {
				if _, err := s.db.Exec(`UPDATE quality_totals SET value=value+1 WHERE kind='intervals'`); err != nil {
					t.Fatal(err)
				}
			}
			objects, tables := renewalSchemaSnapshot(t, s)
			before := map[string]string{}
			columns := map[string][]string{}
			oldRows := map[string]string{}
			for _, table := range tables {
				before[table] = tableRenewalSnapshot(t, s, table)
				rows, err := s.db.Query(`SELECT * FROM ` + table)
				if err != nil {
					t.Fatal(err)
				}
				columns[table], err = rows.Columns()
				rows.Close()
				if err != nil {
					t.Fatal(err)
				}
				oldRows[table] = a1Rows(t, s, table, columns[table])
			}
			var rawRoot int
			if err := s.db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='quality_raw'`).Scan(&rawRoot); err != nil {
				t.Fatal(err)
			}
			err = s.migrate(ctx)
			if fault != "" {
				if err == nil {
					t.Fatal("migration fault ignored")
				}
				after, _ := renewalSchemaSnapshot(t, s)
				if !reflect.DeepEqual(after, objects) {
					t.Fatal("failed migration changed schema")
				}
				for _, table := range tables {
					if tableRenewalSnapshot(t, s, table) != before[table] {
						t.Fatal("failed migration changed rows", table)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"quality_targets", "quality_usage", "quality_totals", "quality_minute", "quality_gaps", "agent_state", "agent_sessions"} {
				queryTable := table
				if table == "quality_totals" {
					queryTable = "(SELECT * FROM quality_totals WHERE kind NOT IN ('receipts','facts','buckets','series','sessions'))"
				}
				if tableRenewalSnapshot(t, s, queryTable) != before[table] {
					t.Fatal("migration modified old rows", table)
				}
			}
			var root, raw, bad int
			if err := s.db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name='quality_raw'`).Scan(&root); err != nil || root != rawRoot {
				t.Fatal("raw table rebuilt", root, rawRoot, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM quality_raw WHERE details_json IS NULL AND length(content_hash)=32`).Scan(&raw); err != nil || raw != 6 {
				t.Fatal("raw loss", raw, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM quality_slots s JOIN quality_targets t ON s.target_id=t.id WHERE s.protocol<>json_extract(t.payload,'$.protocol')`).Scan(&bad); err != nil || bad != 0 {
				t.Fatal("protocol rewrite", bad, err)
			}
			// Existing v1 sample bytes hash identically after the nullable reservation.
			for _, table := range []string{"quality_raw", "quality_intervals", "quality_slots", "quality_config", "quality_capability"} {
				if a1Rows(t, s, table, columns[table]) != oldRows[table] {
					t.Fatal("legacy column bytes/IDs/hashes changed", table)
				}
			}
			for i, target := range cfg.Targets {
				sample := qualitySample(target, cfg.Revision, int64(i+1), now, 2, "success")
				if target.Family == "ipv6" {
					sample.ResolvedIP = "::1"
				}
				if target.Protocol == "icmp" {
					sample.Sent = 1
					sample.Received = 1
				}
				if ack := qualitySave(t, s, id, now, sample); ack.Results[0][2] != "duplicate" {
					t.Fatal("old canonical hash changed", ack)
				}
			}
			if _, err := s.db.Exec(`UPDATE quality_raw SET details_json=?`, `"`+strings.Repeat("x", 2048)+`"`); err == nil {
				t.Fatal("unbounded details accepted")
			}
			if _, err := s.db.Exec(`UPDATE quality_raw SET details_json=?`, `not json`); err == nil {
				t.Fatal("invalid details accepted")
			}
			var integrity string
			if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatal(integrity, err)
			}
			rows, err := s.db.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			if rows.Next() {
				t.Fatal("foreign key violation")
			}
			rows.Close()
		})
	}
}

func a1Rows(t *testing.T, s *Store, table string, columns []string) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT ` + strings.Join(columns, ",") + ` FROM ` + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	data := []string{}
	for rows.Next() {
		v := make([]any, len(columns))
		ptr := make([]any, len(v))
		for i := range v {
			ptr[i] = &v[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, string(b))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(data)
	return strings.Join(data, "\n")
}
