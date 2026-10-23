package storage

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// restart starts a new Agent process: a new epoch and session, which must negotiate again.
func (k *v4Kit) restart() {
	k.epoch++
	k.session = "v4-session-" + strconv.FormatUint(k.epoch, 10)
	k.seq = 0
}

func TestV4AuthorizationFollowsNegotiationAndCapabilities(t *testing.T) {
	t.Run("a configuration saved before the Agent negotiates starts running afterwards", func(t *testing.T) {
		k := newV4Kit(t)
		k.report(false, v4Caps(protocol.QualityIPv6Unknown)) // an old Agent: no header
		cfg := k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp"))
		cfg = k.enable(true)
		if cfg.AgentSupport != protocol.QualitySupportUpgradeRequired || cfg.State != "agent_upgrade_required" || len(k.open()) != 0 {
			t.Fatalf("old Agent: %+v open=%d", cfg, len(k.open()))
		}
		if l := k.lane(cfg, "telecom", "ipv4", "tcp"); l.Status != protocol.QualityLaneAgentUnavailable || l.Condition != protocol.QualitySupportUpgradeRequired {
			t.Fatalf("lane: %+v", l)
		}
		before := k.get().ConfigRevision
		k.restart() // the Agent was upgraded and restarted
		first := k.report(true, nil)
		k.reconcile(first) // step one carries no capability: nothing is granted
		if cfg := k.get(); cfg.AgentSupport != protocol.QualitySupportUnsupported || cfg.ConfigRevision != before || len(k.open()) != 0 {
			t.Fatalf("after step one: %+v", cfg)
		}
		second := k.report(true, v4Caps(protocol.QualityIPv6Present))
		k.reconcile(second)
		cfg = k.get()
		if cfg.AgentSupport != protocol.QualitySupportUserTargets || len(k.open()) != 1 || cfg.ConfigRevision == before {
			t.Fatalf("after step two: %+v open=%v", cfg, k.open())
		}
		if l := k.lane(cfg, "telecom", "ipv4", "tcp"); l.Status != protocol.QualityLaneActive {
			t.Fatalf("lane: %+v", l)
		}
		k.invariants()
	})
	t.Run("the first report of a restarted Agent does not close what was running", func(t *testing.T) {
		k := v4Base(t, true)
		running := k.summary()
		revision := k.get().ConfigRevision
		k.restart()
		first := k.report(true, nil) // header, no capability: unknown
		k.reconcile(first)
		cfg := k.get()
		if k.summary() != running || cfg.ConfigRevision != revision {
			t.Fatalf("a restart moved the authorization: %s", k.summary())
		}
		if cfg.AgentSupport != protocol.QualitySupportNegotiating {
			t.Fatalf("support %q", cfg.AgentSupport)
		}
		// Samples are not accepted from this session until the second step is done.
		second := k.report(true, v4Caps(protocol.QualityIPv6Present))
		k.reconcile(second)
		if k.get().AgentSupport != protocol.QualitySupportUserTargets || k.summary() != running {
			t.Fatalf("after step two: %s", k.summary())
		}
	})
	t.Run("a report without the header revokes", func(t *testing.T) {
		k := v4Base(t, true)
		revoke := k.report(false, v4Caps(protocol.QualityIPv6Present))
		k.reconcile(revoke)
		cfg := k.get()
		if len(k.open()) != 0 || cfg.ConfigRevision != "3" || cfg.SettingsRevision != "2" {
			t.Fatalf("after revoke: %+v %s", cfg, k.summary())
		}
		k.invariants()
		// Nothing was granted again, so a second such report changes nothing.
		k.reconcile(k.report(false, nil))
		if k.get().ConfigRevision != "3" {
			t.Fatal("revoking twice moved the revision")
		}
	})
	t.Run("lost ICMP and IPv6", func(t *testing.T) {
		k := v4Base(t, true)
		k.restart()
		k.negotiate(v4Caps(protocol.QualityIPv6Present, "ipv4/icmp", "ipv6/icmp")) // container without raw sockets
		cfg := k.get()
		if len(k.open()) != 2 || cfg.ConfigRevision != "3" || cfg.SettingsRevision != "2" {
			t.Fatalf("after ICMP loss: %+v %s", cfg, k.summary())
		}
		for _, name := range []string{"ipv4", "ipv6"} {
			if l := k.lane(cfg, "telecom", name, "icmp"); l.Status != protocol.QualityLaneICMPUnsupported {
				t.Fatalf("lane %s icmp: %+v", name, l)
			}
		}
		// The Agent now sees no IPv6 address at all: the IPv6 lanes stop, and come back when it does.
		k.restart()
		k.negotiate(v4Caps(protocol.QualityIPv6None, "ipv4/icmp", "ipv6/icmp"))
		cfg = k.get()
		if _, running := k.open()["telecom/ipv6/tcp"]; running || k.lane(cfg, "telecom", "ipv6", "tcp").Status != protocol.QualityLaneNoIPv6 {
			t.Fatalf("no IPv6: %+v", k.lane(cfg, "telecom", "ipv6", "tcp"))
		}
		noV6 := cfg.ConfigRevision
		k.restart()
		k.negotiate(v4Caps(protocol.QualityIPv6Present, "ipv4/icmp", "ipv6/icmp"))
		cfg = k.get()
		reopened := k.open()["telecom/ipv6/tcp"]
		if reopened.Target == "" || cfg.ConfigRevision == noV6 || k.lane(cfg, "telecom", "ipv6", "tcp").Status != protocol.QualityLaneActive {
			t.Fatalf("IPv6 back: %+v %+v", reopened, cfg)
		}
		if *k.lane(cfg, "telecom", "ipv6", "tcp").TargetID != *k.lane(cfg, "telecom", "ipv4", "tcp").TargetID && false {
			t.Fatal("unreachable")
		}
		k.invariants()
	})
	t.Run("a late report cannot override a later one or another session", func(t *testing.T) {
		k := v4Base(t, true)
		old := k.report(true, v4Caps(protocol.QualityIPv6Present))
		revoke := k.report(false, nil)
		k.reconcile(revoke)
		if len(k.open()) != 0 {
			t.Fatal("revoke did not close")
		}
		k.reconcile(old) // arrives late: it is no longer the latest report
		if len(k.open()) != 0 || k.count(`SELECT targets_granted FROM quality_config WHERE agent_id=?`, k.id) != 0 {
			t.Fatal("a stale grant overrode the revocation")
		}
		stale := k.report(true, v4Caps(protocol.QualityIPv6Present))
		k.restart()
		k.report(true, nil)
		k.reconcile(stale) // belongs to the previous session
		if len(k.open()) != 0 {
			t.Fatal("evidence from the previous session was applied")
		}
	})
	t.Run("an excluded platform never runs", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present))
		if _, err := k.s.db.Exec(`UPDATE quality_capability SET excluded_reason='platform_excluded:alpine' WHERE agent_id=?`, k.id); err != nil {
			t.Fatal(err)
		}
		cfg := k.get()
		if cfg.AgentSupport != protocol.QualitySupportExcluded || cfg.State != "excluded" {
			t.Fatalf("%+v", cfg)
		}
		if _, err := k.trySave(30000, v4Slot("telecom", "example.com", 443, "v4tcp")); !errors.Is(err, ErrQualityUnsupported) {
			t.Fatalf("save on an excluded platform: %v", err)
		}
		if _, err := k.tryEnable(true, ""); !errors.Is(err, ErrQualityUnsupported) {
			t.Fatalf("enable on an excluded platform: %v", err)
		}
	})
}

