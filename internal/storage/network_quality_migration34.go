package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"404-probe/internal/protocol"
)

// migration34FailAt lets tests inject a failure at a named stage; it is nil in production.
var migration34FailAt func(stage string) error

// migration34Skip stops migrate() before migration 34. Tests of older migrations use it so that they
// observe the state those migrations produce, not the state migration 34 later replaces.
var migration34Skip bool

func migration34Stage(stage string) error {
	if migration34FailAt != nil {
		return migration34FailAt(stage)
	}
	return nil
}

// Migration 34 (R07): the built-in catalog targets are retired and every card gets three empty
// user slots. It runs in the one transaction of migrate(), in four layers:
//
//	0 structure   new table and columns;
//	1 per device  a single new measurement/settings revision, switch off, labels from v1.0.4;
//	2 per lane    open intervals end, slot pointers clear;
//	3 targets     every still-active target retires.
//
// Samples, buckets, series and TCP Mini state are never touched, so all history is kept.
func migrateQualityUserTargetsTx(ctx context.Context, tx *sql.Tx) error {
	now := time.Now().UnixMilli()
	// Every structural step is skipped when it is already there (a rewound fixture or an operator
	// copy), so a repeated run cannot fail half way.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS quality_slot_settings(
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			slot TEXT NOT NULL CHECK(slot IN ('telecom','unicom','mobile')),
			kind TEXT NOT NULL CHECK(kind IN ('builtin','custom')),
			region TEXT NOT NULL DEFAULT '' CHECK(length(CAST(region AS BLOB))<=96),
			isp TEXT NOT NULL DEFAULT '' CHECK(length(CAST(isp AS BLOB))<=96),
			host TEXT NOT NULL DEFAULT '' CHECK(length(host)<=253),
			port INTEGER CHECK(port IS NULL OR port BETWEEN 1 AND 65535),
			slot_enabled INTEGER NOT NULL DEFAULT 0 CHECK(slot_enabled IN (0,1)),
			ipv4_tcp INTEGER NOT NULL DEFAULT 0 CHECK(ipv4_tcp IN (0,1)),
			ipv4_icmp INTEGER NOT NULL DEFAULT 0 CHECK(ipv4_icmp IN (0,1)),
			ipv6_tcp INTEGER NOT NULL DEFAULT 0 CHECK(ipv6_tcp IN (0,1)),
			ipv6_icmp INTEGER NOT NULL DEFAULT 0 CHECK(ipv6_icmp IN (0,1)),
			updated_revision INTEGER NOT NULL,
			PRIMARY KEY(agent_id,slot)) WITHOUT ROWID`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM quality_slot_settings`); err != nil {
		return err
	}
	for _, column := range []struct{ table, name, ddl string }{
		{"quality_config", "settings_revision", `ALTER TABLE quality_config ADD COLUMN settings_revision INTEGER NOT NULL DEFAULT 0`},
		{"quality_config", "interval_ms", `ALTER TABLE quality_config ADD COLUMN interval_ms INTEGER NOT NULL DEFAULT 30000 CHECK(interval_ms BETWEEN 30000 AND 300000 AND interval_ms%30000=0)`},
		{"quality_config", "targets_granted", `ALTER TABLE quality_config ADD COLUMN targets_granted INTEGER NOT NULL DEFAULT 0 CHECK(targets_granted IN (0,1))`},
		{"quality_config", "capability_snapshot", `ALTER TABLE quality_config ADD COLUMN capability_snapshot TEXT CHECK(capability_snapshot IS NULL OR (length(CAST(capability_snapshot AS BLOB))<=2048 AND json_valid(capability_snapshot)))`},
		{"quality_capability", "user_targets", `ALTER TABLE quality_capability ADD COLUMN user_targets INTEGER NOT NULL DEFAULT 0 CHECK(user_targets IN (0,1))`},
		{"quality_capability", "ipv6_address", `ALTER TABLE quality_capability ADD COLUMN ipv6_address TEXT NOT NULL DEFAULT 'unknown' CHECK(ipv6_address IN ('present','none','unknown'))`},
		{"quality_targets", "identity", `ALTER TABLE quality_targets ADD COLUMN identity TEXT`},
		{"quality_targets", "created_revision", `ALTER TABLE quality_targets ADD COLUMN created_revision INTEGER`},
		{"quality_targets", "first_active_at", `ALTER TABLE quality_targets ADD COLUMN first_active_at INTEGER`},
	} {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('`+column.table+`') WHERE name=?`, column.name).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := tx.ExecContext(ctx, column.ddl); err != nil {
				return err
			}
		}
	}
	if err := migration34Stage("structure"); err != nil {
		return err
	}

	type device struct {
		id               string
		revision         int64
		hasConfig        bool
		regionID         string
		choices, lanes   string
		regions          map[string]string
		unreadableRegion map[string]bool
	}
	var devices []*device
	byID := map[string]*device{}
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,revision,region_id,choices,v2_lanes FROM quality_config`)
	if err != nil {
		return err
	}
	for rows.Next() {
		d := &device{hasConfig: true, regions: map[string]string{}, unreadableRegion: map[string]bool{}}
		var lanes sql.NullString
		if err := rows.Scan(&d.id, &d.revision, &d.regionID, &d.choices, &lanes); err != nil {
			rows.Close()
			return err
		}
		d.lanes = lanes.String
		devices = append(devices, d)
		byID[d.id] = d
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	// Defensive: a device without a configuration row that has slot rows or an OPEN interval. Which
	// devices are included is decided by that; the version it takes over counts every slot and every
	// interval it ever had, ended ones too. A device with only ended history is left alone.
	orphans, err := tx.QueryContext(ctx, `SELECT agent_id,MAX(revision) FROM (SELECT agent_id,revision FROM quality_slots UNION ALL SELECT agent_id,revision FROM quality_intervals)
		WHERE agent_id IN (SELECT agent_id FROM quality_slots UNION SELECT agent_id FROM quality_intervals WHERE end_at IS NULL) GROUP BY agent_id`)
	if err != nil {
		return err
	}
	for orphans.Next() {
		var id string
		var revision int64
		if err := orphans.Scan(&id, &revision); err != nil {
			orphans.Close()
			return err
		}
		if byID[id] == nil {
			d := &device{id: id, revision: revision, regions: map[string]string{}, unreadableRegion: map[string]bool{}}
			devices = append(devices, d)
			byID[id] = d
		}
	}
	if err := orphans.Err(); err != nil {
		orphans.Close()
		return err
	}
	orphans.Close()

	defaultChoices, _ := json.Marshal(qualityDefaultChoices())
	for _, d := range devices {
		// Labels come from the saved v1.0.4 choice, never from the deleted catalog.
		var choices []protocol.QualityChoice
		var lanes []protocol.QualityLaneChoice
		_ = json.Unmarshal([]byte(d.choices), &choices)
		_ = json.Unmarshal([]byte(d.lanes), &lanes)
		for _, slot := range protocol.QualitySlotOrder() {
			candidates := []string{d.regionID}
			for _, c := range choices {
				if c.Slot == slot {
					candidates = append(candidates, c.Region)
				}
			}
			for _, l := range lanes {
				if l.Slot == slot {
					candidates = append(candidates, l.Region)
				}
			}
			for _, candidate := range candidates {
				if candidate == "" {
					continue
				}
				province, ok := migrationProvince(candidate)
				if ok {
					d.regions[slot] = province
					break
				}
				d.unreadableRegion[slot] = true
			}
			if d.regions[slot] == "" && d.unreadableRegion[slot] {
				slog.Warn("network-quality region could not be mapped during the schema 34 migration; the label was left empty", "agent_id", d.id, "slot", slot)
			}
		}
	}

	for i, d := range devices {
		rev := d.revision
		next := rev + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO quality_config(agent_id,revision,enabled,ipv6,choices,updated_at,region_id,v2_lanes,settings_revision,interval_ms,targets_granted,capability_snapshot)
			VALUES(?,?,0,0,?,?,'',NULL,?,30000,0,NULL)
			ON CONFLICT(agent_id) DO UPDATE SET revision=excluded.revision,enabled=0,ipv6=0,choices=excluded.choices,updated_at=excluded.updated_at,region_id='',v2_lanes=NULL,settings_revision=excluded.settings_revision,interval_ms=30000,targets_granted=0,capability_snapshot=NULL`,
			d.id, next, string(defaultChoices), now, next); err != nil {
			return err
		}
		for _, slot := range protocol.QualitySlotOrder() {
			if _, err := tx.ExecContext(ctx, `INSERT INTO quality_slot_settings(agent_id,slot,kind,region,isp,host,port,slot_enabled,ipv4_tcp,ipv4_icmp,ipv6_tcp,ipv6_icmp,updated_revision) VALUES(?,?,'builtin',?,?,'',NULL,0,0,0,0,0,?)`,
				d.id, slot, d.regions[slot], protocol.QualityISPForSlot(slot), next); err != nil {
				return err
			}
		}
		// Layer 2: only open intervals end; closed ones keep their values.
		if _, err := tx.ExecContext(ctx, `UPDATE quality_intervals SET end_at=?,config_until=? WHERE agent_id=? AND end_at IS NULL`, now, rev, d.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE quality_slots SET target_id=NULL,enabled=0,revision=? WHERE agent_id=?`, next, d.id); err != nil {
			return err
		}
		if i == len(devices)/2 {
			if err := migration34Stage("devices"); err != nil {
				return err
			}
		}
	}
	// Layer 3: no slot refers to a target any more; payloads are not parsed, so damaged ones retire too.
	if _, err := tx.ExecContext(ctx, `UPDATE quality_targets SET retired_at=? WHERE retired_at IS NULL`, now); err != nil {
		return err
	}
	if err := migration34Stage("validation"); err != nil {
		return err
	}

	for _, check := range []struct{ name, query string }{
		{"open interval", `SELECT COUNT(*) FROM quality_intervals WHERE end_at IS NULL`},
		{"enabled configuration", `SELECT COUNT(*) FROM quality_config WHERE enabled<>0`},
		{"enabled slot", `SELECT COUNT(*) FROM quality_slots WHERE enabled<>0 OR target_id IS NOT NULL`},
		{"active target", `SELECT COUNT(*) FROM quality_targets WHERE retired_at IS NULL`},
		{"user target session", `SELECT COUNT(*) FROM quality_capability WHERE user_targets<>0`},
		{"configuration without three slots", `SELECT COUNT(*) FROM quality_config c WHERE (SELECT COUNT(*) FROM quality_slot_settings s WHERE s.agent_id=c.agent_id)<>3`},
		{"interval usage", `SELECT COUNT(*) FROM quality_usage u WHERE u.intervals<>(SELECT COUNT(*) FROM quality_intervals i WHERE i.agent_id=u.agent_id)`},
		{"interval total", `SELECT value<>(SELECT COUNT(*) FROM quality_intervals) FROM quality_totals WHERE kind='intervals'`},
	} {
		var bad int
		if err := tx.QueryRowContext(ctx, check.query).Scan(&bad); err != nil {
			return err
		}
		if bad != 0 {
			return fmt.Errorf("quality migration 34 check failed: %s", check.name)
		}
	}
	violations, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer violations.Close()
	if violations.Next() {
		return fmt.Errorf("quality migration 34 foreign key violation")
	}
	if err := violations.Err(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(34,unixepoch())`)
	return err
}

// migrationProvince reads the province from a v1.0.4 region id. A province id is
// ["province","河北",null]; a city id is ["city","广东","深圳"] and maps to its province (P2).
func migrationProvince(id string) (string, bool) {
	var parts []any
	if err := json.Unmarshal([]byte(id), &parts); err == nil {
		if len(parts) >= 2 {
			if kind, ok := parts[0].(string); ok && (kind == "province" || kind == "city") {
				if name, ok := parts[1].(string); ok && protocol.QualityProvince(name) {
					return name, true
				}
			}
		}
		return "", false
	}
	if protocol.QualityProvince(id) {
		return id, true
	}
	return "", false
}
