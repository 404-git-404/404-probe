package storage

import (
	"encoding/json"
	"errors"
	"testing"

	"404-probe/internal/protocol"
)

func (k *v4Kit) saveThreshold(expected string, interval int, threshold *protocol.QualityThreshold, slots ...protocol.QualityV4Slot) (protocol.QualityV4Config, error) {
	if expected == "" {
		expected = k.get().SettingsRevision
	}
	return k.s.SaveQualityConfigV4(k.ctx, k.id, protocol.QualityV4SaveRequest{ExpectedRevision: expected, IntervalMS: interval, Slots: v4Slots(slots...), SlowThresholdMS: threshold}, k.tick())
}

func threshold(v int) *protocol.QualityThreshold {
	return &protocol.QualityThreshold{Value: v, Valid: true}
}

func (k *v4Kit) thresholdSlot() protocol.QualityV4Slot {
	return v4Slot("telecom", "example.com", 443, "v4tcp", "v4icmp", "v6tcp", "v6icmp")
}

func TestSlowThresholdDefaultsToFiveHundredAndIsReadBack(t *testing.T) {
	k := v4Base(t, false)
	if cfg := k.get(); cfg.SlowThresholdMS != 500 {
		t.Fatalf("default threshold %d", cfg.SlowThresholdMS)
	}
}

func TestSlowThresholdRangeAndShapeAreValidated(t *testing.T) {
	k := v4Base(t, false)
	for _, ok := range []int{50, 3000, 51, 2999, 500} {
		if cfg, err := k.saveThreshold("", 30000, threshold(ok), k.thresholdSlot()); err != nil || cfg.SlowThresholdMS != ok {
			t.Fatalf("%d refused: %+v %v", ok, cfg, err)
		}
	}
	before := k.get()
	for _, bad := range []protocol.QualityThreshold{{Value: 49, Valid: true}, {Value: 3001, Valid: true}, {Value: 0, Valid: true}, {Value: -5, Valid: true}, {Valid: false}} {
		bad := bad
		if _, err := k.saveThreshold("", 30000, &bad, k.thresholdSlot()); !errors.Is(err, ErrQualityInvalid) {
			t.Fatalf("%+v accepted: %v", bad, err)
		}
	}
	if after := k.get(); after.SlowThresholdMS != before.SlowThresholdMS || after.SettingsRevision != before.SettingsRevision {
		t.Fatalf("a refused request changed the configuration: %+v -> %+v", before, after)
	}
}

// What the JSON decoder hands over for the threshold: decimals, exponents and strings are not whole numbers.
func TestQualityThresholdJSONOnlyAcceptsWholeNumbers(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
		value int
	}{
		{`{"slow_threshold_ms":500}`, true, 500},
		{`{"slow_threshold_ms":50}`, true, 50},
		{`{"slow_threshold_ms":3000}`, true, 3000},
		{`{"slow_threshold_ms":500.5}`, false, 0},
		{`{"slow_threshold_ms":500.0}`, false, 0},
		{`{"slow_threshold_ms":5e2}`, false, 0},
		{`{"slow_threshold_ms":"500"}`, false, 0},
		{`{"slow_threshold_ms":true}`, false, 0},
		{`{"slow_threshold_ms":[500]}`, false, 0},
		{`{"slow_threshold_ms":12345678901234567890}`, false, 0},
	} {
		var request protocol.QualityV4SaveRequest
		if err := json.Unmarshal([]byte(tc.body), &request); err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if request.SlowThresholdMS == nil || request.SlowThresholdMS.Valid != tc.valid || (tc.valid && request.SlowThresholdMS.Value != tc.value) {
			t.Fatalf("%s decoded to %+v", tc.body, request.SlowThresholdMS)
		}
	}
	var absent protocol.QualityV4SaveRequest
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil || absent.SlowThresholdMS != nil {
		t.Fatalf("an absent field must stay absent: %+v %v", absent.SlowThresholdMS, err)
	}
}

