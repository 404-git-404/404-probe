package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func m10Success(family, ip string, at int64) protocol.EgressObservation {
	return protocol.EgressObservation{Family: family, Status: "available", IP: ip, CheckedAt: at, ValidUntil: at + protocol.EgressSuccessTTL.Milliseconds(), Reason: "verified"}
}

func TestM10PersistenceOrderingHistoryAndLegacy(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "m10.db")
	s, id, _ := testStore(t, file)
	now := time.Now()
	r := validReport(id, 1, "s1", "boot", 1, 10, 20)
	r.CollectedAt = now.UnixMilli()
	r.PublicEgress = &protocol.PublicEgress{Version: 1, IPv4: m10Success("ipv4", "8.8.8.8", r.CollectedAt), IPv6: m10Success("ipv6", "2606:4700::1111", r.CollectedAt)}
	state, ok, _, err := s.ProcessReport(ctx, id, r, now)
	if err != nil || !ok || state.PublicEgress.IPv6.IP == "" {
		t.Fatal(state, ok, err)
	}
	r.Sequence++
	r.PublicEgress.IPv4.CheckedAt -= 1000
	r.PublicEgress.IPv4.ValidUntil -= 1000
	r.PublicEgress.IPv4.IP = "1.1.1.1"
	state, ok, _, err = s.ProcessReport(ctx, id, r, now)
	if err != nil || !ok || state.PublicEgress.IPv4.IP != "8.8.8.8" {
		t.Fatal("family regression", state, ok, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	states, err := s.ListStates(ctx)
	if err != nil || len(states) != 1 || states[0].PublicEgress.IPv4.IP != "8.8.8.8" {
		t.Fatal("restart", states, err)
	}
	r.Epoch = 2
	r.SessionID = "s2"
	r.Sequence = 1
	r.PublicEgress = nil
	state, ok, _, err = s.ProcessReport(ctx, id, r, now)
	if err != nil || !ok || state.PublicEgress.Supported || state.PublicEgress.IPv4.Status != "unknown" || state.PublicEgress.IPv4.IP != "" || state.PublicEgress.IPv4.LastSuccess == nil {
		t.Fatal("legacy/session", state, ok, err)
	}
	old := r
	old.Epoch = 1
	old.SessionID = "s1"
	old.Sequence = 999
	_, ok, _, err = s.ProcessReport(ctx, id, old, now)
	if err != nil || ok {
		t.Fatal("old epoch accepted", err)
	}
}

func TestM10MigrationFrom27(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "upgrade.db")
	s, id, _ := testStore(t, file)
	if _, _, _, err := s.ProcessReport(ctx, id, validReport(id, 1, "s", "b", 1, 1, 1), time.Now()); err != nil {
		t.Fatal(err)
	}
	dropTCPMiniFixtureSchema(t, s.db)
	if _, err := s.db.Exec(`DROP TABLE agent_public_egress; DELETE FROM schema_migrations WHERE version=28`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	states, err := s.ListStates(ctx)
	if err != nil || len(states) != 1 || states[0].PublicEgress.IPv4.Status != "unknown" {
		t.Fatal(states, err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&n); err != nil || n != currentSchemaVersion {
		t.Fatal(n, err)
	}
}
