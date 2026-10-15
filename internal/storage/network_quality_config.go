package storage

import (
	"404-probe/internal/protocol"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"
)

var ErrQualityConflict = errors.New("quality revision conflict")
var ErrQualityQuota = errors.New("quality quota")
var ErrQualityUnsupported = errors.New("quality unsupported")
var ErrQualityInvalid = errors.New("invalid quality config")

var ErrQualityStoragePressure = errors.New("quality storage pressure")

// Resolver must be a pure local catalog lookup, never a network operation.
type QualityResolver func(protocol.QualityChoice, string) (protocol.QualityTarget, error)

func qualityDefaultChoices() []protocol.QualityChoice {
	var choices []protocol.QualityChoice
	for _, slot := range []string{"telecom", "unicom", "mobile"} {
		choices = append(choices, protocol.QualityChoice{Slot: slot, Source: "catalog", Protocol: "tcp"})
	}
	return choices
}
func qualityChoicesTx(ctx context.Context, tx *sql.Tx, id string) ([]protocol.QualityChoice, int64, bool, bool, error) {
	choices := qualityDefaultChoices()
	var revision int64
	var enabled, v6 bool
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT revision,enabled,ipv6,choices FROM quality_config WHERE agent_id=?`, id).Scan(&revision, &enabled, &v6, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return choices, 0, false, false, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &choices)
	}
	return choices, revision, enabled, v6, err
}
func qualityConfigTx(ctx context.Context, tx *sql.Tx, id string, versions ...int) (protocol.QualityConfig, error) {
	version := 1
	if len(versions) > 0 && versions[0] == 2 {
		version = 2
	}
	choices, rev, enabled, v6, err := qualityChoicesTx(ctx, tx, id)
	out := protocol.QualityConfig{Version: version, Revision: strconv.FormatInt(rev, 10), Enabled: enabled, IPv6: v6, IntervalMS: 30000, JitterPercent: 20, TimeoutMS: 5000, Targets: []protocol.QualityTarget{}}
	if err != nil {
		return out, err
	}
	caps, base, excluded, err := qualityCapabilityTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	out.Supported = base
	if version == 2 {
		out.Capabilities = caps
		out.Semantics = protocol.QualityV2Semantics
		if caps != nil {
			out.Semantics = caps.Semantics
		}
		out.Supported = caps != nil
		out.State = "ready"
		out.RegionID, err = qualityRegionTx(ctx, tx, id, choices)
		if err != nil {
			return out, err
		}
		if out.RegionID == "" {
			out.State = "region_selection_required"
			out.Enabled = false
		}
		if caps == nil {
			out.State = "not_scheduled_legacy"
			out.Enabled = false
		}
	}
	if excluded != "" {
		out.Enabled = false
		if version == 2 {
			out.State = excluded
		}
		return out, nil
	}
	facts, err := qualityLaneChoicesTx(ctx, tx, id, choices)
	if err != nil {
		return out, err
	}
	factByKey := map[string]protocol.QualityLaneChoice{}
	for _, l := range facts {
		factByKey[qualityLaneKey(l)] = l
	}
	rows, err := tx.QueryContext(ctx, `SELECT q.slot,q.family,q.protocol,q.revision,q.enabled,t.payload FROM quality_slots q LEFT JOIN quality_targets t ON t.id=q.target_id WHERE q.agent_id=? ORDER BY q.slot,q.family,q.protocol`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var slot, family, proto string
		var sr int64
		var laneEnabled bool
		var payload sql.NullString
		if err := rows.Scan(&slot, &family, &proto, &sr, &laneEnabled, &payload); err != nil {
			return out, err
		}
		if version == 1 && family == "ipv6" && !v6 {
			continue
		}
		var choice protocol.QualityChoice
		for _, c := range choices {
			if c.Slot == slot {
				choice = c
			}
		}
		if version == 1 && choice.Protocol != proto {
			continue
		}
		t := protocol.QualityTarget{Slot: slot, Family: family, Protocol: proto, Source: choice.Source, Region: choice.Region, Status: "unavailable"}
		if l, ok := factByKey[slot+"/"+family+"/"+proto]; ok {
			t.Source = l.Source
			t.Region = l.Region
			if l.Source == "manual" {
				t.Host = l.Host
				t.Port = l.Port
			}
		}
		if !payload.Valid && choice.Protocol == proto && choice.Source == "manual" {
			t.Host = choice.Host
			t.Port = choice.Port
		}
		if payload.Valid {
			if err := json.Unmarshal([]byte(payload.String), &t); err != nil {
				return out, err
			}
			t.Status = "active"
			if !out.Enabled || !laneEnabled || (family == "ipv6" && !v6) {
				t.Status = "paused"
			}
		}
		if version == 2 {
			if caps == nil {
				t.Status = "not_scheduled_legacy"
			} else if !qualityLaneSupported(caps, family, proto) {
				t.Status = "unavailable"
			} else if t.Region != out.RegionID {
				t.Status = "paused"
			}
		}
		t.SlotRevision = strconv.FormatInt(sr, 10)
		out.Targets = append(out.Targets, t)
	}
	if len(out.Targets) == 0 && version == 1 {
		for _, c := range choices {
			out.Targets = append(out.Targets, protocol.QualityTarget{Slot: c.Slot, Family: "ipv4", SlotRevision: "0", Source: c.Source, Protocol: c.Protocol, Region: c.Region, Status: "unavailable"})
		}
	}
	return out, rows.Err()
}
func (s *Store) GetQualityConfig(ctx context.Context, id string) (protocol.QualityConfig, error) {
	return s.getQualityConfig(ctx, id, 0, "")
}
func (s *Store) GetQualitySessionConfig(ctx context.Context, id string, epoch uint64, session string, versions ...int) (protocol.QualityConfig, error) {
	if epoch == 0 || epoch > math.MaxInt64 || session == "" {
		return protocol.QualityConfig{}, ErrQualityUnsupported
	}
	return s.getQualityConfig(ctx, id, int64(epoch), session, versions...)
}
func (s *Store) getQualityConfig(ctx context.Context, id string, epoch int64, session string, versions ...int) (protocol.QualityConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	budget := s.qualityBudget(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)`, id).Scan(&exists); err != nil {
		return protocol.QualityConfig{}, err
	}
	if !exists {
		return protocol.QualityConfig{}, ErrAgentNotFound
	}
	config, err := qualityConfigTx(ctx, tx, id, versions...)
	if err != nil {
		return config, err
	}
	if epoch > 0 {
		if err := requireActiveAgentTx(ctx, tx, id); err != nil {
			return config, err
		}
		if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
			return config, err
		}
		var supported bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=? AND c.epoch=? AND c.session_id=? AND c.supported=1)`, id, epoch, session).Scan(&supported); err != nil {
			return config, err
		}
		if !supported || (len(versions) > 0 && versions[0] == 2 && !config.Supported) {
			return config, ErrQualityUnsupported
		}
	}
	if epoch > 0 {
		budget.apply(&config)
	}
	return config, nil
}

func (s *Store) UpdateQualityConfig(ctx context.Context, id string, update protocol.QualityConfigUpdate, resolve QualityResolver, now time.Time) (protocol.QualityConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if update.ExpectedRevision != "0" {
		if _, err := protocol.QualityInteger(update.ExpectedRevision); err != nil {
			return protocol.QualityConfig{}, ErrQualityInvalid
		}
	}
	pressure := s.qualityPressure(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	defer tx.Rollback()
	if err := requireActiveAgentTx(ctx, tx, id); err != nil {
		return protocol.QualityConfig{}, err
	}
	if err := requireNoAgentRemovalTx(ctx, tx, id); err != nil {
		return protocol.QualityConfig{}, err
	}
	choices, rev, enabled, v6, err := qualityChoicesTx(ctx, tx, id)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	if update.ExpectedRevision != strconv.FormatInt(rev, 10) {
		return protocol.QualityConfig{}, ErrQualityConflict
	}
	if rev == math.MaxInt64 || len(update.Choices) > 3 || resolve == nil {
		return protocol.QualityConfig{}, ErrQualityInvalid
	}
	seen := map[string]bool{}
	for _, c := range update.Choices {
		index := -1
		for i, old := range choices {
			if old.Slot == c.Slot {
				index = i
			}
		}
		if index < 0 || seen[c.Slot] || len(c.Region) > 120 || len(c.Host) > 253 || (c.Protocol != "tcp" && c.Protocol != "icmp") || (c.Source != "catalog" && c.Source != "manual") {
			return protocol.QualityConfig{}, ErrQualityInvalid
		}
		seen[c.Slot] = true
		choices[index] = c
	}
	newJSON, _ := json.Marshal(choices)
	oldChoices, _, _, _, _ := qualityChoicesTx(ctx, tx, id)
	oldJSON, _ := json.Marshal(oldChoices)
	if rev > 0 && string(newJSON) == string(oldJSON) && enabled == update.Enabled && v6 == update.IPv6 {
		return qualityConfigTx(ctx, tx, id)
	}
	newRev := rev + 1
	for _, choice := range choices {
		for _, family := range []string{"ipv4", "ipv6"} {
			target, err := resolve(choice, family)
			if err != nil {
				return protocol.QualityConfig{}, ErrQualityInvalid
			}
			if err := qualitySetLaneTx(ctx, tx, id, choice, family, target, true, true, update, newRev, pressure, now); err != nil {
				return protocol.QualityConfig{}, err
			}
		}
	}
	if err := qualityReconcileOtherLanesTx(ctx, tx, id, choices, update, newRev, pressure, now); err != nil {
		return protocol.QualityConfig{}, err
	}
	if err := qualitySyncLegacyLanesTx(ctx, tx, id, choices); err != nil {
		return protocol.QualityConfig{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quality_targets SET retired_at=CASE WHEN EXISTS(SELECT 1 FROM quality_slots s WHERE s.target_id=quality_targets.id) THEN NULL ELSE COALESCE(retired_at,?) END WHERE agent_id=?`, now.UnixMilli(), id); err != nil {
		return protocol.QualityConfig{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,ipv6=excluded.ipv6,choices=excluded.choices,updated_at=excluded.updated_at,region_id=''`, id, newRev, update.Enabled, update.IPv6, string(newJSON), now.UnixMilli()); err != nil {
		return protocol.QualityConfig{}, err
	}
	out, err := qualityConfigTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.QualityConfig{}, err
	}
	return out, nil
}

func qualityCountTx(ctx context.Context, tx *sql.Tx, id, field string, local, global *int) error {
	// field is exclusively a static internal table-counter name, never user input.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT `+field+` FROM quality_usage WHERE agent_id=?),0)`, id).Scan(local); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `SELECT value FROM quality_totals WHERE kind=?`, field).Scan(global)
}

