package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

func migrateNetworkTargetsAndSkins49Tx(ctx context.Context, tx *sql.Tx) error {
	colors := "'" + strings.Join(tagColors[:], "','") + "'"
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE network_target_library (
			id TEXT NOT NULL PRIMARY KEY,
			region TEXT NOT NULL,
			isp TEXT NOT NULL,
			host TEXT NOT NULL COLLATE NOCASE CHECK(host<>''),
			port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
			revision INTEGER NOT NULL CHECK(revision>0),
			UNIQUE(region,isp,host,port)
		);
		ALTER TABLE quality_slot_settings ADD COLUMN library_id TEXT REFERENCES network_target_library(id);
		ALTER TABLE quality_slot_settings ADD COLUMN legacy_label INTEGER NOT NULL DEFAULT 0 CHECK(legacy_label IN (0,1));
		UPDATE quality_slot_settings SET kind='custom', legacy_label=1,
			isp=CASE isp WHEN 'telecom' THEN '电信' WHEN 'unicom' THEN '联通' WHEN 'mobile' THEN '移动' ELSE isp END
			WHERE kind='builtin';
		INSERT INTO network_target_library(id,region,isp,host,port,revision)
			SELECT lower(hex(randomblob(16))),region,isp,lower(host),port,1
			FROM quality_slot_settings WHERE host<>'' GROUP BY region,isp,lower(host),port;
		UPDATE quality_slot_settings AS s SET library_id=(
			SELECT l.id FROM network_target_library l
			WHERE l.region=s.region AND l.isp=s.isp AND l.host=s.host AND l.port=s.port
		) WHERE s.host<>'';
		CREATE INDEX idx_quality_slot_library ON quality_slot_settings(library_id,agent_id) WHERE library_id IS NOT NULL;

		CREATE TABLE skins (
			id TEXT NOT NULL PRIMARY KEY CHECK(id<>'default'),
			name TEXT NOT NULL,
			author TEXT NOT NULL,
			description TEXT NOT NULL,
			light TEXT NOT NULL,
			dark TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE skin_settings (
			singleton INTEGER NOT NULL PRIMARY KEY CHECK(singleton=1),
			active_id TEXT DEFAULT NULL REFERENCES skins(id)
		);
		INSERT INTO skin_settings(singleton) VALUES(1);

		CREATE TABLE tags_49 (
			id TEXT NOT NULL PRIMARY KEY CHECK(length(id)=32 AND id NOT GLOB '*[^0-9a-f]*'),
			name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 20),
			name_key TEXT NOT NULL UNIQUE CHECK(length(name_key)>0),
			revision INTEGER NOT NULL CHECK(revision>0),
			color TEXT NOT NULL CHECK(color IN (`+colors+`))
		);
		INSERT INTO tags_49 SELECT id,name,name_key,revision,color FROM tags;
		CREATE TABLE agent_tags_49 (
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			tag_id TEXT NOT NULL REFERENCES tags_49(id) ON DELETE CASCADE,
			PRIMARY KEY(agent_id,tag_id)
		);
		INSERT INTO agent_tags_49 SELECT agent_id,tag_id FROM agent_tags;
		DROP TABLE agent_tags;
		DROP TABLE tags;
		ALTER TABLE tags_49 RENAME TO tags;
		ALTER TABLE agent_tags_49 RENAME TO agent_tags;
		CREATE INDEX idx_agent_tags_tag ON agent_tags(tag_id,agent_id);
		INSERT INTO schema_migrations(version, applied_at) VALUES(49,?);
	`, time.Now().UnixMilli())
	return err
}
