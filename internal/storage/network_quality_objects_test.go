package storage

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func TestQualityObjectsRetainHistoryAndCardOrder(t *testing.T) {
	k := v4Base(t, true)
	legacy := k.get()
	for i, slot := range legacy.Slots {
		if slot.Slot != protocol.QualitySlotOrder()[i] || !slot.ShowOnCard {
			t.Fatalf("legacy object %d: %+v", i, slot)
		}
	}
	legacyTarget := *k.lane(legacy, "telecom", "ipv4", "tcp").TargetID
	rows := slices.Clone(legacy.Slots)
	for i := 1; i < 3; i++ {
		rows[i] = v4Slot(rows[i].Slot, "example.org", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp")
		rows[i].ShowOnCard = true
	}
	for i := 0; i < 5; i++ {
		rows = append(rows, v4Slot(fmt.Sprintf("new-%d", i), "example.org", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
	}
	save := func(slots []protocol.QualityV4Slot) (protocol.QualityV4Config, error) {
		return k.s.SaveQualityConfigV4(k.ctx, k.id, protocol.QualityV4SaveRequest{
			ExpectedRevision: k.get().SettingsRevision, IntervalMS: 30000, Slots: slots, Objects: true,
		}, k.tick())
	}
	rows[3].ShowOnCard = true
	if _, err := save(rows); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("four card objects: %v", err)
	}
	rows[3].ShowOnCard = false
	if _, err := save(append(slices.Clone(rows), v4Slot("new-5", "example.net", 443))); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("nine objects: %v", err)
	}
	cfg, err := save(rows)
	if err != nil || len(cfg.Slots) != 8 || len(cfg.Lanes) != 32 {
		t.Fatalf("eight objects: slots=%d lanes=%d error=%v", len(cfg.Slots), len(cfg.Lanes), err)
	}
	oldAgent, err := k.s.GetQualitySessionConfig(k.ctx, k.id, k.epoch, k.session, 2, true)
	if err != nil || len(oldAgent.Targets) != 12 {
		t.Fatalf("old Agent: targets=%d error=%v", len(oldAgent.Targets), err)
	}
	caps := v4Caps(protocol.QualityIPv6Present)
	caps.Objects = true
	k.reconcile(k.report(true, caps))
	cfg = k.get()
	newAgent, err := k.s.GetQualitySessionConfig(k.ctx, k.id, k.epoch, k.session, 2, true)
	if err != nil || len(newAgent.Targets) != 32 || newAgent.IntervalMS != 60000 || cfg.EffectiveIntervalMS != newAgent.IntervalMS {
		t.Fatalf("new Agent: targets=%d agent interval=%d web interval=%d error=%v", len(newAgent.Targets), newAgent.IntervalMS, cfg.EffectiveIntervalMS, err)
	}
	object := cfg.Slots[3].Slot
	if !protocol.QualityObjectID(object) || protocol.QualitySlot(object) {
		t.Fatalf("new object identity: %q", object)
	}
	measured := k.tick()
	if receipt := k.upload(measured.Add(time.Second), k.sample(cfg, object, "ipv4", "tcp", 1, measured, "success")); receipt[0] != "committed" {
		t.Fatalf("upload: %v", receipt)
	}
	rows = slices.Clone(cfg.Slots)
	rows[2].ShowOnCard, rows[3].ShowOnCard = false, true
	rows[0], rows[3] = rows[3], rows[0]
	ordered, err := save(rows)
	if err != nil || ordered.ConfigRevision != cfg.ConfigRevision || ordered.SettingsRevision == cfg.SettingsRevision || ordered.Slots[0].Slot != object || *k.lane(ordered, "telecom", "ipv4", "tcp").TargetID != legacyTarget {
		t.Fatalf("reorder changed measurement: %s -> %s, %v", cfg.ConfigRevision, ordered.ConfigRevision, err)
	}
	card, err := k.s.QualityCardSummary(k.ctx, k.id, "ipv4", k.now)
	if err != nil || len(card.Lanes) != 3 || card.Lanes[0].Slot != object || card.Lanes[1].Slot != "unicom" || card.Lanes[2].Slot != "telecom" {
		t.Fatalf("card order: %+v, %v", card.Lanes, err)
	}
	rows[0].Host = "example.net"
	changed, err := save(rows)
	if err != nil || changed.Slots[0].Slot != object {
		t.Fatalf("change target: %v", err)
	}
	measured = k.tick()
	if receipt := k.upload(measured.Add(time.Second), k.sample(changed, object, "ipv4", "tcp", 2, measured, "success")); receipt[0] != "committed" {
		t.Fatalf("second upload: %v", receipt)
	}
	if removed, err := save(rows[1:]); err != nil || len(removed.Slots) != 7 || len(k.open()) != 28 {
		t.Fatalf("delete object: slots=%d open=%d error=%v", len(removed.Slots), len(k.open()), err)
	}
	if n := k.count(`SELECT COUNT(*) FROM quality_slots WHERE agent_id=? AND slot=?`, k.id, object); n != 0 {
		t.Fatalf("deleted object left %d lane pointers", n)
	}
	now := k.now.Add(time.Minute)
	history, err := k.s.NetworkHistory(k.ctx, k.id, NetworkLine{object, "ipv4", "tcp"}, now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now)
	if err != nil || history.Stats.Samples != 2 || len(history.Changes) != 1 || history.LastChangeAt == nil {
		t.Fatalf("retained history: samples=%d changes=%v error=%v", history.Stats.Samples, history.Changes, err)
	}
	fleet, err := k.s.NetworkFleet(k.ctx, k.id, "ipv4", "tcp", now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now, time.Minute, true)
	if err != nil || len(fleet.Machines) != 1 || len(fleet.Machines[0].Lines) != 2 || fleet.Machines[0].Lines[0].Slot != "unicom" {
		t.Fatalf("card fleet selection: %+v, %v", fleet.Machines, err)
	}
}

func TestMigration44PreservesLegacyQualityHistory(t *testing.T) {
	k := v4Base(t, true)
	before := k.get()
	measured := k.tick()
	if receipt := k.upload(measured.Add(time.Second), k.sample(before, "telecom", "ipv4", "tcp", 1, measured, "success")); receipt[0] != "committed" {
		t.Fatalf("upload: %v", receipt)
	}
	for _, statement := range []string{
		`ALTER TABLE quality_slot_settings DROP COLUMN position`,
		`ALTER TABLE quality_slot_settings DROP COLUMN show_on_card`,
		`ALTER TABLE quality_config DROP COLUMN objects_configured`,
		`DELETE FROM schema_migrations WHERE version=44`,
	} {
		if _, err := k.s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.s.migrate(k.ctx); err != nil {
		t.Fatal(err)
	}
	if version, err := k.s.SchemaVersion(k.ctx); err != nil || version != 44 {
		t.Fatalf("schema version: %d, %v", version, err)
	}
	after := k.get()
	if !slices.Equal(before.Slots, after.Slots) || before.ConfigRevision != after.ConfigRevision || *before.Lanes[0].TargetID != *after.Lanes[0].TargetID {
		t.Fatalf("migration changed slot identity or settings: %+v", after)
	}
	now := measured.Add(time.Minute)
	history, err := k.s.NetworkHistory(k.ctx, k.id, NetworkLine{"telecom", "ipv4", "tcp"}, now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), now)
	if err != nil || history.Stats.Samples != 1 {
		t.Fatalf("migrated history: samples=%d error=%v", history.Stats.Samples, err)
	}
}
