package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

// The front-end tests of stage D use real responses of the Server, not hand-written DTOs. This test
// writes them when R07_FIXTURES is set (a directory) and otherwise only checks that the committed
// files still decode strictly into the Server's own types.
//
//	R07_FIXTURES=../../scripts/fixtures/r07 go test ./internal/storage -run TestFrontendFixtures
const fixtureAgent = "f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1"

func fixtureKit(t *testing.T) *v4Kit {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	_, hash, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgent(context.Background(), fixtureAgent, "fixture", hash, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	return &v4Kit{t: t, s: s, id: fixtureAgent, now: time.Unix(1800000000, 0), epoch: 1, session: "fixture-session", ctx: context.Background()}
}

func fixtureSlots() []protocol.QualityV4Slot {
	telecom := protocol.QualityV4Slot{Slot: "telecom", Kind: "builtin", Region: "广东", ISP: "telecom", Host: "example.com", Port: 443, SlotEnabled: true, IPv4TCP: true, IPv4ICMP: true, IPv6TCP: true, IPv6ICMP: true}
	unicom := protocol.QualityV4Slot{Slot: "unicom", Kind: "custom", Region: "Tokyo", ISP: "NTT", Host: "93.184.216.34", Port: 80, SlotEnabled: true, IPv4TCP: true, IPv4ICMP: true}
	return []protocol.QualityV4Slot{telecom, unicom}
}

func marshalFixture(t *testing.T, v any) []byte {
	t.Helper()
	wire, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(wire, '\n')
}

func TestFrontendFixtures(t *testing.T) {
	dir := os.Getenv("R07_FIXTURES")
	if dir == "" {
		dir = filepath.Join("..", "..", "scripts", "fixtures", "r07")
		files, err := filepath.Glob(filepath.Join(dir, "v4-*.json"))
		if err != nil || len(files) == 0 {
			t.Skip("fixtures are not committed yet")
		}
		for _, file := range files {
			wire, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var cfg protocol.QualityV4Config
			decoder := json.NewDecoder(bytes.NewReader(wire))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&cfg); err != nil || len(cfg.Slots) != 3 || len(cfg.Lanes) != 12 {
				t.Fatalf("%s: %v slots=%d lanes=%d", file, err, len(cfg.Slots), len(cfg.Lanes))
			}
		}
		files, _ = filepath.Glob(filepath.Join(dir, "tcp-mini-*.json"))
		for _, file := range files {
			wire, _ := os.ReadFile(file)
			var status TCPMiniStatus
			decoder := json.NewDecoder(bytes.NewReader(wire))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&status); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
		}
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		if err := os.WriteFile(filepath.Join(dir, name), marshalFixture(t, v), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Never configured: the Agent has not reported quality yet.
	k := fixtureKit(t)
	write("v4-empty-unreported.json", k.get())

	// An old Agent: reports without the negotiation, targets saved and switched on.
	k = fixtureKit(t)
	k.report(false, v4Caps(protocol.QualityIPv6Present))
	k.save(60000, fixtureSlots()...)
	k.enable(true)
	write("v4-old-agent.json", k.get())

	// A restarted Agent whose earlier grant stands: step one done, step two (the capability) still to come.
	k = fixtureKit(t)
	k.negotiate(v4Caps(protocol.QualityIPv6Present))
	k.save(60000, fixtureSlots()...)
	k.enable(true)
	k.restart()
	k.reconcile(k.report(true, nil))
	write("v4-negotiating.json", k.get())

	// First upgrade from an old Agent: nothing was ever granted, so the same first step reads as unsupported.
	k = fixtureKit(t)
	k.report(false, v4Caps(protocol.QualityIPv6Present))
	k.save(60000, fixtureSlots()...)
	k.restart()
	k.reconcile(k.report(true, nil))
	write("v4-first-upgrade.json", k.get())

	// A current Agent, configuration saved, detection off.
	k = fixtureKit(t)
	k.negotiate(v4Caps(protocol.QualityIPv6Present))
	k.save(60000, fixtureSlots()...)
	write("v4-saved-off.json", k.get())

	// Detection on and running.
	k.enable(true)
	write("v4-running.json", k.get())

	// A host without IPv6 and without ICMP permission.
	k = fixtureKit(t)
	k.negotiate(v4Caps(protocol.QualityIPv6None, "ipv4/icmp", "ipv6/icmp"))
	k.save(30000, fixtureSlots()...)
	k.enable(true)
	write("v4-no-ipv6-no-icmp.json", k.get())

	// The telecom target does not exist: three not_found events in a row.
	k = fixtureKit(t)
	k.negotiate(v4Caps(protocol.QualityIPv6Present))
	k.save(30000, fixtureSlots()...)
	cfg := k.enable(true)
	k.now = k.now.Add(10 * time.Minute)
	for i := 1; i <= 3; i++ {
		at := k.now.Add(-time.Duration(4-i) * 30 * time.Second)
		if acks := k.upload(k.now, k.sample(cfg, "telecom", "ipv4", "tcp", i, at, "not_found")); acks[0] != "committed" {
			t.Fatal(acks)
		}
	}
	write("v4-target-abnormal.json", k.get())

	// TCP Mini: an alert of a running TCP lane, and the same alert after detection was switched off
	// (what an upgraded device shows: the evidence stays, no TCP lane is open).
	k = fixtureKit(t)
	k.negotiate(v4Caps(protocol.QualityIPv6Present))
	k.save(30000, fixtureSlots()...)
	k.enable(true)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	lastHigh := now.Add(-26 * time.Hour)
	for _, q := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO tcp_mini_state(agent_id,evidence_start,scanned_at,initialized,last_high,last_low,revision) VALUES(?,?,?,1,?,0,4)`, []any{k.id, now.Add(-120 * time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli(), lastHigh.UnixMilli()}},
		{`INSERT INTO tcp_mini_high(agent_id,minute,peak_ms,source) VALUES(?,?,?,'raw')`, []any{k.id, lastHigh.Truncate(time.Minute).UnixMilli(), 4700.0}},
	} {
		if _, err := k.s.db.Exec(q.query, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	status, err := k.s.TCPMini(k.ctx, k.id, "UTC", now)
	if err != nil || status.State != "alert" || !status.CurrentTCP {
		t.Fatalf("%+v %v", status, err)
	}
	write("tcp-mini-alert-running.json", status)
	k.enable(false)
	status, err = k.s.TCPMini(k.ctx, k.id, "UTC", now)
	if err != nil || status.State != "alert" || status.CurrentTCP {
		t.Fatalf("%+v %v", status, err)
	}
	write("tcp-mini-alert-switched-off.json", status)
	k = fixtureKit(t)
	status, err = k.s.TCPMini(k.ctx, k.id, "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	write("tcp-mini-none.json", status)
}