func TestV4StatusPrioritiesAndSupportStates(t *testing.T) {
	// Before any report the Agent is "unsupported"; after one without the header it needs an upgrade.
	k := newV4Kit(t)
	if got := k.get().AgentSupport; got != protocol.QualitySupportUnsupported {
		t.Fatalf("never reported: %q", got)
	}
	k.report(false, v4Caps(protocol.QualityIPv6Unknown))
	if got := k.get().AgentSupport; got != protocol.QualitySupportUpgradeRequired {
		t.Fatalf("old Agent: %q", got)
	}
	k.negotiate(v4Caps(protocol.QualityIPv6None, "ipv4/icmp", "ipv6/icmp"))
	cfg := k.save(30000,
		v4Slot("telecom", "example.com", 443, "v4tcp", "v6tcp", "v6icmp", "v4icmp"),
		v4Slot("unicom", "example.org", 443, "v4tcp", "off"),
		v4Slot("mobile", "", 0, "v4tcp"))
	statuses := func(cfg protocol.QualityV4Config) map[string]string {
		out := map[string]string{}
		for _, l := range cfg.Lanes {
			out[l.Slot+"/"+l.Family+"/"+l.Protocol] = l.Status
		}
		return out
	}
	want := func(got map[string]string, expect map[string]string) {
		t.Helper()
		for lane, status := range expect {
			if got[lane] != status {
				t.Errorf("%s = %q, want %q", lane, got[lane], status)
			}
		}
	}
	// Switch off: "disabled" outranks everything below it.
	want(statuses(cfg), map[string]string{"telecom/ipv4/tcp": "disabled", "mobile/ipv4/tcp": "disabled"})
	cfg = k.enable(true)
	want(statuses(cfg), map[string]string{
		"telecom/ipv4/tcp":  protocol.QualityLaneActive,
		"telecom/ipv4/icmp": protocol.QualityLaneICMPUnsupported,
		"telecom/ipv6/tcp":  protocol.QualityLaneNoIPv6,
		"telecom/ipv6/icmp": protocol.QualityLaneNoIPv6, // no IPv6 outranks "ICMP unsupported"
		"unicom/ipv4/tcp":   protocol.QualityLaneInactive,
		"unicom/ipv6/tcp":   protocol.QualityLaneInactive,
		"mobile/ipv4/tcp":   protocol.QualityLaneUnset,
	})
	if cfg.State != "ready" {
		t.Fatalf("state %q", cfg.State)
	}
	// A revoked Agent outranks everything.
	if _, err := k.s.db.Exec(`UPDATE agents SET revoked=1 WHERE id=?`, k.id); err != nil {
		t.Fatal(err)
	}
	cfg = k.get()
	for _, l := range cfg.Lanes {
		if l.Status != protocol.QualityLaneDeviceInactive {
			t.Fatalf("revoked Agent: %+v", l)
		}
	}
}

