package storage

import (
	"context"
	"database/sql"
)

func migrateNetworkStatus42Tx(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		// Keep the old columns for existing databases. New reads use the nullable observed values.
		`ALTER TABLE agent_state ADD COLUMN observed_rx_rate REAL`,
		`ALTER TABLE agent_state ADD COLUMN observed_tx_rate REAL`,
		`UPDATE agent_state SET observed_rx_rate=rx_rate,observed_tx_rate=tx_rate`,
		`ALTER TABLE minute_metrics ADD COLUMN rate_samples INTEGER`,
		`CREATE TABLE quality_clock_estimates(
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			epoch INTEGER NOT NULL,session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,sample_at INTEGER NOT NULL,received_at INTEGER NOT NULL
		)`,
		`CREATE TABLE quality_target_history(
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			slot TEXT NOT NULL,family TEXT NOT NULL,protocol TEXT NOT NULL,revision INTEGER NOT NULL,
			target_id TEXT NOT NULL,start_at INTEGER NOT NULL,end_at INTEGER,
			PRIMARY KEY(agent_id,slot,family,protocol,revision)
		)`,
		`CREATE INDEX quality_target_history_expiry ON quality_target_history(end_at) WHERE end_at IS NOT NULL`,
		`INSERT INTO quality_target_history SELECT agent_id,slot,family,protocol,revision,target_id,start_at,end_at FROM quality_intervals`,
		`CREATE TRIGGER quality_target_history_insert AFTER INSERT ON quality_intervals BEGIN
			INSERT INTO quality_target_history VALUES(NEW.agent_id,NEW.slot,NEW.family,NEW.protocol,NEW.revision,NEW.target_id,NEW.start_at,NEW.end_at);
		END`,
		`CREATE TABLE quality_alert_history(
			id INTEGER PRIMARY KEY,agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			slot TEXT NOT NULL,family TEXT NOT NULL,protocol TEXT NOT NULL,target_id TEXT NOT NULL,revision INTEGER NOT NULL,
			start_at INTEGER NOT NULL,end_at INTEGER,worst_p95 REAL,worst_failure REAL
		)`,
		`CREATE UNIQUE INDEX quality_alert_open ON quality_alert_history(agent_id,slot,family,protocol) WHERE end_at IS NULL`,
		`CREATE INDEX quality_alert_agent ON quality_alert_history(agent_id,start_at)`,
		`CREATE INDEX quality_alert_expiry ON quality_alert_history(end_at) WHERE end_at IS NOT NULL`,
		`CREATE TABLE quality_alert_cursor(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,eval_end INTEGER NOT NULL)`,
		`CREATE TRIGGER quality_target_history_close AFTER UPDATE OF end_at ON quality_intervals WHEN NEW.end_at IS NOT NULL BEGIN
			UPDATE quality_target_history SET end_at=NEW.end_at WHERE agent_id=NEW.agent_id AND slot=NEW.slot AND family=NEW.family AND protocol=NEW.protocol AND revision=NEW.revision;
			UPDATE quality_alert_history SET end_at=MAX(start_at,NEW.end_at) WHERE agent_id=NEW.agent_id AND slot=NEW.slot AND family=NEW.family AND protocol=NEW.protocol AND revision=NEW.revision AND end_at IS NULL;
		END`,
		`CREATE TABLE agent_presence_events(
			id INTEGER PRIMARY KEY,agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			at INTEGER NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('online','offline','paused','resumed'))
		)`,
		`CREATE INDEX agent_presence_timeline ON agent_presence_events(agent_id,at,id)`,
		`CREATE INDEX agent_presence_expiry ON agent_presence_events(at)`,
		`CREATE TRIGGER agent_presence_register AFTER INSERT ON agents BEGIN
			INSERT INTO agent_presence_events(agent_id,at,kind) VALUES(NEW.id,NEW.created_at,'offline');
		END`,
		`CREATE TABLE agent_presence_anchor(
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,kind TEXT NOT NULL
		)`,
		`CREATE TABLE agent_presence(
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			offline_at INTEGER NOT NULL
		)`,
		`CREATE INDEX agent_presence_deadlines ON agent_presence(offline_at)`,
		`CREATE TRIGGER agent_presence_pause AFTER UPDATE OF disabled_at,revoked ON agents
		WHEN (OLD.disabled_at IS NULL AND NEW.disabled_at IS NOT NULL) OR (OLD.revoked=0 AND NEW.revoked=1) BEGIN
			INSERT INTO agent_presence_events(agent_id,at,kind)
				SELECT NEW.id,MIN(offline_at,NEW.updated_at),'offline' FROM agent_presence
				WHERE agent_id=NEW.id AND (offline_at<NEW.updated_at OR NEW.revoked=1);
			DELETE FROM agent_presence WHERE agent_id=NEW.id;
			INSERT INTO agent_presence_events(agent_id,at,kind)
				SELECT NEW.id,NEW.disabled_at,'paused' WHERE OLD.disabled_at IS NULL AND NEW.disabled_at IS NOT NULL;
			UPDATE quality_alert_history SET end_at=MAX(start_at,NEW.updated_at) WHERE agent_id=NEW.id AND end_at IS NULL;
		END`,
		`CREATE TRIGGER agent_presence_resume AFTER UPDATE OF disabled_at ON agents
		WHEN OLD.disabled_at IS NOT NULL AND NEW.disabled_at IS NULL BEGIN
			INSERT INTO agent_presence_events(agent_id,at,kind) VALUES(NEW.id,NEW.updated_at,'resumed');
		END`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(42,strftime('%s','now') * 1000)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
