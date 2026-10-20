package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

// v4Kit drives the R07 model the way the Server does: reports negotiate the session, saves and
// switch requests change the settings, and every step can be inspected in the tables.
type v4Kit struct {
	t       *testing.T
	s       *Store
	id      string
	now     time.Time
	seq     uint64
	epoch   uint64
	session string
	ctx     context.Context
}

func newV4Kit(t *testing.T) *v4Kit {
	t.Helper()
	s, id, _ := testStore(t, ":memory:")
	t.Cleanup(func() { s.Close() })
	return &v4Kit{t: t, s: s, id: id, now: time.Unix(1800000000, 0), epoch: 1, session: "v4-session", ctx: context.Background()}
}

func v4Caps(ipv6 string, unsupported ...string) *protocol.QualityCapabilities {
	caps := &protocol.QualityCapabilities{Version: 2, Semantics: protocol.QualityStagedSemantics, Targets: protocol.QualityTargetsUserV1, IPv6Address: ipv6}
	skip := map[string]bool{}
	for _, lane := range unsupported {
		skip[lane] = true
	}
	for _, family := range []string{"ipv4", "ipv6"} {
		for _, proto := range []string{"tcp", "icmp"} {
			lane := protocol.QualityLaneCapability{Family: family, Protocol: proto, Supported: !skip[family+"/"+proto]}
			if !lane.Supported {
				lane.Reason = "unsupported"
			}
			caps.Lanes = append(caps.Lanes, lane)
		}
	}
	return caps
}

func (k *v4Kit) tick() time.Time {
	k.now = k.now.Add(time.Second)
	return k.now
}

// report sends one resource report with the given quality evidence and returns it.
func (k *v4Kit) report(header bool, caps *protocol.QualityCapabilities) protocol.Report {
	k.t.Helper()
	k.seq++
	r := validReport(k.id, k.epoch, k.session, "boot", k.seq, k.seq*10, k.seq*20)
	r.NetworkQuality = true
	r.NetworkQualityV2 = caps
	r.QualityTargetsHeader = header
	if _, accepted, reason, err := k.s.ProcessReport(k.ctx, k.id, r, k.tick()); err != nil || !accepted {
		k.t.Fatalf("report accepted=%t reason=%q err=%v", accepted, reason, err)
	}
	return r
}

// negotiate completes both steps for the current session and runs the authorization recalculation.
func (k *v4Kit) negotiate(caps *protocol.QualityCapabilities) {
	k.t.Helper()
	first := k.report(true, nil)
	if err := k.s.ReconcileQualityAuthorization(k.ctx, k.id, QualityNegotiationFromReport(first), k.now); err != nil {
		k.t.Fatal(err)
	}
	second := k.report(true, caps)
	if err := k.s.ReconcileQualityAuthorization(k.ctx, k.id, QualityNegotiationFromReport(second), k.now); err != nil {
		k.t.Fatal(err)
	}
}

// reconcile applies the evidence of one report.
func (k *v4Kit) reconcile(r protocol.Report) {
	k.t.Helper()
	if err := k.s.ReconcileQualityAuthorization(k.ctx, k.id, QualityNegotiationFromReport(r), k.now); err != nil {
		k.t.Fatal(err)
	}
}

func v4Slot(slot, host string, port int, sub ...string) protocol.QualityV4Slot {
	s := protocol.QualityV4Slot{Slot: slot, Kind: "custom", Region: "R-" + slot, ISP: "I-" + slot, Host: host, Port: port}
	if host != "" {
		s.SlotEnabled = true
	}
	for _, name := range sub {
		switch name {
		case "v4tcp":
			s.IPv4TCP = true
		case "v4icmp":
			s.IPv4ICMP = true
		case "v6tcp":
			s.IPv6TCP = true
		case "v6icmp":
			s.IPv6ICMP = true
		case "off":
			s.SlotEnabled = false
		}
	}
	return s
}

