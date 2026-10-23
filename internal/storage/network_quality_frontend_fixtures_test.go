package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/protocol"
)

// The front-end tests of stage D use real responses of the Server, not hand-written DTOs. This test
// writes them when R07_FIXTURES is set (a directory). Otherwise it checks that the committed files decode strictly
// into the Server's own types and that regenerating them gives the very same bytes, so a change of the Server's
// answers cannot leave the front-end tests running on stale fixtures.
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
		committed := filepath.Join("..", "..", "scripts", "fixtures", "r07")
		dir = t.TempDir()
		t.Cleanup(func() {
			generated, _ := filepath.Glob(filepath.Join(dir, "*.json"))
			existing, _ := filepath.Glob(filepath.Join(committed, "*.json"))
			// tcp-mini-*.json are static copies of the retired TCP Mini answers (schema 35). The v1.0.6 page
			// scripts still load them until the card rewrite replaces those scripts.
			existing = slices.DeleteFunc(existing, func(file string) bool { return strings.HasPrefix(filepath.Base(file), "tcp-mini-") })
			if len(generated) != len(existing) || len(generated) < 10 {
				t.Errorf("the generator wrote %d fixtures, %d are committed", len(generated), len(existing))
			}
			for _, file := range generated {
				want, err := os.ReadFile(filepath.Join(committed, filepath.Base(file)))
				got, _ := os.ReadFile(file)
				if err != nil || !bytes.Equal(got, want) {
					t.Errorf("%s is out of date: regenerate with R07_FIXTURES=../../scripts/fixtures/r07", filepath.Base(file))
				}
			}
		})
		files, err := filepath.Glob(filepath.Join(committed, "v4-*.json"))
		if err != nil || len(files) < 8 {
			t.Fatalf("committed v4 fixtures: %v %d", err, len(files))
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
		// The history answers carry the id of the lane they were read for.
		var running protocol.QualityV4Config
		wire, err := os.ReadFile(filepath.Join(committed, "v4-running.json"))
		if err != nil || json.Unmarshal(wire, &running) != nil {
			t.Fatalf("v4-running: %v", err)
		}
		var laneTarget string
		for _, l := range running.Lanes {
			if l.Slot == "telecom" && l.Family == "ipv4" && l.Protocol == "tcp" && l.TargetID != nil {
				laneTarget = *l.TargetID
			}
		}
		for _, name := range []string{"history-card-tcp.json", "history-1day-tcp.json"} {
			wire, err := os.ReadFile(filepath.Join(committed, name))
			if err != nil {
				t.Fatal(err)
			}
			var h QualityHistoryRollup
			decoder := json.NewDecoder(bytes.NewReader(wire))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&h); err != nil || laneTarget == "" || h.Target.ID != laneTarget {
				t.Fatalf("%s: %v target.id %q, lane target %q", name, err, h.Target.ID, laneTarget)
			}
		}
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

	// The history read models of the running telecom IPv4 TCP lane, exactly as the Server answers them: the page must
	// accept these (R07 stage E, D1: they once came without the target id and every curve stayed empty).
	running := k.get()
	for i := 1; i <= 4; i++ {
		at := k.now.Add(time.Duration(i) * time.Minute)
		if acks := k.upload(at.Add(time.Second), k.sample(running, "telecom", "ipv4", "tcp", i, at, "success")); acks[0] != "committed" {
			t.Fatal(acks)
		}
	}
	historyNow := k.now.Add(5 * time.Minute)
	runningTarget := k.targetID(running, "telecom", "ipv4", "tcp")
	card, err := k.s.QualityCardHistory(k.ctx, k.id, runningTarget, historyNow)
	if err != nil {
		t.Fatal(err)
	}
	write("history-card-tcp.json", card)
	day, err := k.s.QualityHistoryRollup(k.ctx, k.id, runningTarget, "1day", historyNow)
	if err != nil {
		t.Fatal(err)
	}
	write("history-1day-tcp.json", day)

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
}