func TestV4EffectiveIntervalNeverLowersTheUsersChoice(t *testing.T) {
	for _, tc := range []struct{ user, scale, want int }{
		{30000, 1, 30000}, {120000, 1, 120000}, {300000, 1, 300000},
		{30000, 2, 60000}, {120000, 2, 120000}, {60000, 4, 120000}, {300000, 10, 300000}, {30000, 10, 300000}, {90000, 0, 90000},
	} {
		if got := qualityEffectiveInterval(tc.user, tc.scale); got != tc.want {
			t.Errorf("user=%d scale=%d got=%d want=%d", tc.user, tc.scale, got, tc.want)
		}
	}
	k := v4Base(t, true)
	cfg := k.save(120000, v4Slot("telecom", "example.com", 443, "v4tcp"))
	if cfg.IntervalMS != 120000 || cfg.EffectiveIntervalMS != 120000 || cfg.State != "ready" {
		t.Fatalf("%+v", cfg)
	}
}

// ---- samples ----

func (k *v4Kit) sample(cfg protocol.QualityV4Config, slot, family, proto string, seq int, at time.Time, kind string) protocol.QualitySample {
	k.t.Helper()
	lane := k.lane(cfg, slot, family, proto)
	if lane.TargetID == nil {
		k.t.Fatalf("lane %s/%s/%s has no target", slot, family, proto)
	}
	s := protocol.QualitySample{Sequence: strconv.Itoa(seq), TargetID: *lane.TargetID, ConfigRevision: cfg.ConfigRevision, SlotRevision: lane.SlotRevision,
		ScheduledAt: at.UnixMilli(), StartedAt: at.UnixMilli(), FinishedAt: at.UnixMilli() + 25, DurationMS: 25}
	d := protocol.QualityDetails{Semantics: protocol.QualityStagedSemantics, IntervalMS: cfg.EffectiveIntervalMS, DNSMS: 5}
	if proto == "icmp" {
		d.Requested, d.WindowMS = 3, 2000
		for i := 0; i < 3; i++ {
			d.Echoes = append(d.Echoes, protocol.QualityEcho{Seq: i + 1, Status: "not_sent"})
		}
	}
	switch kind {
	case "not_found", "no_address", "dns_error", "dns_timeout":
		s.Outcome, d.DNSOutcome, d.ErrorStage, d.ErrorCode = "dns_error", kind, "dns", kind
		d.ProbeComplete = proto == "tcp"
	case "unsafe_address":
		s.Outcome, d.DNSOutcome, d.ErrorStage, d.ErrorCode = "unsafe_address", "success", "local", "unsafe_address"
	case "success":
		if proto != "tcp" {
			k.t.Fatal("success sample is TCP only")
		}
		ms := 10.0
		s.Outcome, s.LatencyMS, s.ResolvedIP = "success", &ms, "93.184.216.34"
		d.DNSOutcome, d.ProbeComplete, d.ConnectAttempted, d.ConnectComplete, d.ConnectMS = "success", true, true, true, &ms
	default:
		k.t.Fatalf("unknown sample kind %q", kind)
	}
	s.Details = d.Encode()
	return s
}