// v4Slots builds a full request; missing slots stay empty.
func v4Slots(slots ...protocol.QualityV4Slot) []protocol.QualityV4Slot {
	byName := map[string]protocol.QualityV4Slot{}
	for _, s := range slots {
		byName[s.Slot] = s
	}
	var out []protocol.QualityV4Slot
	for _, name := range protocol.QualitySlotOrder() {
		if s, ok := byName[name]; ok {
			out = append(out, s)
		} else {
			out = append(out, protocol.QualityV4Slot{Slot: name, Kind: "builtin", ISP: name})
		}
	}
	return out
}

func (k *v4Kit) get() protocol.QualityV4Config {
	k.t.Helper()
	cfg, err := k.s.GetQualityConfigV4(k.ctx, k.id, k.now)
	if err != nil {
		k.t.Fatal(err)
	}
	return cfg
}

func (k *v4Kit) save(interval int, slots ...protocol.QualityV4Slot) protocol.QualityV4Config {
	k.t.Helper()
	cfg, err := k.trySave(interval, slots...)
	if err != nil {
		k.t.Fatal(err)
	}
	return cfg
}

func (k *v4Kit) trySave(interval int, slots ...protocol.QualityV4Slot) (protocol.QualityV4Config, error) {
	current := k.get()
	return k.s.SaveQualityConfigV4(k.ctx, k.id, protocol.QualityV4SaveRequest{ExpectedRevision: current.SettingsRevision, IntervalMS: interval, Slots: v4Slots(slots...)}, k.tick())
}

func (k *v4Kit) enable(on bool) protocol.QualityV4Config {
	k.t.Helper()
	cfg, err := k.tryEnable(on, "")
	if err != nil {
		k.t.Fatal(err)
	}
	return cfg
}

func (k *v4Kit) tryEnable(on bool, expected string) (protocol.QualityV4Config, error) {
	if expected == "" {
		expected = k.get().SettingsRevision
	}
	return k.s.SetQualityEnabledV4(k.ctx, k.id, protocol.QualityV4EnabledRequest{ExpectedRevision: expected, Enabled: &on}, k.tick())
}

type v4Interval struct {
	Slot, Family, Proto, Target string
	From                        int64
	Until                       *int64
	Open                        bool
}

func (k *v4Kit) intervals() []v4Interval {
	k.t.Helper()
	rows, err := k.s.db.Query(`SELECT slot,family,protocol,target_id,config_from,config_until,end_at IS NULL FROM quality_intervals WHERE agent_id=? ORDER BY slot,family,protocol,config_from`, k.id)
	if err != nil {
		k.t.Fatal(err)
	}
	defer rows.Close()
	var out []v4Interval
	for rows.Next() {
		var iv v4Interval
		var until *int64
		if err := rows.Scan(&iv.Slot, &iv.Family, &iv.Proto, &iv.Target, &iv.From, &until, &iv.Open); err != nil {
			k.t.Fatal(err)
		}
		iv.Until = until
		out = append(out, iv)
	}
	return out
}

func (k *v4Kit) open() map[string]v4Interval {
	out := map[string]v4Interval{}
	for _, iv := range k.intervals() {
		if iv.Open {
			out[iv.Slot+"/"+iv.Family+"/"+iv.Proto] = iv
		}
	}
	return out
}

type v4Target struct {
	ID, Identity, Host string
	Created            int64
	Retired            bool
	FirstActive        bool
}

func (k *v4Kit) targets() map[string]v4Target {
	k.t.Helper()
	rows, err := k.s.db.Query(`SELECT id,COALESCE(identity,''),json_extract(payload,'$.host'),COALESCE(created_revision,-1),retired_at IS NOT NULL,first_active_at IS NOT NULL,json_extract(payload,'$.slot')||'/'||json_extract(payload,'$.family')||'/'||json_extract(payload,'$.protocol') FROM quality_targets WHERE agent_id=?`, k.id)
	if err != nil {
		k.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]v4Target{}
	for rows.Next() {
		var t v4Target
		var lane string
		if err := rows.Scan(&t.ID, &t.Identity, &t.Host, &t.Created, &t.Retired, &t.FirstActive, &lane); err != nil {
			k.t.Fatal(err)
		}
		out[t.ID] = t
		_ = lane
	}
	return out
}

