package storage

import (
	"context"
	"database/sql"
	"time"
)

// Existing slot keys are also the history lane keys. Copy only settings; leave targets,
// authorization intervals and measurements linked to their original keys.
func migrateQualityObjects44Tx(ctx context.Context, tx *sql.Tx) error {
	var hasPosition, hasConfigured bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('quality_slot_settings') WHERE name='position'), EXISTS(SELECT 1 FROM pragma_table_info('quality_config') WHERE name='objects_configured')`).Scan(&hasPosition, &hasConfigured); err != nil {
		return err
	}
	if !hasPosition {
		for _, statement := range []string{
			`ALTER TABLE quality_slot_settings RENAME TO quality_slot_settings_43`,
			`CREATE TABLE quality_slot_settings(
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			slot TEXT NOT NULL,
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
			position INTEGER NOT NULL DEFAULT -1 CHECK(position BETWEEN -1 AND 7),
			show_on_card INTEGER NOT NULL DEFAULT 1 CHECK(show_on_card IN (0,1)),
			PRIMARY KEY(agent_id,slot)) WITHOUT ROWID`,
			`INSERT INTO quality_slot_settings
				SELECT *, CASE slot WHEN 'telecom' THEN 0 WHEN 'unicom' THEN 1 ELSE 2 END, 1 FROM quality_slot_settings_43`,
			`DROP TABLE quality_slot_settings_43`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if !hasConfigured {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE quality_config ADD COLUMN objects_configured INTEGER NOT NULL DEFAULT 0 CHECK(objects_configured IN (0,1))`); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(44,?)`, time.Now().UnixMilli())
	return err
}
