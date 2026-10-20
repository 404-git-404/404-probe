package storage

import (
	"errors"
	"testing"

	"404-probe/internal/protocol"
)

// baseline: one slot with all four sub-items, negotiated, master switch on.
//
//	save  => settings 1, measurement 1, 4 targets created at 1
//	enable => settings 2, measurement 2, 4 intervals open from 2
func v4Base(t *testing.T, on bool) *v4Kit {
	t.Helper()
	k := newV4Kit(t)
	k.negotiate(v4Caps(protocol.QualityIPv6Present))
	k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
	if on {
		k.enable(true)
	}
	return k
}

func TestV4SaveReadAndNoSwitchChange(t *testing.T) {
	k := v4Base(t, false)
	cfg := k.get()
	if cfg.SettingsRevision != "1" || cfg.ConfigRevision != "1" || cfg.Enabled || cfg.State != "disabled" || cfg.AgentSupport != protocol.QualitySupportUserTargets {
		t.Fatalf("after save: %+v", cfg)
	}
	if len(cfg.Slots) != 3 || len(cfg.Lanes) != 12 {
		t.Fatalf("slots=%d lanes=%d", len(cfg.Slots), len(cfg.Lanes))
	}
	if got := cfg.Slots[0]; got.Slot != "telecom" || got.Host != "example.com" || got.Port != 443 || got.Kind != "custom" || got.Region != "R-telecom" || !got.IPv6ICMP {
		t.Fatalf("stored slot: %+v", got)
	}
	// Saving never turns detection on, and nothing is open while the switch is off.
	if len(k.open()) != 0 || len(k.targets()) != 4 {
		t.Fatalf("open=%v targets=%d", k.open(), len(k.targets()))
	}
	for _, l := range cfg.Lanes[:4] {
		if l.Status != protocol.QualityLaneDisabled || l.TargetID == nil || slotRevisionOf(l) != 1 {
			t.Fatalf("lane while off: %+v", l)
		}
	}
	for _, l := range cfg.Lanes[4:] {
		if l.Status != protocol.QualityLaneDisabled || l.TargetID != nil || l.SlotRevision != "0" {
			t.Fatalf("empty slot lane: %+v", l)
		}
	}
	k.invariants()
	// An identical save changes nothing at all.
	before := k.summary()
	same := k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
	if same.SettingsRevision != "1" || same.ConfigRevision != "1" || k.summary() != before {
		t.Fatalf("identical save moved something: %+v", same)
	}
	// Turning on opens one interval per ticked lane, from the new measurement revision.
	on := k.enable(true)
	if on.SettingsRevision != "2" || on.ConfigRevision != "2" || !on.Enabled || on.State != "ready" || len(k.open()) != 4 {
		t.Fatalf("after enable: %+v open=%d", on, len(k.open()))
	}
	for _, iv := range k.open() {
		if iv.From != 2 {
			t.Fatalf("interval starts at %d", iv.From)
		}
	}
	for _, l := range on.Lanes[:4] {
		if l.Status != protocol.QualityLaneActive || slotRevisionOf(l) != 2 {
			t.Fatalf("active lane: %+v", l)
		}
	}
	for _, target := range k.targets() {
		if !target.FirstActive || target.Created != 1 {
			t.Fatalf("target %+v: created at the save, first active only after the switch", target)
		}
	}
	k.invariants()
}

