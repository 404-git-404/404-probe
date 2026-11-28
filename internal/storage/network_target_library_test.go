package storage

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

func TestNetworkTargetMutationsRollBackAllAgents(t *testing.T) {
	first := newV4Kit(t)
	s, ctx := first.s, first.ctx
	second := strings.Repeat("b", 32)
	if err := s.AddAgent(ctx, second, "zz-last-agent", auth.Hash(second), first.now); err != nil {
		t.Fatal(err)
	}
	fields := NetworkTargetFields{Region: "Asia", ISP: "ISP", Host: "example.com", Port: 443}
	target, err := s.CreateNetworkTarget(ctx, fields)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{first.id, second}
	if out, err := s.ApplyNetworkTarget(ctx, target.ID, ids, first.tick()); err != nil || len(out.Added) != 2 {
		t.Fatal(out, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE quality_config SET settings_revision=? WHERE agent_id=?`, int64(math.MaxInt64), second); err != nil {
		t.Fatal(err)
	}
	old := first.get()
	now := first.now.Add(time.Minute)
	fields.Host = "1.1.1.1"
	if _, err := s.UpdateNetworkTarget(ctx, target.ID, target.Revision, fields, now); !errors.Is(err, ErrQualityInvalid) {
		t.Fatal("update", err)
	}
	if _, err := s.DeleteNetworkTarget(ctx, target.ID, target.Revision, now); !errors.Is(err, ErrQualityInvalid) {
		t.Fatal("delete", err)
	}
	other, err := s.CreateNetworkTarget(ctx, fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyNetworkTarget(ctx, other.ID, ids, now); !errors.Is(err, ErrQualityInvalid) {
		t.Fatal("apply", err)
	}
	got := first.get()
	if got.SettingsRevision != old.SettingsRevision || got.ConfigRevision != old.ConfigRevision ||
		len(got.Slots) != 1 || got.Slots[0] != old.Slots[0] {
		t.Fatal("first Agent changed", got)
	}
	targets, err := s.ListNetworkTargets(ctx)
	if err != nil || len(targets) != 2 || targets[0].ID != other.ID || len(targets[0].UsedBy) != 0 ||
		targets[1].ID != target.ID || targets[1].Revision != target.Revision || targets[1].Host != target.Host || len(targets[1].UsedBy) != 2 {
		t.Fatal(targets, err)
	}
}

func TestNetworkTargetLegacyLabelsAndImplicitDelete(t *testing.T) {
	k := v4Base(t, false)
	s, ctx := k.s, k.ctx
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT library_id FROM quality_slot_settings WHERE agent_id=? AND slot='telecom'`, k.id).Scan(&id); err != nil {
		t.Fatal(err)
	}
	markLegacy := func() {
		if _, err := s.db.ExecContext(ctx, `UPDATE quality_slot_settings SET legacy_label=1 WHERE agent_id=? AND slot='telecom'`, k.id); err != nil {
			t.Fatal(err)
		}
	}
	assertLabel := func(want int) {
		t.Helper()
		if got := k.count(`SELECT legacy_label FROM quality_slot_settings WHERE agent_id=? AND slot='telecom'`, k.id); got != want {
			t.Fatalf("legacy_label=%d want %d", got, want)
		}
	}
	markLegacy()
	assertLabel(1)
	fields := NetworkTargetFields{Region: "new-region", ISP: k.get().Slots[0].ISP, Host: "example.com", Port: 443}
	if _, err := s.UpdateNetworkTarget(ctx, id, 1, fields, k.tick()); err != nil {
		t.Fatal(err)
	}
	assertLabel(0)
	markLegacy()
	fields.Region = "other-region"
	other, err := s.CreateNetworkTarget(ctx, fields)
	if err != nil {
		t.Fatal(err)
	}
	cfg := k.get()
	cfg.Slots[0].LibraryID = other.ID
	if _, err := s.SaveQualityConfigV4(ctx, k.id, protocol.QualityV4SaveRequest{ExpectedRevision: cfg.SettingsRevision, IntervalMS: cfg.IntervalMS, Slots: cfg.Slots}, k.tick()); err != nil {
		t.Fatal(err)
	}
	assertLabel(0)
	if got := len(k.get().Slots); got != 3 {
		t.Fatalf("implicit layout already replaced: %d", got)
	}
	if out, err := s.DeleteNetworkTarget(ctx, other.ID, other.Revision, k.tick()); err != nil || len(out.Removed) != 1 {
		t.Fatal(out, err)
	}
	if got := k.get(); len(got.Slots) != 0 {
		t.Fatal("deleted target left implicit placeholders", got.Slots)
	}
}

func TestNetworkTargetMutationsSkipIneligibleAgents(t *testing.T) {
	k := newV4Kit(t)
	s, ctx := k.s, k.ctx
	fields := NetworkTargetFields{Region: "region", ISP: "ISP", Host: "example.com", Port: 443}
	target, err := s.CreateNetworkTarget(ctx, fields)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{k.id}
	reasons := map[string]string{}
	for i, reason := range []string{"revoked", "removing", "unsupported", "revoked"} {
		id := strings.Repeat(string(rune('b'+i)), 32)
		if err := s.AddAgent(ctx, id, "fixture-"+id[:1], auth.Hash(id), k.now); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		reasons[id] = reason
	}
	if out, err := s.ApplyNetworkTarget(ctx, target.ID, ids, k.tick()); err != nil || len(out.Added) != len(ids) {
		t.Fatal(out, err)
	}
	before := make(map[string]protocol.QualityV4Config)
	for _, id := range ids {
		before[id], err = s.GetQualityConfigV4(ctx, id, k.now)
		if err != nil {
			t.Fatal(err)
		}
	}
	unsupported := v4Caps(protocol.QualityIPv6None)
	unsupported.ExcludedReason = "platform_excluded:alpine"
	for i := range unsupported.Lanes {
		unsupported.Lanes[i].Supported, unsupported.Lanes[i].Reason = false, "platform_excluded"
	}
	report := validReport(ids[3], 1, "test", "boot", 1, 10, 20)
	report.OS, report.NetworkQualityV2 = "alpine", unsupported
	if _, accepted, why, err := s.ProcessReport(ctx, ids[3], report, k.tick()); err != nil || !accepted {
		t.Fatal(accepted, why, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET revoked=1 WHERE id=?1;
		INSERT INTO agent_removal_operations(operation_id,agent_id,status,requested_epoch,requested_session_id,active_epoch,active_session_id,created_at,updated_at)
		VALUES(?2,?2,'requested',1,'test',1,'test',1,1);
		UPDATE agents SET disabled_at=1 WHERE id=?3`, ids[1], ids[2], ids[4]); err != nil {
		t.Fatal(err)
	}
	assertSkipped := func(skipped []NetworkTargetSkippedAgent) {
		t.Helper()
		if len(skipped) != len(reasons) {
			t.Fatal(skipped)
		}
		for _, skipped := range skipped {
			if reasons[skipped.ID] != skipped.Reason || skipped.Name != "fixture-"+skipped.ID[:1] {
				t.Fatal(skipped)
			}
		}
	}
	fields.Region = "updated"
	updated, err := s.UpdateNetworkTarget(ctx, target.ID, 1, fields, k.tick())
	if err != nil || updated.UpdatedAgents != 1 {
		t.Fatal(updated, err)
	}
	assertSkipped(updated.Skipped)
	deleted, err := s.DeleteNetworkTarget(ctx, target.ID, 2, k.tick())
	if err != nil || len(deleted.Removed) != 1 || deleted.Removed[0].ID != k.id {
		t.Fatal(deleted, err)
	}
	assertSkipped(deleted.Skipped)
	if listed, err := s.ListNetworkTargets(ctx); err != nil || len(listed) != 0 {
		t.Fatal(listed, err)
	}
	for _, id := range ids[1:] {
		got, err := s.GetQualityConfigV4(ctx, id, k.now)
		if err != nil || got.SettingsRevision != before[id].SettingsRevision || len(got.Slots) != 1 || got.Slots[0] != before[id].Slots[0] {
			t.Fatal(id, got, err)
		}
	}
	newTarget, err := s.CreateNetworkTarget(ctx, fields)
	if err != nil || newTarget.ID == target.ID {
		t.Fatal(newTarget, err)
	}
	out, err := s.ApplyNetworkTarget(ctx, newTarget.ID, ids, k.tick())
	if err != nil || len(out.Added) != 1 || out.Added[0] != k.id || len(out.Skipped) != len(reasons) {
		t.Fatal(out, err)
	}
	for _, skipped := range out.Skipped {
		if reasons[skipped.AgentID] != skipped.Reason {
			t.Fatal(skipped)
		}
	}
}
