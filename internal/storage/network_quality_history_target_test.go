package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Every history read model names the target it is about by the target's own id. The page decides which lane a
// response belongs to by that id, so a response without it is dropped and the lane shows no data at all (R07 stage E, D1).
func TestHistoryReadModelsCarryTheTargetID(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	at := k.now.Add(time.Minute)
	for _, lane := range [][3]string{{"telecom", "ipv4", "tcp"}} {
		if acks := k.upload(at.Add(time.Second), k.sample(cfg, lane[0], lane[1], lane[2], 1, at, "success")); acks[0] != "committed" {
			t.Fatalf("upload: %v", acks)
		}
	}
	id := k.targetID(cfg, "telecom", "ipv4", "tcp")
	now := at.Add(2 * time.Minute)
	check := func(what, got string, wire any) {
		t.Helper()
		if got != id {
			t.Errorf("%s: target.id %q, want %q", what, got, id)
		}
		raw, err := json.Marshal(wire)
		if err != nil || !strings.Contains(string(raw), `"target":{"id":"`+id+`"`) && !strings.Contains(string(raw), `"id":"`+id+`"`) {
			t.Errorf("%s: the id is not on the wire: %v %.200s", what, err, raw)
		}
	}
	raw, err := k.s.QualityHistory(k.ctx, k.id, id, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	check("history (raw)", raw.Target.ID, raw)
	v2, err := k.s.QualityHistoryV2(k.ctx, k.id, id, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	check("history v2", v2.Target.ID, v2)
	day, err := k.s.QualityHistoryRollup(k.ctx, k.id, id, "1day", now)
	if err != nil {
		t.Fatal(err)
	}
	check("history v3 (1day)", day.Target.ID, day)
	card, err := k.s.QualityCardHistory(k.ctx, k.id, id, now)
	if err != nil {
		t.Fatal(err)
	}
	check("card history", card.Target.ID, card)
}

// The id stays on a target that has been replaced: its history is still readable and still names itself.
func TestHistoryOfAReplacedTargetKeepsItsID(t *testing.T) {
	k := v4Base(t, true)
	cfg := k.get()
	old := k.targetID(cfg, "telecom", "ipv4", "tcp")
	k.save(30000, v4Slot("telecom", "example.org", 443, "v4tcp"))
	if now := k.targetID(k.get(), "telecom", "ipv4", "tcp"); now == old {
		t.Fatal("the address change did not replace the target")
	}
	card, err := k.s.QualityCardHistory(k.ctx, k.id, old, k.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if card.Target.ID != old {
		t.Fatalf("replaced target: id %q, want %q", card.Target.ID, old)
	}
}
