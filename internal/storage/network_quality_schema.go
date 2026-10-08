package storage

import (
	"404-probe/internal/protocol"
	"context"
	"database/sql"
	"fmt"
	"time"
)

const QualityRetention = 48 * time.Hour
const qualityRawDevice = 45000
const qualityRawGlobal = 4500000
const qualityMinuteDevice = 18000
const qualityMinuteGlobal = 1800000
const qualityTargetDevice = 512
const qualityTargetGlobal = 51200
const qualityIntervalDevice = 4096

func migrateQualityTx(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE quality_config(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,revision INTEGER NOT NULL,enabled INTEGER NOT NULL,ipv6 INTEGER NOT NULL,choices TEXT NOT NULL,updated_at INTEGER NOT NULL)`,
		`CREATE TABLE quality_capability(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,epoch INTEGER NOT NULL,session_id TEXT NOT NULL,supported INTEGER NOT NULL)`,
		`CREATE TABLE quality_targets(id TEXT PRIMARY KEY,agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,fingerprint TEXT NOT NULL,payload TEXT NOT NULL,created_at INTEGER NOT NULL,retired_at INTEGER,UNIQUE(agent_id,fingerprint))`,
		`CREATE INDEX quality_target_agent ON quality_targets(agent_id,id)`,
		`CREATE INDEX quality_target_retirement ON quality_targets(retired_at,id)`,
		`CREATE TABLE quality_slots(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,slot TEXT NOT NULL,family TEXT NOT NULL,revision INTEGER NOT NULL,target_id TEXT REFERENCES quality_targets(id),PRIMARY KEY(agent_id,slot,family))`,
		`CREATE TABLE quality_intervals(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,slot TEXT NOT NULL,family TEXT NOT NULL,revision INTEGER NOT NULL,target_id TEXT NOT NULL REFERENCES quality_targets(id),start_at INTEGER NOT NULL,end_at INTEGER,config_from INTEGER NOT NULL,config_until INTEGER,PRIMARY KEY(agent_id,slot,family,revision))`,
		`CREATE INDEX quality_interval_retention ON quality_intervals(end_at,agent_id,revision)`,
		`CREATE INDEX quality_interval_target ON quality_intervals(agent_id,target_id,revision,start_at)`,
		`CREATE INDEX quality_interval_target_id ON quality_intervals(target_id)`,
		`CREATE TABLE quality_raw(id INTEGER PRIMARY KEY,agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,target_id TEXT NOT NULL REFERENCES quality_targets(id),epoch INTEGER NOT NULL,session_id TEXT NOT NULL,seq INTEGER NOT NULL,slot_revision INTEGER NOT NULL,config_revision INTEGER NOT NULL,sample_time INTEGER NOT NULL,received_at INTEGER NOT NULL,scheduled_at INTEGER NOT NULL,finished_at INTEGER NOT NULL,duration_ms REAL NOT NULL,outcome TEXT NOT NULL,resolved_ip TEXT NOT NULL,raw_sent INTEGER NOT NULL,raw_received INTEGER NOT NULL,content_hash BLOB NOT NULL CHECK(length(content_hash)=32),attempt INTEGER NOT NULL,success INTEGER NOT NULL,latency REAL,packet_sent INTEGER NOT NULL,packet_received INTEGER NOT NULL,UNIQUE(agent_id,epoch,session_id,seq))`,
		`CREATE INDEX quality_raw_target_time ON quality_raw(agent_id,target_id,sample_time,seq)`,
		`CREATE INDEX quality_raw_retention ON quality_raw(sample_time,id)`,
		`CREATE INDEX quality_raw_target ON quality_raw(target_id)`,
		`CREATE TABLE quality_minute(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,target_id TEXT NOT NULL REFERENCES quality_targets(id),bucket INTEGER NOT NULL,count INTEGER NOT NULL,attempts INTEGER NOT NULL,successes INTEGER NOT NULL,latency_sum REAL NOT NULL,sent INTEGER NOT NULL,received INTEGER NOT NULL,PRIMARY KEY(target_id,bucket))`,
		`CREATE INDEX quality_minute_retention ON quality_minute(bucket,target_id)`,
		`CREATE INDEX quality_minute_agent ON quality_minute(agent_id,bucket)`,
		`CREATE INDEX quality_slot_target ON quality_slots(target_id)`,
		`CREATE TABLE quality_gaps(id INTEGER PRIMARY KEY,agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,epoch INTEGER NOT NULL,session_id TEXT NOT NULL,start_at INTEGER NOT NULL,end_at INTEGER NOT NULL,reason TEXT NOT NULL,dropped INTEGER,UNIQUE(agent_id,epoch,session_id,start_at,end_at,reason))`,
		`CREATE INDEX quality_gap_retention ON quality_gaps(end_at,id)`,
		`CREATE INDEX quality_gap_agent ON quality_gaps(agent_id,start_at)`,
		`CREATE TABLE quality_usage(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,raw INTEGER NOT NULL DEFAULT 0,minute INTEGER NOT NULL DEFAULT 0,targets INTEGER NOT NULL DEFAULT 0,intervals INTEGER NOT NULL DEFAULT 0,gaps INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE quality_totals(kind TEXT PRIMARY KEY,value INTEGER NOT NULL DEFAULT 0)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for _, item := range []struct{ table, field string }{{"raw", "raw"}, {"minute", "minute"}, {"targets", "targets"}, {"intervals", "intervals"}, {"gaps", "gaps"}} {
		for _, q := range []string{
			fmt.Sprintf(`INSERT INTO quality_totals(kind,value) VALUES('%s',0)`, item.field),
			fmt.Sprintf(`CREATE TRIGGER quality_%s_insert AFTER INSERT ON quality_%s BEGIN INSERT OR IGNORE INTO quality_usage(agent_id) VALUES(NEW.agent_id); UPDATE quality_usage SET %s=%s+1 WHERE agent_id=NEW.agent_id; UPDATE quality_totals SET value=value+1 WHERE kind='%s'; END`, item.table, item.table, item.field, item.field, item.field),
			fmt.Sprintf(`CREATE TRIGGER quality_%s_delete AFTER DELETE ON quality_%s BEGIN UPDATE quality_usage SET %s=%s-1 WHERE agent_id=OLD.agent_id; UPDATE quality_totals SET value=value-1 WHERE kind='%s'; END`, item.table, item.table, item.field, item.field, item.field),
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(23,unixepoch())`)
	return err
}

func saveQualityCapabilityTx(ctx context.Context, tx *sql.Tx, r protocol.Report) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO quality_capability(agent_id,epoch,session_id,supported) VALUES(?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET epoch=excluded.epoch,session_id=excluded.session_id,supported=excluded.supported`, r.AgentID, int64(r.Epoch), r.SessionID, boolInt(r.NetworkQuality))
	return err
}