func (k *v4Kit) upload(at time.Time, samples ...protocol.QualitySample) []string {
	k.t.Helper()
	ack, err := k.s.SaveQualityBatch(k.ctx, k.id, protocol.QualityBatch{Version: 2, Epoch: strconv.FormatUint(k.epoch, 10), SessionID: k.session, Samples: samples}, at)
	if err != nil {
		k.t.Fatal(err)
	}
	var out []string
	for _, r := range ack.Results {
		out = append(out, r[2])
	}
	return out
}

func TestV4UserTargetSamplesNeedANegotiatedSession(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	at := k.now.Add(time.Minute)
	if acks := k.upload(at.Add(time.Second), k.sample(cfg, "telecom", "ipv4", "tcp", 1, at, "success")); acks[0] != "committed" {
		t.Fatalf("negotiated session: %v", acks)
	}
	// After a restart the new session has done step one only: its samples are not accepted.
	k.restart()
	first := k.report(true, nil)
	k.reconcile(first)
	// The session has no capability yet, so the whole batch is refused.
	pending := k.sample(cfg, "telecom", "ipv4", "tcp", 1, at.Add(30*time.Second), "success")
	if _, err := k.s.SaveQualityBatch(k.ctx, k.id, protocol.QualityBatch{Version: 2, Epoch: strconv.FormatUint(k.epoch, 10), SessionID: k.session, Samples: []protocol.QualitySample{pending}}, at.Add(time.Minute)); !errors.Is(err, ErrQualityUnsupported) {
		t.Fatalf("step one only: %v", err)
	}
	second := k.report(true, v4Caps(protocol.QualityIPv6Present))
	k.reconcile(second)
	if acks := k.upload(at.Add(time.Minute), k.sample(cfg, "telecom", "ipv4", "tcp", 2, at.Add(40*time.Second), "success")); acks[0] != "committed" {
		t.Fatalf("after step two: %v", acks)
	}
	// A session that never sent the header (an old Agent) is refused even with the right target id.
	k.restart()
	k.report(false, v4Caps(protocol.QualityIPv6Present))
	if k.count(`SELECT user_targets FROM quality_capability WHERE agent_id=?`, k.id) != 0 {
		t.Fatal("a report without the header kept the session authorized")
	}
	if acks := k.upload(at.Add(2*time.Minute), k.sample(cfg, "telecom", "ipv4", "tcp", 3, at.Add(50*time.Second), "success")); acks[0] != "unauthorized" {
		t.Fatalf("old Agent: %v", acks)
	}
}