func (k *v4Kit) lane(cfg protocol.QualityV4Config, slot, family, proto string) protocol.QualityV4Lane {
	k.t.Helper()
	for _, l := range cfg.Lanes {
		if l.Slot == slot && l.Family == family && l.Protocol == proto {
			return l
		}
	}
	k.t.Fatalf("no lane %s/%s/%s", slot, family, proto)
	return protocol.QualityV4Lane{}
}

func (k *v4Kit) targetID(cfg protocol.QualityV4Config, slot, family, proto string) string {
	l := k.lane(cfg, slot, family, proto)
	if l.TargetID == nil {
		return ""
	}
	return *l.TargetID
}

func (k *v4Kit) revisions() (settings, measurement int64) {
	k.t.Helper()
	if err := k.s.db.QueryRow(`SELECT settings_revision,revision FROM quality_config WHERE agent_id=?`, k.id).Scan(&settings, &measurement); err != nil {
		k.t.Fatal(err)
	}
	return
}

func (k *v4Kit) count(query string, args ...any) int {
	k.t.Helper()
	var n int
	if err := k.s.db.QueryRow(query, args...).Scan(&n); err != nil {
		k.t.Fatal(err)
	}
	return n
}

func slotRevisionOf(l protocol.QualityV4Lane) int64 {
	v, _ := strconv.ParseInt(l.SlotRevision, 10, 64)
	return v
}

// invariants asserts the version invariants of design 2.4 on the stored state.
func (k *v4Kit) invariants() {
	k.t.Helper()
	_, revision := k.revisions()
	if n := k.count(`SELECT COUNT(*) FROM quality_slots WHERE agent_id=? AND revision>?`, k.id, revision); n != 0 {
		k.t.Fatalf("%d slots have a slot revision above the measurement revision %d", n, revision)
	}
	if n := k.count(`SELECT COUNT(*) FROM quality_targets WHERE agent_id=? AND created_revision>?`, k.id, revision); n != 0 {
		k.t.Fatalf("%d targets were created above the measurement revision", n)
	}
	// Every open interval starts at the slot revision of its lane and has no end.
	if n := k.count(`SELECT COUNT(*) FROM quality_intervals i JOIN quality_slots s ON s.agent_id=i.agent_id AND s.slot=i.slot AND s.family=i.family AND s.protocol=i.protocol WHERE i.agent_id=? AND i.end_at IS NULL AND (i.config_from<>s.revision OR i.config_until IS NOT NULL OR i.target_id<>s.target_id)`, k.id); n != 0 {
		k.t.Fatalf("%d open intervals disagree with their slot", n)
	}
	// Closed intervals end before the next one of the same lane starts.
	ivs := k.intervals()
	for i := 1; i < len(ivs); i++ {
		a, b := ivs[i-1], ivs[i]
		if a.Slot == b.Slot && a.Family == b.Family && a.Proto == b.Proto && (a.Until == nil || *a.Until >= b.From) {
			k.t.Fatalf("intervals overlap: %+v then %+v", a, b)
		}
	}
}

func (k *v4Kit) summary() string {
	var parts []string
	for _, iv := range k.intervals() {
		until := "open"
		if iv.Until != nil {
			until = strconv.FormatInt(*iv.Until, 10)
		}
		parts = append(parts, fmt.Sprintf("%s/%s/%s:%d-%s", iv.Slot, iv.Family, iv.Proto, iv.From, until))
	}
	return strings.Join(parts, " ")
}