func TestV4IdentityRules(t *testing.T) {
	t.Run("only labels or type", func(t *testing.T) {
		k := v4Base(t, true)
		before := k.get()
		usage := k.count(`SELECT targets FROM quality_usage WHERE agent_id=?`, k.id)
		moved := v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp")
		moved.Kind, moved.Region, moved.ISP = "custom", "Elsewhere", "Another ISP"
		after := k.save(30000, moved)
		if after.SettingsRevision != "3" || after.ConfigRevision != before.ConfigRevision {
			t.Fatalf("revisions %s/%s -> %s/%s", before.SettingsRevision, before.ConfigRevision, after.SettingsRevision, after.ConfigRevision)
		}
		for i, l := range after.Lanes {
			b := before.Lanes[i]
			if l.SlotRevision != b.SlotRevision || (l.TargetID == nil) != (b.TargetID == nil) || (l.TargetID != nil && *l.TargetID != *b.TargetID) || l.Status != b.Status {
				t.Fatalf("lane %d changed by a label edit: %+v -> %+v", i, b, l)
			}
		}
		if k.count(`SELECT targets FROM quality_usage WHERE agent_id=?`, k.id) != usage || len(k.open()) != 4 {
			t.Fatal("a label edit consumed quota or touched an interval")
		}
		if after.Slots[0].Region != "Elsewhere" || after.Slots[0].ISP != "Another ISP" {
			t.Fatalf("labels not stored: %+v", after.Slots[0])
		}
		// Switching the type alone is a label edit too.
		builtin := v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp")
		builtin.Kind, builtin.Region, builtin.ISP = "builtin", "广东", "telecom"
		third := k.save(30000, builtin)
		if third.SettingsRevision != "4" || third.ConfigRevision != before.ConfigRevision {
			t.Fatalf("type switch: %+v", third)
		}
		k.invariants()
	})
	t.Run("address change with the switch on", func(t *testing.T) {
		k := v4Base(t, true)
		old := k.get()
		after := k.save(30000, v4Slot("telecom", "example.org", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
		if after.SettingsRevision != "3" || after.ConfigRevision != "3" {
			t.Fatalf("revisions: %+v", after)
		}
		for i, l := range after.Lanes[:4] {
			if l.TargetID == nil || *l.TargetID == *old.Lanes[i].TargetID || slotRevisionOf(l) != 3 || l.Status != protocol.QualityLaneActive {
				t.Fatalf("lane %d: %+v (old %+v)", i, l, old.Lanes[i])
			}
		}
		targets := k.targets()
		for i, l := range old.Lanes[:4] {
			if !targets[*l.TargetID].Retired {
				t.Fatalf("old target of lane %d is not retired", i)
			}
			if targets[*after.Lanes[i].TargetID].Created != 3 {
				t.Fatal("new target not created at the new measurement revision")
			}
		}
		closed := 0
		for _, iv := range k.intervals() {
			if !iv.Open && iv.Until != nil && *iv.Until == 2 && iv.From == 2 {
				closed++
			}
			if iv.Open && iv.From != 3 {
				t.Fatalf("new interval starts at %d", iv.From)
			}
		}
		if closed != 4 || len(k.open()) != 4 {
			t.Fatalf("closed=%d open=%d: %s", closed, len(k.open()), k.summary())
		}
		k.invariants()
	})
	t.Run("address change with the switch off: A to B to A is three targets", func(t *testing.T) {
		k := v4Base(t, false)
		ids := []string{k.targetID(k.get(), "telecom", "ipv4", "tcp")}
		for i, host := range []string{"example.org", "example.com"} {
			cfg := k.save(30000, v4Slot("telecom", host, 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
			if cfg.ConfigRevision != []string{"2", "3"}[i] {
				t.Fatalf("step %d: %+v", i, cfg)
			}
			ids = append(ids, k.targetID(cfg, "telecom", "ipv4", "tcp"))
		}
		if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
			t.Fatalf("a changed address back to an old one reused a target: %v", ids)
		}
		if len(k.intervals()) != 0 {
			t.Fatal("an interval opened while the switch was off")
		}
		targets := k.targets()
		if targets[ids[2]].Identity != targets[ids[0]].Identity || targets[ids[2]].Created != 3 || targets[ids[0]].Created != 1 {
			t.Fatalf("identity is the address, the version makes the id: %+v %+v", targets[ids[0]], targets[ids[2]])
		}
		k.invariants()
	})
	t.Run("a TCP port change leaves the ICMP target alone", func(t *testing.T) {
		k := v4Base(t, true)
		old := k.get()
		after := k.save(30000, v4Slot("telecom", "example.com", 8443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
		for _, l := range after.Lanes[:4] {
			oldLane := k.lane(old, l.Slot, l.Family, l.Protocol)
			switch l.Protocol {
			case "tcp":
				if *l.TargetID == *oldLane.TargetID || slotRevisionOf(l) != 3 {
					t.Fatalf("tcp lane kept its target: %+v", l)
				}
			case "icmp":
				if *l.TargetID != *oldLane.TargetID || slotRevisionOf(l) != 2 {
					t.Fatalf("icmp lane changed with a TCP port: %+v", l)
				}
			}
		}
		for _, iv := range k.open() {
			want := int64(2)
			if iv.Proto == "tcp" {
				want = 3
			}
			if iv.From != want {
				t.Fatalf("interval %+v should start at %d", iv, want)
			}
		}
		k.invariants()
	})
}

func TestV4VersionRulesTable(t *testing.T) {
	t.Run("sub-item toggled off and on", func(t *testing.T) {
		k := v4Base(t, true)
		old := k.get()
		after := k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp"))
		if after.ConfigRevision != "3" || after.SettingsRevision != "3" {
			t.Fatalf("%+v", after)
		}
		off := k.lane(after, "telecom", "ipv6", "icmp")
		if off.Status != protocol.QualityLaneInactive || slotRevisionOf(off) != 3 || *off.TargetID != *k.lane(old, "telecom", "ipv6", "icmp").TargetID {
			t.Fatalf("unticked lane: %+v", off)
		}
		if _, still := k.open()["telecom/ipv6/icmp"]; still {
			t.Fatal("interval still open")
		}
		for _, l := range after.Lanes[:3] {
			if slotRevisionOf(l) != 2 {
				t.Fatalf("a lane that did not change got slot revision %d", slotRevisionOf(l))
			}
		}
		again := k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
		back := k.lane(again, "telecom", "ipv6", "icmp")
		if again.ConfigRevision != "4" || *back.TargetID != *off.TargetID || slotRevisionOf(back) != 4 || k.open()["telecom/ipv6/icmp"].From != 4 {
			t.Fatalf("ticked again: %+v", back)
		}
		k.invariants()
	})
	t.Run("unticked sub-item while the slot is unticked changes no lane revision", func(t *testing.T) {
		k := v4Base(t, true)
		k.save(30000, v4Slot("telecom", "example.com", 443, "off", "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
		cfg := k.get()
		before := k.summary()
		slotRevs := map[string]int64{}
		for _, l := range cfg.Lanes[:4] {
			slotRevs[l.Family+l.Protocol] = slotRevisionOf(l)
		}
		after := k.save(30000, v4Slot("telecom", "example.com", 443, "off", "v4tcp", "v4icmp", "v6tcp"))
		if after.ConfigRevision != "4" { // the raw tick changed: a measurement change
			t.Fatalf("%+v", after)
		}
		for _, l := range after.Lanes[:4] {
			if slotRevisionOf(l) != slotRevs[l.Family+l.Protocol] {
				t.Fatalf("lane %s%s: the synthetic enabled flag did not change, so its slot revision must not move", l.Family, l.Protocol)
			}
		}
		if k.summary() != before {
			t.Fatalf("intervals changed while the slot stayed unticked:\n%s\n%s", before, k.summary())
		}
		k.invariants()
	})
	t.Run("frequency only, switch on", func(t *testing.T) {
		k := v4Base(t, true)
		old := k.get()
		after := k.save(120000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
		if after.SettingsRevision != "3" || after.ConfigRevision != "3" || after.IntervalMS != 120000 || after.EffectiveIntervalMS != 120000 {
			t.Fatalf("%+v", after)
		}
		for i, l := range after.Lanes[:4] {
			if slotRevisionOf(l) != slotRevisionOf(old.Lanes[i]) || *l.TargetID != *old.Lanes[i].TargetID {
				t.Fatalf("frequency change moved lane %d", i)
			}
		}
		for _, iv := range k.open() {
			if iv.From != 2 || iv.Until != nil {
				t.Fatalf("an open interval was disturbed: %+v", iv)
			}
		}
		k.invariants()
	})
	t.Run("frequency only, switch off", func(t *testing.T) {
		k := v4Base(t, false)
		after := k.save(300000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
		if after.SettingsRevision != "2" || after.ConfigRevision != "2" || len(k.intervals()) != 0 {
			t.Fatalf("%+v", after)
		}
	})
	t.Run("master switch with executable lanes", func(t *testing.T) {
		k := v4Base(t, true)
		off := k.enable(false)
		if off.SettingsRevision != "3" || off.ConfigRevision != "3" || len(k.open()) != 0 {
			t.Fatalf("%+v open=%d", off, len(k.open()))
		}
		for _, l := range off.Lanes[:4] {
			if slotRevisionOf(l) != 3 || l.Status != protocol.QualityLaneDisabled {
				t.Fatalf("%+v", l)
			}
		}
		on := k.enable(true)
		if on.ConfigRevision != "4" || len(k.open()) != 4 {
			t.Fatalf("%+v", on)
		}
		k.invariants()
	})
	t.Run("master switch without executable lanes", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present))
		before := k.count(`SELECT COUNT(*) FROM quality_slots WHERE agent_id=?`, k.id)
		on := k.enable(true)
		if on.SettingsRevision != "1" || on.ConfigRevision != "1" || !on.Enabled || on.State != "unset" || len(k.intervals()) != 0 {
			t.Fatalf("%+v", on)
		}
		if k.count(`SELECT COUNT(*) FROM quality_slots WHERE agent_id=?`, k.id) != before {
			t.Fatal("slot rows were written for nothing")
		}
		k.enable(false)
		if _, rev := k.revisions(); rev != 2 {
			t.Fatalf("revision %d", rev)
		}
	})
	t.Run("several changes in one save move each revision once", func(t *testing.T) {
		k := v4Base(t, true)
		after := k.save(60000,
			v4Slot("telecom", "example.org", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"),
			v4Slot("unicom", "unicom.example.net", 80, "v4tcp"),
			v4Slot("mobile", "8.8.8.8", 53, "v4icmp"))
		if after.SettingsRevision != "3" || after.ConfigRevision != "3" {
			t.Fatalf("%+v", after)
		}
		for _, target := range k.targets() {
			if target.Created != 1 && target.Created != 3 {
				t.Fatalf("a target was created at %d", target.Created)
			}
		}
		for _, l := range after.Lanes {
			if l.TargetID != nil && slotRevisionOf(l) != 3 {
				t.Fatalf("lane %s/%s/%s: slot revision %d", l.Slot, l.Family, l.Protocol, slotRevisionOf(l))
			}
		}
		if len(k.open()) != 4+1+1 {
			t.Fatalf("open: %s", k.summary())
		}
		// A literal address only creates lanes of its own family as intervals.
		if _, ok := k.open()["mobile/ipv4/icmp"]; !ok {
			t.Fatal("literal IPv4 ICMP lane did not open")
		}
		k.invariants()
	})
	t.Run("recalculation without a change writes nothing", func(t *testing.T) {
		k := v4Base(t, true)
		before, revisionBefore := k.summary(), k.count(`SELECT revision FROM quality_config WHERE agent_id=?`, k.id)
		r := k.report(true, v4Caps(protocol.QualityIPv6Present))
		k.reconcile(r)
		k.reconcile(r)
		if k.summary() != before || k.count(`SELECT revision FROM quality_config WHERE agent_id=?`, k.id) != revisionBefore {
			t.Fatal("an unchanged capability moved the configuration")
		}
	})
}

func TestV4ConflictsAndIdempotentRequests(t *testing.T) {
	k := v4Base(t, false)
	if _, err := k.s.SaveQualityConfigV4(k.ctx, k.id, protocol.QualityV4SaveRequest{ExpectedRevision: "0", IntervalMS: 30000, Slots: v4Slots()}, k.tick()); !errors.Is(err, ErrQualityConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if _, err := k.s.SaveQualityConfigV4(k.ctx, k.id, protocol.QualityV4SaveRequest{ExpectedRevision: "x", IntervalMS: 30000, Slots: v4Slots()}, k.tick()); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("bad revision: %v", err)
	}
	if _, err := k.s.SetQualityEnabledV4(k.ctx, k.id, protocol.QualityV4EnabledRequest{ExpectedRevision: "1"}, k.tick()); !errors.Is(err, ErrQualityInvalid) {
		t.Fatalf("missing enabled: %v", err)
	}
	// Switching on must carry the version the user saw.
	if _, err := k.tryEnable(true, "0"); !errors.Is(err, ErrQualityConflict) {
		t.Fatalf("stale enable: %v", err)
	}
	on := k.enable(true)
	if on.SettingsRevision != "2" {
		t.Fatalf("%+v", on)
	}
	// true -> true with the current version is a no-op; with an old one it conflicts.
	same, err := k.tryEnable(true, on.SettingsRevision)
	if err != nil || same.SettingsRevision != "2" || same.ConfigRevision != on.ConfigRevision {
		t.Fatalf("no-op enable: %+v %v", same, err)
	}
	if _, err := k.tryEnable(true, "1"); !errors.Is(err, ErrQualityConflict) {
		t.Fatalf("stale true->true: %v", err)
	}
	// Switching off is the safe direction: any version, and repeating it is a no-op.
	off, err := k.tryEnable(false, "0")
	if err != nil || off.Enabled || off.SettingsRevision != "3" {
		t.Fatalf("off with a stale version: %+v %v", off, err)
	}
	again, err := k.tryEnable(false, "0")
	if err != nil || again.SettingsRevision != "3" || again.ConfigRevision != off.ConfigRevision {
		t.Fatalf("off twice: %+v %v", again, err)
	}
	if _, err := k.s.SaveQualityConfigV4(k.ctx, "missing-agent", protocol.QualityV4SaveRequest{ExpectedRevision: "0", IntervalMS: 30000, Slots: v4Slots()}, k.now); err == nil {
		t.Fatal("saving for a missing Agent succeeded")
	}
}

func TestV4InputsAreValidatedInStorage(t *testing.T) {
	k := v4Base(t, false)
	revision := k.get().SettingsRevision
	for name, mutate := range map[string]func(*protocol.QualityV4SaveRequest){
		"interval too short":     func(r *protocol.QualityV4SaveRequest) { r.IntervalMS = 20000 },
		"interval off the grid":  func(r *protocol.QualityV4SaveRequest) { r.IntervalMS = 45000 },
		"interval too long":      func(r *protocol.QualityV4SaveRequest) { r.IntervalMS = 330000 },
		"two slots":              func(r *protocol.QualityV4SaveRequest) { r.Slots = r.Slots[:2] },
		"duplicate slot":         func(r *protocol.QualityV4SaveRequest) { r.Slots[1] = r.Slots[0] },
		"private address":        func(r *protocol.QualityV4SaveRequest) { r.Slots[0] = v4Slot("telecom", "10.0.0.1", 80, "v4tcp") },
		"labels required":        func(r *protocol.QualityV4SaveRequest) { r.Slots[0].Region = "" },
		"v6 sub-item on v4 host": func(r *protocol.QualityV4SaveRequest) { r.Slots[0] = v4Slot("telecom", "8.8.8.8", 53, "v6tcp") },
		"v4 sub-item on v6 host": func(r *protocol.QualityV4SaveRequest) {
			r.Slots[0] = v4Slot("telecom", "2606:4700:4700::1111", 53, "v4tcp")
		},
		"reserved name":           func(r *protocol.QualityV4SaveRequest) { r.Slots[0] = v4Slot("telecom", "x.local", 80, "v4tcp") },
		"port missing":            func(r *protocol.QualityV4SaveRequest) { r.Slots[0] = v4Slot("telecom", "example.com", 0, "v4tcp") },
		"builtin with free text":  func(r *protocol.QualityV4SaveRequest) { r.Slots[0].Kind, r.Slots[0].Region = "builtin", "某机房" },
		"custom label with space": func(r *protocol.QualityV4SaveRequest) { r.Slots[0].Region = " x" },
	} {
		request := protocol.QualityV4SaveRequest{ExpectedRevision: revision, IntervalMS: 30000, Slots: v4Slots(v4Slot("telecom", "example.com", 443, "v4tcp"))}
		mutate(&request)
		if _, err := k.s.SaveQualityConfigV4(k.ctx, k.id, request, k.tick()); !errors.Is(err, ErrQualityInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := k.get(); got.SettingsRevision != revision {
		t.Fatalf("a rejected save moved the revision: %+v", got)
	}
	// A host is stored in its canonical form: case and a trailing dot do not make a new target.
	first := k.save(30000, v4Slot("telecom", "Example.COM.", 443, "v4tcp"))
	if first.Slots[0].Host != "example.com" {
		t.Fatalf("host not canonical: %q", first.Slots[0].Host)
	}
	again := k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp"))
	if again.SettingsRevision != first.SettingsRevision {
		t.Fatalf("the same host written differently is a change: %+v", again)
	}
	mapped := k.save(30000, v4Slot("telecom", "::ffff:8.8.8.8", 53, "v4tcp"))
	if mapped.Slots[0].Host != "8.8.8.8" {
		t.Fatalf("mapped address stored as %q", mapped.Slots[0].Host)
	}
}

func TestV4QuotaIsAtomic(t *testing.T) {
	k := v4Base(t, true)
	// Fill the device quota with dummy targets; a save that would exceed it must change nothing.
	room := qualityTargetDevice - k.count(`SELECT COUNT(*) FROM quality_targets WHERE agent_id=?`, k.id)
	for i := 0; i < room-2; i++ {
		id := m34Hex(900000 + i)
		if _, err := k.s.db.Exec(`INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at,retired_at) VALUES(?,?,?,?,1,1)`, id, k.id, id, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	before := k.summary()
	settings, revision := k.revisions()
	_, err := k.trySave(30000, v4Slot("telecom", "example.org", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
	if !errors.Is(err, ErrQualityQuota) {
		t.Fatalf("over quota: %v", err)
	}
	s2, r2 := k.revisions()
	if s2 != settings || r2 != revision || k.summary() != before || k.get().Slots[0].Host != "example.com" {
		t.Fatal("a failed save left changes behind")
	}
	// Label edits never consume quota.
	edit := v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp")
	edit.Region = "renamed"
	if _, err := k.trySave(30000, edit); err != nil {
		t.Fatalf("label edit at the quota limit: %v", err)
	}
}