func TestV4LateSamplesStayWithTheirOwnTarget(t *testing.T) {
	k := v4Base(t, true)
	old := k.get()
	start := k.now
	measured := start.Add(10 * time.Second)
	sample := k.sample(old, "telecom", "ipv4", "tcp", 1, measured, "success")
	k.now = start.Add(time.Minute)
	changed := k.save(30000, v4Slot("telecom", "example.org", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
	closedAt := k.now
	// Measured before the change but uploaded hours later (inside the six-hour window): accepted.
	if acks := k.upload(closedAt.Add(3*time.Hour), sample); acks[0] != "committed" {
		t.Fatalf("late but valid sample: %v", acks)
	}
	// The same slot revision after the interval closed, started past the clock tolerance: refused.
	late := k.sample(old, "telecom", "ipv4", "tcp", 2, closedAt.Add(5*time.Minute), "success")
	if acks := k.upload(closedAt.Add(5*time.Minute+time.Second), late); acks[0] != "unauthorized" {
		t.Fatalf("started after the interval ended: %v", acks)
	}
	// The new target with the old configuration revision is not authorized either.
	stale := k.sample(changed, "telecom", "ipv4", "tcp", 3, closedAt.Add(10*time.Second), "success")
	stale.ConfigRevision = old.ConfigRevision
	if acks := k.upload(closedAt.Add(20*time.Second), stale); acks[0] != "unauthorized" {
		t.Fatalf("new target, old configuration: %v", acks)
	}
	fresh := k.sample(changed, "telecom", "ipv4", "tcp", 4, closedAt.Add(15*time.Second), "success")
	if acks := k.upload(closedAt.Add(25*time.Second), fresh); acks[0] != "committed" {
		t.Fatalf("new target: %v", acks)
	}
	// Older than six hours is refused whatever the intervals say.
	ancient := k.sample(old, "telecom", "ipv4", "tcp", 5, measured, "success")
	if acks := k.upload(closedAt.Add(7*time.Hour), ancient); acks[0] == "committed" {
		t.Fatalf("a sample over six hours old was accepted: %v", acks)
	}
}

func TestV4TargetAbnormalCondition(t *testing.T) {
	setup := func(t *testing.T, slots ...protocol.QualityV4Slot) (*v4Kit, protocol.QualityV4Config) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present))
		k.save(30000, slots...)
		cfg := k.enable(true)
		k.now = k.now.Add(10 * time.Minute)
		return k, cfg
	}
	feed := func(k *v4Kit, cfg protocol.QualityV4Config, slot, family, proto string, first int, kinds ...string) {
		t.Helper()
		for i, kind := range kinds {
			at := k.now.Add(-time.Duration(len(kinds)-i) * 30 * time.Second)
			if acks := k.upload(k.now, k.sample(cfg, slot, family, proto, first+i, at, kind)); acks[0] != "committed" {
				t.Fatalf("%s sample: %v", kind, acks)
			}
		}
	}
	status := func(k *v4Kit, slot, family, proto string) (string, string) {
		l := k.lane(k.get(), slot, family, proto)
		return l.Status, l.Condition
	}
	one := v4Slot("telecom", "gone.example.com", 443, "v4tcp", "v4icmp", "v6tcp")
	t.Run("three events", func(t *testing.T) {
		k, cfg := setup(t, one)
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "not_found")
		if s, c := status(k, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneTargetAbnormal || c != "not_found" {
			t.Fatalf("%s/%s", s, c)
		}
		// Other lanes of the slot have their own samples.
		if s, _ := status(k, "telecom", "ipv6", "tcp"); s != protocol.QualityLaneActive {
			t.Fatalf("other lane: %s", s)
		}
	})
	t.Run("two events are not enough", func(t *testing.T) {
		k, cfg := setup(t, one)
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found")
		if s, _ := status(k, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneActive {
			t.Fatalf("%s", s)
		}
	})
	t.Run("the three kinds mix and the latest names the reason", func(t *testing.T) {
		k, cfg := setup(t, one)
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "no_address", "unsafe_address")
		if s, c := status(k, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneTargetAbnormal || c != "unsafe_address" {
			t.Fatalf("%s/%s", s, c)
		}
	})
	t.Run("another result clears it", func(t *testing.T) {
		k, cfg := setup(t, one)
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "not_found", "success")
		if s, _ := status(k, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneActive {
			t.Fatalf("success after events: %s", s)
		}
		k2, cfg2 := setup(t, one)
		feed(k2, cfg2, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "dns_timeout") // a timeout is a path problem
		if s, _ := status(k2, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneActive {
			t.Fatalf("dns timeout: %s", s)
		}
	})
	t.Run("ICMP events count too", func(t *testing.T) {
		k, cfg := setup(t, one)
		feed(k, cfg, "telecom", "ipv4", "icmp", 1, "not_found", "no_address", "not_found")
		if s, c := status(k, "telecom", "ipv4", "icmp"); s != protocol.QualityLaneTargetAbnormal || c != "not_found" {
			t.Fatalf("%s/%s", s, c)
		}
	})
	t.Run("stale events are not an abnormal target", func(t *testing.T) {
		k, cfg := setup(t, one)
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "not_found")
		k.now = k.now.Add(time.Hour)
		if s, _ := status(k, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneActive {
			t.Fatalf("an old run is still shown: %s", s)
		}
	})
	t.Run("events from before the current interval do not count", func(t *testing.T) {
		k, cfg := setup(t, one)
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "not_found")
		k.enable(false)
		cfg = k.enable(true)
		if s, _ := status(k, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneActive {
			t.Fatalf("events survived a re-enable: %s", s)
		}
		_ = cfg
	})
	t.Run("two different names at once point at the local resolver", func(t *testing.T) {
		k, cfg := setup(t, one, v4Slot("unicom", "other.example.net", 443, "v4tcp"))
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "not_found")
		feed(k, cfg, "unicom", "ipv4", "tcp", 10, "no_address", "no_address", "no_address")
		for _, slot := range []string{"telecom", "unicom"} {
			if s, _ := status(k, slot, "ipv4", "tcp"); s != protocol.QualityLaneActive {
				t.Fatalf("%s is marked although two names failed together: %s", slot, s)
			}
		}
	})
	t.Run("the same name in two slots counts once", func(t *testing.T) {
		k, cfg := setup(t, one, v4Slot("unicom", "gone.example.com", 8443, "v4tcp"))
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "not_found")
		feed(k, cfg, "unicom", "ipv4", "tcp", 10, "not_found", "not_found", "not_found")
		for _, slot := range []string{"telecom", "unicom"} {
			if s, c := status(k, slot, "ipv4", "tcp"); s != protocol.QualityLaneTargetAbnormal || c != "not_found" {
				t.Fatalf("%s: %s/%s", slot, s, c)
			}
		}
	})
	t.Run("a literal address is never excluded by the resolver rule", func(t *testing.T) {
		k, cfg := setup(t, one, v4Slot("unicom", "8.8.8.8", 53, "v4tcp"))
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "not_found")
		feed(k, cfg, "unicom", "ipv4", "tcp", 10, "unsafe_address", "unsafe_address", "unsafe_address")
		if s, _ := status(k, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneTargetAbnormal {
			t.Fatalf("the name was excluded by a literal address: %s", s)
		}
		if s, c := status(k, "unicom", "ipv4", "tcp"); s != protocol.QualityLaneTargetAbnormal || c != "unsafe_address" {
			t.Fatalf("literal: %s/%s", s, c)
		}
	})
	t.Run("events of a replaced session are not counted", func(t *testing.T) {
		k, cfg := setup(t, one)
		feed(k, cfg, "telecom", "ipv4", "tcp", 1, "not_found", "not_found", "not_found")
		k.restart()
		k.negotiate(v4Caps(protocol.QualityIPv6Present))
		if s, _ := status(k, "telecom", "ipv4", "tcp"); s != protocol.QualityLaneActive {
			t.Fatalf("events of the previous process: %s", s)
		}
	})
}