func qualitySetLaneTx(ctx context.Context, tx *sql.Tx, id string, choice protocol.QualityChoice, family string, target protocol.QualityTarget, laneEnabled, legacy bool, update protocol.QualityConfigUpdate, newRev int64, pressure bool, now time.Time) error {
	excluded, err := qualityExcludedTx(ctx, tx, id)
	if err != nil {
		return err
	}
	target.Slot = choice.Slot
	target.Family = family
	target.Protocol = choice.Protocol
	target.Source = choice.Source
	target.Region = choice.Region
	target.SlotRevision = ""
	target.Status = ""
	target.ID = ""
	var targetID string
	if family == "ipv6" && !update.IPv6 {
		target.Host = ""
	}
	if target.Host != "" {
		fingerprintBytes, _ := json.Marshal(struct {
			Agent  string
			Target protocol.QualityTarget
		}{id, target})
		digest := sha256.Sum256(fingerprintBytes)
		fingerprint := hex.EncodeToString(digest[:])
		targetID = fingerprint[:32]
		target.ID = targetID
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_targets WHERE agent_id=? AND fingerprint=?)`, id, fingerprint).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if pressure {
				return ErrQualityStoragePressure
			}
			var local, global int
			if err := qualityCountTx(ctx, tx, id, "targets", &local, &global); err != nil {
				return err
			}
			if local >= qualityTargetDevice || global >= qualityTargetGlobal {
				return ErrQualityQuota
			}
			payload, _ := json.Marshal(target)
			if _, err := tx.ExecContext(ctx, `INSERT INTO quality_targets(id,agent_id,fingerprint,payload,created_at) VALUES(?,?,?,?,?)`, targetID, id, fingerprint, string(payload), now.UnixMilli()); err != nil {
				return err
			}
		}
	}
	var oldID sql.NullString
	var oldRev int64
	var oldEnabled bool
	err = tx.QueryRowContext(ctx, `SELECT target_id,revision,enabled FROM quality_slots WHERE agent_id=? AND slot=? AND family=? AND protocol=?`, id, choice.Slot, family, choice.Protocol).Scan(&oldID, &oldRev, &oldEnabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var oldActive bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_intervals WHERE agent_id=? AND slot=? AND family=? AND protocol=? AND end_at IS NULL)`, id, choice.Slot, family, choice.Protocol).Scan(&oldActive); err != nil {
		return err
	}
	active := targetID != "" && laneEnabled && update.Enabled && (family == "ipv4" || update.IPv6) && !excluded
	caps, _, _, err := qualityCapabilityTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if caps != nil && !qualityLaneSupported(caps, family, choice.Protocol) {
		active = false
	}
	if !legacy && caps == nil {
		active = false
	}
	var oldLegacy bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT v1_authorized FROM quality_intervals WHERE agent_id=? AND slot=? AND family=? AND protocol=? AND end_at IS NULL),?)`, id, choice.Slot, family, choice.Protocol, legacy).Scan(&oldLegacy); err != nil {
		return err
	}
	if oldRev > 0 && oldID.String == targetID && oldActive == active && oldEnabled == laneEnabled && (!active || oldLegacy == legacy) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quality_intervals SET end_at=?,config_until=? WHERE agent_id=? AND slot=? AND family=? AND protocol=? AND end_at IS NULL`, now.UnixMilli(), newRev-1, id, choice.Slot, family, choice.Protocol); err != nil {
		return err
	}
	var targetArg any
	if targetID != "" {
		targetArg = targetID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quality_slots(agent_id,slot,family,protocol,revision,target_id,enabled) VALUES(?,?,?,?,?,?,?) ON CONFLICT(agent_id,slot,family,protocol) DO UPDATE SET revision=excluded.revision,target_id=excluded.target_id,enabled=excluded.enabled`, id, choice.Slot, family, choice.Protocol, newRev, targetArg, laneEnabled); err != nil {
		return err
	}
	if active {
		if pressure {
			return ErrQualityStoragePressure
		}
		var local, global int
		if err := qualityCountTx(ctx, tx, id, "intervals", &local, &global); err != nil {
			return err
		}
		if local >= qualityIntervalDevice {
			return ErrQualityQuota
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO quality_intervals(agent_id,slot,family,protocol,revision,target_id,start_at,config_from,v1_authorized) VALUES(?,?,?,?,?,?,?,?,?)`, id, choice.Slot, family, choice.Protocol, newRev, targetID, now.UnixMilli(), newRev, legacy); err != nil {
			return err
		}
	}
	return nil
}
