package storage

import (
	"404-probe/internal/protocol"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"time"
)

func qualityCapabilityTx(ctx context.Context, tx *sql.Tx, id string) (*protocol.QualityCapabilities, bool, string, error) {
	var raw sql.NullString
	var supported bool
	var reason string
	err := tx.QueryRowContext(ctx, `SELECT c.v2_details,c.supported,c.excluded_reason FROM quality_capability c JOIN agent_state s ON c.agent_id=s.agent_id AND c.epoch=s.epoch AND c.session_id=s.session_id WHERE c.agent_id=?`, id).Scan(&raw, &supported, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, "", nil
	}
	if err != nil {
		return nil, false, "", err
	}
	if !raw.Valid {
		return nil, supported, reason, nil
	}
	var caps protocol.QualityCapabilities
	if err := json.Unmarshal([]byte(raw.String), &caps); err != nil {
		return nil, false, "", err
	}
	if err := caps.Validate(); err != nil {
		return nil, false, "", err
	}
	return &caps, supported, reason, nil
}
func qualityExcludedTx(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	_, _, reason, err := qualityCapabilityTx(ctx, tx, id)
	return reason != "", err
}
func qualityLaneSupported(c *protocol.QualityCapabilities, family, proto string) bool {
	if c == nil || c.ExcludedReason != "" {
		return false
	}
	for _, l := range c.Lanes {
		if l.Family == family && l.Protocol == proto {
			return l.Supported
		}
	}
	return false
}
func qualityRegionTx(ctx context.Context, tx *sql.Tx, id string, choices []protocol.QualityChoice) (string, error) {
	var region string
	err := tx.QueryRowContext(ctx, `SELECT region_id FROM quality_config WHERE agent_id=?`, id).Scan(&region)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if region != "" {
		return region, nil
	}
	// Map only an already unified legacy catalog selection. No new target is created.
	for _, c := range choices {
		if c.Region == "" || c.Source != "catalog" {
			return "", nil
		}
		if region != "" && region != c.Region {
			return "", nil
		}
		region = c.Region
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.payload FROM quality_slots s JOIN quality_targets t ON s.target_id=t.id WHERE s.agent_id=?`, id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var t protocol.QualityTarget
		if err := rows.Scan(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			return "", err
		}
		if t.Region != region {
			return "", nil
		}
	}
	return region, rows.Err()
}
func qualityLaneKey(l protocol.QualityLaneChoice) string {
	return l.Slot + "/" + l.Family + "/" + l.Protocol
}
func qualityLaneChoicesTx(ctx context.Context, tx *sql.Tx, id string, choices []protocol.QualityChoice) ([]protocol.QualityLaneChoice, error) {
	var raw sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT v2_lanes FROM quality_config WHERE agent_id=?`, id).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if raw.Valid {
		var lanes []protocol.QualityLaneChoice
		err = json.Unmarshal([]byte(raw.String), &lanes)
		return lanes, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.slot,s.family,s.protocol,s.enabled,t.payload FROM quality_slots s LEFT JOIN quality_targets t ON t.id=s.target_id WHERE s.agent_id=? ORDER BY s.slot,s.family,s.protocol`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lanes := []protocol.QualityLaneChoice{}
	for rows.Next() {
		var l protocol.QualityLaneChoice
		var payload sql.NullString
		if err := rows.Scan(&l.Slot, &l.Family, &l.Protocol, &l.Enabled, &payload); err != nil {
			return nil, err
		}
		for _, c := range choices {
			if c.Slot == l.Slot && c.Protocol == l.Protocol {
				l.QualityChoice = c
			}
		}
		if payload.Valid {
			var t protocol.QualityTarget
			if err := json.Unmarshal([]byte(payload.String), &t); err != nil {
				return nil, err
			}
			l.Source = t.Source
			l.Region = t.Region
		}
		lanes = append(lanes, l)
	}
	return lanes, rows.Err()
}
func qualityLegacyLane(choices []protocol.QualityChoice, l protocol.QualityLaneChoice) bool {
	for _, c := range choices {
		if c.Slot == l.Slot {
			return c.Protocol == l.Protocol
		}
	}
	return false
}

func qualitySyncLegacyLanesTx(ctx context.Context, tx *sql.Tx, id string, choices []protocol.QualityChoice) error {
	var raw sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT v2_lanes FROM quality_config WHERE agent_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !raw.Valid {
		return nil
	}
	var lanes []protocol.QualityLaneChoice
	if err := json.Unmarshal([]byte(raw.String), &lanes); err != nil {
		return err
	}
	byKey := map[string]protocol.QualityLaneChoice{}
	for _, l := range lanes {
		byKey[qualityLaneKey(l)] = l
	}
	for _, c := range choices {
		for _, f := range []string{"ipv4", "ipv6"} {
			l := protocol.QualityLaneChoice{QualityChoice: c, Family: f, Enabled: true}
			byKey[qualityLaneKey(l)] = l
		}
	}
	lanes = nil
	for _, l := range byKey {
		lanes = append(lanes, l)
	}
	sort.Slice(lanes, func(i, j int) bool { return qualityLaneKey(lanes[i]) < qualityLaneKey(lanes[j]) })
	wire, err := json.Marshal(lanes)
	if err != nil || len(lanes) > 12 || len(wire) > 8192 {
		return ErrQualityInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE quality_config SET v2_lanes=? WHERE agent_id=?`, string(wire), id)
	return err
}
func qualityReconcileOtherLanesTx(ctx context.Context, tx *sql.Tx, id string, choices []protocol.QualityChoice, u protocol.QualityConfigUpdate, rev int64, pressure bool, now time.Time) error {
	// Existing other-protocol lanes retain identity when a legacy selection changes.
	var explicit bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_config WHERE agent_id=? AND v2_lanes IS NOT NULL)`, id).Scan(&explicit); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.slot,s.family,s.protocol,s.enabled,t.payload FROM quality_slots s LEFT JOIN quality_targets t ON t.id=s.target_id WHERE s.agent_id=?`, id)
	if err != nil {
		return err
	}
	type lane struct {
		l protocol.QualityLaneChoice
		t protocol.QualityTarget
	}
	var items []lane
	for rows.Next() {
		var x lane
		var raw sql.NullString
		if err := rows.Scan(&x.l.Slot, &x.l.Family, &x.l.Protocol, &x.l.Enabled, &raw); err != nil {
			rows.Close()
			return err
		}
		if qualityLegacyLane(choices, x.l) {
			continue
		}
		if raw.Valid {
			if err := json.Unmarshal([]byte(raw.String), &x.t); err != nil {
				rows.Close()
				return err
			}
			x.l.Source = x.t.Source
			x.l.Region = x.t.Region
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, x := range items {
		if !explicit {
			x.l.Enabled = false
		}
		if err := qualitySetLaneTx(ctx, tx, id, x.l.QualityChoice, x.l.Family, x.t, x.l.Enabled, false, u, rev, pressure, now); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) GetQualityConfigV2(ctx context.Context, id string) (protocol.QualityConfig, error) {
	return s.getQualityConfig(ctx, id, 0, "", 2)
}

// Lanes is a full enabled-selection snapshot. Omitted existing lanes are paused,
// preserving their slot and history. No protocol/address-family lane is inferred.
func (s *Store) UpdateQualityConfigV2(ctx context.Context, id string, u protocol.QualityV2ConfigUpdate, resolve QualityResolver, now time.Time) (protocol.QualityConfig, error) {
	if u.Validate() != nil || resolve == nil {
		return protocol.QualityConfig{}, ErrQualityInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
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
	if u.ExpectedRevision != strconv.FormatInt(rev, 10) {
		return protocol.QualityConfig{}, ErrQualityConflict
	}
	if rev == math.MaxInt64 {
		return protocol.QualityConfig{}, ErrQualityInvalid
	}
	caps, _, excluded, err := qualityCapabilityTx(ctx, tx, id)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	if caps == nil || excluded != "" {
		return protocol.QualityConfig{}, ErrQualityUnsupported
	}
	prior, err := qualityLaneChoicesTx(ctx, tx, id, choices)
	if err != nil {
		return protocol.QualityConfig{}, err
	}
	byKey := map[string]protocol.QualityLaneChoice{}
	for _, l := range prior {
		l.Enabled = false
		l.Region = u.RegionID
		byKey[qualityLaneKey(l)] = l
	}
	for _, l := range u.Lanes {
		l.Region = u.RegionID
		byKey[qualityLaneKey(l)] = l
	}
	if len(byKey) > 12 {
		return protocol.QualityConfig{}, ErrQualityInvalid
	}
	lanes := make([]protocol.QualityLaneChoice, 0, len(byKey))
	for _, l := range byKey {
		lanes = append(lanes, l)
	}
	sort.Slice(lanes, func(i, j int) bool { return qualityLaneKey(lanes[i]) < qualityLaneKey(lanes[j]) })
	wire, err := json.Marshal(lanes)
	if err != nil || len(wire) > 8192 {
		return protocol.QualityConfig{}, ErrQualityInvalid
	}
	var oldRegion string
	var oldWire sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT region_id,v2_lanes FROM quality_config WHERE agent_id=?`, id).Scan(&oldRegion, &oldWire)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.QualityConfig{}, err
	}
	if oldWire.Valid && oldWire.String == string(wire) && oldRegion == u.RegionID && enabled == u.Enabled && v6 == u.IPv6 {
		return qualityConfigTx(ctx, tx, id, 2)
	}
	update := protocol.QualityConfigUpdate{Enabled: u.Enabled, IPv6: u.IPv6}
	newRev := rev + 1
	for _, l := range lanes {
		target, err := resolve(l.QualityChoice, l.Family)
		if err != nil {
			return protocol.QualityConfig{}, ErrQualityInvalid
		}
		if err := qualitySetLaneTx(ctx, tx, id, l.QualityChoice, l.Family, target, l.Enabled, qualityLegacyLane(choices, l), update, newRev, pressure, now); err != nil {
			return protocol.QualityConfig{}, err
		}
	}
	oldChoices, _ := json.Marshal(choices)
	if _, err := tx.ExecContext(ctx, `INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at,region_id,v2_lanes) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET revision=excluded.revision,enabled=excluded.enabled,ipv6=excluded.ipv6,updated_at=excluded.updated_at,region_id=excluded.region_id,v2_lanes=excluded.v2_lanes`, id, newRev, u.Enabled, u.IPv6, string(oldChoices), now.UnixMilli(), u.RegionID, string(wire)); err != nil {
		return protocol.QualityConfig{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quality_targets SET retired_at=CASE WHEN EXISTS(SELECT 1 FROM quality_slots s WHERE s.target_id=quality_targets.id) THEN NULL ELSE COALESCE(retired_at,?) END WHERE agent_id=?`, now.UnixMilli(), id); err != nil {
		return protocol.QualityConfig{}, err
	}
	out, err := qualityConfigTx(ctx, tx, id, 2)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.QualityConfig{}, err
	}
	return out, nil
}