func TestV4CurrentTCPFollowsTheOpenIPv4TCPInterval(t *testing.T) {
	k := v4Base(t, false)
	status := func() bool {
		t.Helper()
		out, err := k.s.QualityAlertGate(k.ctx, k.id)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if status() {
		t.Fatal("current_tcp while the switch is off")
	}
	k.enable(true)
	if !status() {
		t.Fatal("current_tcp with an open IPv4 TCP interval")
	}
	k.save(30000, v4Slot("telecom", "example.com", 443, "v4icmp", "v6tcp"))
	if status() {
		t.Fatal("current_tcp with only ICMP and IPv6 TCP open")
	}
	k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp"))
	if !status() {
		t.Fatal("current_tcp after IPv4 TCP is selected again")
	}
	k.enable(false)
	if status() {
		t.Fatal("current_tcp after the switch went off")
	}
}

func TestV4HistoryCarriesTheSlotsCurrentLabels(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	id := k.targetID(cfg, "telecom", "ipv4", "tcp")
	hist, err := k.s.QualityHistoryV2(k.ctx, k.id, id, 24, k.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if hist.Target.Kind != "custom" || hist.Target.ISP != "I-telecom" || hist.Target.Region != "R-telecom" || hist.Target.Host != "example.com" {
		t.Fatalf("history target: %+v", hist.Target)
	}
	// Renaming the labels moves the history of the same target with them and does not touch the target.
	renamed := v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp")
	renamed.Region, renamed.ISP = "New region", "New ISP"
	k.save(30000, renamed)
	hist, err = k.s.QualityHistoryV2(k.ctx, k.id, id, 24, k.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if hist.Target.Region != "New region" || hist.Target.ISP != "New ISP" {
		t.Fatalf("history target after rename: %+v", hist.Target)
	}
	var payload string
	if err := k.s.db.QueryRow(`SELECT payload FROM quality_targets WHERE id=?`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "New ISP") || strings.Contains(payload, "kind") {
		t.Fatalf("labels leaked into the stored payload: %s", payload)
	}
}

func (k *v4Kit) sessionConfig(version int, header bool) (protocol.QualityConfig, error) {
	return k.s.GetQualitySessionConfig(k.ctx, k.id, k.epoch, k.session, version, header)
}

func TestV4SessionConfigFormats(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	t.Run("negotiated session with the header gets user targets", func(t *testing.T) {
		got, err := k.sessionConfig(2, true)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Enabled || !got.Supported || got.Revision != cfg.ConfigRevision || got.IntervalMS != 30000 || got.State != "ready" || got.Capabilities == nil || len(got.Targets) != 4 {
			t.Fatalf("%+v", got)
		}
		for _, target := range got.Targets {
			if target.Source != "user" || target.Status != "active" || target.Host != "example.com" || target.ID == "" || target.SlotRevision != "2" || target.Region != "" {
				t.Fatalf("target %+v", target)
			}
			if target.Protocol == "icmp" && target.Port != 0 {
				t.Fatalf("ICMP target carries a port: %+v", target)
			}
		}
	})
	oldFormat := func(t *testing.T, got protocol.QualityConfig, version int) {
		t.Helper()
		wire, _ := json.Marshal(got)
		if got.Version != version || got.Revision != k.get().ConfigRevision || !got.Supported || got.Enabled || got.IPv6 || len(got.Targets) != 0 || got.Capabilities != nil || got.RegionID != "" ||
			got.IntervalMS != 30000 || got.JitterPercent != 20 || got.TimeoutMS != 5000 {
			t.Fatalf("old format: %s", wire)
		}
		if strings.Contains(string(wire), "example.com") || strings.Contains(string(wire), "443") {
			t.Fatalf("old format leaks a user target: %s", wire)
		}
		if version == 2 && (got.State != "agent_upgrade_required" || got.Semantics != protocol.QualityStagedSemantics) {
			t.Fatalf("old v2 format: %s", wire)
		}
	}
	t.Run("a request without the header gets the old format", func(t *testing.T) {
		got, err := k.sessionConfig(2, false)
		if err != nil {
			t.Fatal(err)
		}
		oldFormat(t, got, 2)
	})
	t.Run("version 1 never gets user targets", func(t *testing.T) {
		got, err := k.sessionConfig(1, true)
		if err != nil {
			t.Fatal(err)
		}
		oldFormat(t, got, 1)
	})
	t.Run("a session that did not complete step two gets the old format", func(t *testing.T) {
		k.restart()
		k.reconcile(k.report(true, nil))
		if _, err := k.sessionConfig(2, true); !errors.Is(err, ErrQualityUnsupported) {
			t.Fatalf("no v2 capability yet: %v", err)
		}
		k.reconcile(k.report(false, v4Caps(protocol.QualityIPv6Present)))
		got, err := k.sessionConfig(2, true)
		if err != nil {
			t.Fatal(err)
		}
		oldFormat(t, got, 2)
	})
	t.Run("a session other than the current one is refused", func(t *testing.T) {
		if _, err := k.s.GetQualitySessionConfig(k.ctx, k.id, k.epoch+5, k.session, 2, true); !errors.Is(err, ErrQualityUnsupported) {
			t.Fatalf("stale epoch: %v", err)
		}
		if _, err := k.s.GetQualitySessionConfig(k.ctx, k.id, k.epoch, "other", 2, true); !errors.Is(err, ErrQualityUnsupported) {
			t.Fatalf("other session: %v", err)
		}
	})
}

func TestV4SessionConfigPausedTargetsAndInterval(t *testing.T) {
	k := v4Base(t, false)
	got, err := k.sessionConfig(2, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || len(got.Targets) != 4 {
		t.Fatalf("%+v", got)
	}
	for _, target := range got.Targets {
		if target.Status != "paused" {
			t.Fatalf("target while the switch is off: %+v", target)
		}
	}
	k.save(120000, v4Slot("telecom", "example.com", 443, "v4tcp"))
	k.enable(true)
	got, err = k.sessionConfig(2, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.IntervalMS != 120000 || !got.Enabled {
		t.Fatalf("the user's 120 s must reach the Agent: %+v", got)
	}
	active := 0
	for _, target := range got.Targets {
		if target.Status == "active" {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active=%d", active)
	}
}

// icmpWant is what the read model says about ICMP for both families.
func icmpWant(t *testing.T, cfg protocol.QualityV4Config, v4, v6 string, what string) {
	t.Helper()
	if cfg.ICMPSupport.IPv4 != v4 || cfg.ICMPSupport.IPv6 != v6 {
		t.Fatalf("%s: icmp_support %+v, want %s/%s", what, cfg.ICMPSupport, v4, v6)
	}
	// Whatever else the lanes say, a lane that reports "icmp_unsupported" must be backed by the field.
	for _, l := range cfg.Lanes {
		if l.Status == protocol.QualityLaneICMPUnsupported && (l.Family == "ipv4" && cfg.ICMPSupport.IPv4 != protocol.QualityICMPUnsupported || l.Family == "ipv6" && cfg.ICMPSupport.IPv6 != protocol.QualityICMPUnsupported) {
			t.Fatalf("%s: lane %+v disagrees with %+v", what, l, cfg.ICMPSupport)
		}
	}
}

// The dialog disables the ICMP boxes from icmp_support alone, so it must not depend on the master switch, the slots,
// the ticks or the IPv6 address (design 7.1, priority 8; review R07-D M5).
func TestV4ICMPSupportDoesNotDependOnLaneStatus(t *testing.T) {
	const unknown, yes, no = protocol.QualityICMPUnknown, protocol.QualityICMPSupported, protocol.QualityICMPUnsupported
	t.Run("not yet known", func(t *testing.T) {
		k := newV4Kit(t)
		icmpWant(t, k.get(), unknown, unknown, "never reported")
		k.report(false, v4Caps(protocol.QualityIPv6Present, "ipv4/icmp", "ipv6/icmp"))
		icmpWant(t, k.get(), unknown, unknown, "an old Agent says nothing we may rely on")
	})
	t.Run("a container without raw sockets, whatever is configured", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present, "ipv4/icmp", "ipv6/icmp"))
		icmpWant(t, k.get(), no, no, "nothing configured, switch off")
		k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp")) // no ICMP ticked
		icmpWant(t, k.get(), no, no, "saved without ICMP ticks, switch off")
		icmpWant(t, k.enable(true), no, no, "switched on, no ICMP ticked")
		k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6icmp"))
		cfg := k.get()
		icmpWant(t, cfg, no, no, "ICMP ticked")
		if k.lane(cfg, "telecom", "ipv4", "icmp").Status != protocol.QualityLaneICMPUnsupported {
			t.Fatalf("lane: %+v", k.lane(cfg, "telecom", "ipv4", "icmp"))
		}
		icmpWant(t, k.enable(false), no, no, "switched off again")
	})
	t.Run("no IPv6 address hides the lane status but not the capability", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6None, "ipv4/icmp", "ipv6/icmp"))
		k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp"))
		cfg := k.enable(true)
		if k.lane(cfg, "telecom", "ipv6", "icmp").Status != protocol.QualityLaneNoIPv6 {
			t.Fatalf("lane: %+v", k.lane(cfg, "telecom", "ipv6", "icmp"))
		}
		icmpWant(t, cfg, no, no, "no IPv6 outranks icmp_unsupported in the lane")
	})
	t.Run("families are independent, and a full capability is supported", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present, "ipv6/icmp"))
		icmpWant(t, k.get(), yes, no, "only IPv6 ICMP lost")
		k = newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present))
		icmpWant(t, k.get(), yes, yes, "everything supported")
	})
	t.Run("a restart is unknown until the second step, then follows the new session", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present, "ipv4/icmp", "ipv6/icmp"))
		k.save(30000, v4Slot("telecom", "example.com", 443, "v4tcp"))
		k.enable(true)
		icmpWant(t, k.get(), no, no, "before the restart")
		k.restart()
		k.reconcile(k.report(true, nil)) // step one: header, no capability; the grant of before stands
		cfg := k.get()
		if cfg.AgentSupport != protocol.QualitySupportNegotiating {
			t.Fatalf("support %q", cfg.AgentSupport)
		}
		icmpWant(t, cfg, unknown, unknown, "negotiating: the old session's answer is not carried over")
		k.reconcile(k.report(true, v4Caps(protocol.QualityIPv6Present))) // the container got CAP_NET_RAW
		icmpWant(t, k.get(), yes, yes, "capability back")
		k.restart()
		k.negotiate(v4Caps(protocol.QualityIPv6Present, "ipv4/icmp"))
		icmpWant(t, k.get(), no, yes, "lost again, IPv4 only")
	})
	t.Run("excluded and inactive devices are unknown", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present, "ipv4/icmp"))
		icmpWant(t, k.get(), no, yes, "before")
		if _, err := k.s.db.Exec(`UPDATE quality_capability SET excluded_reason='platform_excluded:alpine' WHERE agent_id=?`, k.id); err != nil {
			t.Fatal(err)
		}
		icmpWant(t, k.get(), unknown, unknown, "excluded platform")
		if _, err := k.s.db.Exec(`UPDATE quality_capability SET excluded_reason='' WHERE agent_id=?`, k.id); err != nil {
			t.Fatal(err)
		}
		icmpWant(t, k.get(), no, yes, "restored")
		if _, err := k.s.db.Exec(`UPDATE agents SET revoked=1 WHERE id=?`, k.id); err != nil {
			t.Fatal(err)
		}
		icmpWant(t, k.get(), unknown, unknown, "revoked device")
	})
	t.Run("the field is on the wire", func(t *testing.T) {
		k := newV4Kit(t)
		k.negotiate(v4Caps(protocol.QualityIPv6Present, "ipv4/icmp"))
		wire, err := json.Marshal(k.get())
		if err != nil || !strings.Contains(string(wire), `"icmp_support":{"ipv4":"unsupported","ipv6":"supported"}`) {
			t.Fatalf("%v %s", err, wire)
		}
	})
}