// An old page (v1.0.6) does not send the field: the stored value stays, also after it re-read the revision.
func TestSlowThresholdAbsentKeepsTheStoredValue(t *testing.T) {
	k := v4Base(t, false)
	if _, err := k.saveThreshold("", 30000, threshold(800), k.thresholdSlot()); err != nil {
		t.Fatal(err)
	}
	// The old page saves a change of the interval without the field.
	cfg, err := k.saveThreshold("", 60000, nil, k.thresholdSlot())
	if err != nil || cfg.SlowThresholdMS != 800 || cfg.IntervalMS != 60000 {
		t.Fatalf("absent threshold reset the stored one: %+v %v", cfg, err)
	}
	// And with an expired revision it conflicts as before; after it re-reads, the retry still keeps 800.
	if _, err := k.saveThreshold("0", 60000, nil, k.thresholdSlot()); !errors.Is(err, ErrQualityConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	cfg, err = k.saveThreshold(k.get().SettingsRevision, 30000, nil, k.thresholdSlot())
	if err != nil || cfg.SlowThresholdMS != 800 {
		t.Fatalf("after the re-read: %+v %v", cfg, err)
	}
}

// A page that saves with an out-of-date revision does not win over a newer threshold.
func TestSlowThresholdStaleRevisionConflicts(t *testing.T) {
	k := v4Base(t, false)
	stale := k.get().SettingsRevision
	if _, err := k.saveThreshold("", 30000, threshold(700), k.thresholdSlot()); err != nil {
		t.Fatal(err)
	}
	if _, err := k.saveThreshold(stale, 30000, threshold(900), k.thresholdSlot()); !errors.Is(err, ErrQualityConflict) {
		t.Fatalf("the stale threshold save was not refused: %v", err)
	}
	if cfg := k.get(); cfg.SlowThresholdMS != 700 {
		t.Fatalf("threshold %d", cfg.SlowThresholdMS)
	}
}

// The threshold is a setting, not a measurement: the settings revision moves, the measurement revision, the
// targets, intervals and slot revisions do not.
func TestSlowThresholdChangeIsASettingsChangeOnly(t *testing.T) {
	k := v4Base(t, true)
	before := k.get()
	settingsBefore, measurementBefore := k.revisions()
	intervalsBefore := k.summary()
	targetsBefore := len(k.targets())
	cfg, err := k.saveThreshold("", 30000, threshold(1200), k.thresholdSlot())
	if err != nil || cfg.SlowThresholdMS != 1200 {
		t.Fatalf("%+v %v", cfg, err)
	}
	settingsAfter, measurementAfter := k.revisions()
	if settingsAfter != settingsBefore+1 || measurementAfter != measurementBefore {
		t.Fatalf("revisions: settings %d->%d, measurement %d->%d", settingsBefore, settingsAfter, measurementBefore, measurementAfter)
	}
	if cfg.ConfigRevision != before.ConfigRevision {
		t.Fatalf("the configuration revision of the Agent moved: %s -> %s", before.ConfigRevision, cfg.ConfigRevision)
	}
	if k.summary() != intervalsBefore || len(k.targets()) != targetsBefore {
		t.Fatalf("intervals or targets changed:\n%s\n%s", intervalsBefore, k.summary())
	}
	for i, lane := range cfg.Lanes {
		if lane.SlotRevision != before.Lanes[i].SlotRevision {
			t.Fatalf("slot revision of %s/%s/%s moved", lane.Slot, lane.Family, lane.Protocol)
		}
	}
	k.invariants()
	// Saving the same threshold again changes nothing at all.
	cfg2, err := k.saveThreshold("", 30000, threshold(1200), k.thresholdSlot())
	if err != nil {
		t.Fatal(err)
	}
	if s, m := k.revisions(); s != settingsAfter || m != measurementAfter || cfg2.SlowThresholdMS != 1200 {
		t.Fatalf("an unchanged save moved a revision: %d %d", s, m)
	}
}

// The master switch builds its own proposal: it must carry the threshold, or it would reset it to the default.
func TestMasterSwitchDoesNotResetTheSlowThreshold(t *testing.T) {
	k := v4Base(t, false)
	if _, err := k.saveThreshold("", 30000, threshold(1500), k.thresholdSlot()); err != nil {
		t.Fatal(err)
	}
	if cfg := k.enable(true); cfg.SlowThresholdMS != 1500 || !cfg.Enabled {
		t.Fatalf("switch on: %+v", cfg)
	}
	if cfg := k.enable(false); cfg.SlowThresholdMS != 1500 || cfg.Enabled {
		t.Fatalf("switch off: %+v", cfg)
	}
	var stored int
	if err := k.s.db.QueryRow(`SELECT slow_threshold_ms FROM quality_config WHERE agent_id=?`, k.id).Scan(&stored); err != nil || stored != 1500 {
		t.Fatalf("stored %d %v", stored, err)
	}
}

// Authorization recalculations (a restarted Agent, a changed capability) go through the same function with no
// proposal: they must not touch the threshold either.
func TestAuthorizationRecalculationKeepsTheSlowThreshold(t *testing.T) {
	k := v4Base(t, true)
	if _, err := k.saveThreshold("", 30000, threshold(2100), k.thresholdSlot()); err != nil {
		t.Fatal(err)
	}
	k.restart()
	k.negotiate(v4Caps(protocol.QualityIPv6None))
	if cfg := k.get(); cfg.SlowThresholdMS != 2100 {
		t.Fatalf("threshold after the recalculation: %d", cfg.SlowThresholdMS)
	}
}
