package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"404-probe/internal/protocol"
)

// Migration 31 removes hand-entered ("manual") network-quality targets. A line that was saved as
// manual becomes "not set": its choice is reset to an empty catalog selection, its slot no longer
// points at a target and its open measurement interval ends, so no host or port that was typed in
// is handed to the Agent any more. Targets, samples and aggregates stay (history is kept); a target
// that nothing refers to any more is retired like any other replaced target.
//
// The migration is idempotent: it only touches rows that still say "manual".
func migrateQualityManualRemovalTx(ctx context.Context, tx *sql.Tx) error {
	now := time.Now().UnixMilli()
	type config struct {
		agent    string
		revision int64
		choices  []protocol.QualityChoice
		lanes    []protocol.QualityLaneChoice
		hasLanes bool
		changed  bool
	}
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,revision,choices,v2_lanes FROM quality_config`)
	if err != nil {
		return err
	}
	var configs []*config
	for rows.Next() {
		c := &config{}
		var rawChoices string
		var rawLanes sql.NullString
		if err := rows.Scan(&c.agent, &c.revision, &rawChoices, &rawLanes); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(rawChoices), &c.choices); err != nil {
			rows.Close()
			return err
		}
		if rawLanes.Valid {
			c.hasLanes = true
			if err := json.Unmarshal([]byte(rawLanes.String), &c.lanes); err != nil {
				rows.Close()
				return err
			}
		}
		configs = append(configs, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// Slots whose target was saved from manual input, found by what is stored rather than by the config.
	manualSlots := map[string][][3]string{}
	slotRows, err := tx.QueryContext(ctx, `SELECT s.agent_id,s.slot,s.family,s.protocol,t.payload FROM quality_slots s JOIN quality_targets t ON t.id=s.target_id`)
	if err != nil {
		return err
	}
	for slotRows.Next() {
		var agent, slot, family, proto, payload string
		if err := slotRows.Scan(&agent, &slot, &family, &proto, &payload); err != nil {
			slotRows.Close()
			return err
		}
		var target protocol.QualityTarget
		if json.Unmarshal([]byte(payload), &target) == nil && target.Source == "manual" {
			manualSlots[agent] = append(manualSlots[agent], [3]string{slot, family, proto})
		}
	}
	if err := slotRows.Err(); err != nil {
		slotRows.Close()
		return err
	}
	slotRows.Close()

	byAgent := map[string]*config{}
	for _, c := range configs {
		byAgent[c.agent] = c
		for i, choice := range c.choices {
			if choice.Source == "manual" {
				c.choices[i] = protocol.QualityChoice{Slot: choice.Slot, Source: "catalog", Protocol: choice.Protocol}
				manualSlots[c.agent] = append(manualSlots[c.agent], [3]string{choice.Slot, "ipv4", choice.Protocol}, [3]string{choice.Slot, "ipv6", choice.Protocol})
				c.changed = true
			}
		}
		for i, lane := range c.lanes {
			if lane.Source == "manual" {
				c.lanes[i].QualityChoice = protocol.QualityChoice{Slot: lane.Slot, Source: "catalog", Protocol: lane.Protocol}
				c.lanes[i].Enabled = false
				manualSlots[c.agent] = append(manualSlots[c.agent], [3]string{lane.Slot, lane.Family, lane.Protocol})
				c.changed = true
			}
		}
	}
	for agent, slots := range manualSlots {
		c := byAgent[agent]
		if c == nil {
			// A manual slot without a config row: nothing to rewrite, the slot is still detached below.
			c = &config{agent: agent}
			byAgent[agent] = c
		}
		revision := c.revision + 1
		seen := map[[3]string]bool{}
		for _, key := range slots {
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, err := tx.ExecContext(ctx, `UPDATE quality_intervals SET end_at=?,config_until=? WHERE agent_id=? AND slot=? AND family=? AND protocol=? AND end_at IS NULL`,
				now, revision-1, agent, key[0], key[1], key[2]); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE quality_slots SET revision=?,target_id=NULL WHERE agent_id=? AND slot=? AND family=? AND protocol=?`,
				revision, agent, key[0], key[1], key[2]); err != nil {
				return err
			}
		}
		if c.revision > 0 || c.changed {
			choices, err := json.Marshal(c.choices)
			if err != nil {
				return err
			}
			var lanes any
			if c.hasLanes {
				encoded, err := json.Marshal(c.lanes)
				if err != nil {
					return err
				}
				lanes = string(encoded)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE quality_config SET revision=?,choices=?,v2_lanes=?,updated_at=? WHERE agent_id=?`,
				revision, string(choices), lanes, now, agent); err != nil {
				return err
			}
		}
	}
	targetRows, err := tx.QueryContext(ctx, `SELECT id,payload FROM quality_targets WHERE retired_at IS NULL AND NOT EXISTS(SELECT 1 FROM quality_slots s WHERE s.target_id=quality_targets.id)`)
	if err != nil {
		return err
	}
	var retire []string
	for targetRows.Next() {
		var id, payload string
		if err := targetRows.Scan(&id, &payload); err != nil {
			targetRows.Close()
			return err
		}
		var target protocol.QualityTarget
		if json.Unmarshal([]byte(payload), &target) == nil && target.Source == "manual" {
			retire = append(retire, id)
		}
	}
	if err := targetRows.Err(); err != nil {
		targetRows.Close()
		return err
	}
	targetRows.Close()
	for _, id := range retire {
		if _, err := tx.ExecContext(ctx, `UPDATE quality_targets SET retired_at=COALESCE(retired_at,?) WHERE id=?`, now, id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(31,unixepoch())`)
	return err
}
