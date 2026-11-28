package storage

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
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
