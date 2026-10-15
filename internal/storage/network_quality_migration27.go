package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Only schema work at startup. Historic rows are backfilled in resumable batches.
func migrateQualityRollupTx(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE quality_series(id INTEGER PRIMARY KEY,agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,target_id TEXT NOT NULL REFERENCES quality_targets(id) ON DELETE CASCADE,semantics TEXT NOT NULL,UNIQUE(target_id,semantics))`,
		`CREATE INDEX quality_series_agent ON quality_series(agent_id,target_id)`,
		`CREATE TABLE quality_sessions(id INTEGER PRIMARY KEY,agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,epoch INTEGER NOT NULL,session_id TEXT NOT NULL,UNIQUE(agent_id,epoch,session_id))`,
		`CREATE TABLE quality_receipts(session_key INTEGER NOT NULL REFERENCES quality_sessions(id) ON DELETE CASCADE,seq INTEGER NOT NULL,series_id INTEGER NOT NULL REFERENCES quality_series(id) ON DELETE CASCADE,sample_time INTEGER NOT NULL,config_revision INTEGER NOT NULL,slot_revision INTEGER NOT NULL,content_hash BLOB NOT NULL CHECK(length(content_hash)=32),canonical_length INTEGER NOT NULL CHECK(canonical_length>0),PRIMARY KEY(session_key,seq)) WITHOUT ROWID`,
		`CREATE INDEX quality_receipt_retention ON quality_receipts(sample_time,session_key,seq)`,
		`CREATE INDEX quality_receipt_series ON quality_receipts(series_id)`,
		`CREATE TABLE quality_retired(session_key INTEGER PRIMARY KEY REFERENCES quality_sessions(id) ON DELETE CASCADE,seq INTEGER NOT NULL,sample_time INTEGER NOT NULL)`,
		`INSERT OR IGNORE INTO quality_sessions(agent_id,epoch,session_id) SELECT agent_id,epoch,session_id FROM quality_gaps WHERE reason LIKE 'storage_pruned:%'`,
		`INSERT INTO quality_retired SELECT s.id,MAX(CAST(SUBSTR(g.reason,16) AS INTEGER)),MAX(g.end_at) FROM quality_gaps g JOIN quality_sessions s ON s.agent_id=g.agent_id AND s.epoch=g.epoch AND s.session_id=g.session_id WHERE g.reason LIKE 'storage_pruned:%' GROUP BY s.id`,
		`CREATE TABLE quality_buckets(series_id INTEGER NOT NULL REFERENCES quality_series(id) ON DELETE CASCADE,resolution INTEGER NOT NULL CHECK(resolution IN (60000,300000,3600000)),bucket_start INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(series_id,resolution,bucket_start)) WITHOUT ROWID`,
		`CREATE INDEX quality_bucket_retention ON quality_buckets(resolution,bucket_start,series_id)`,
		`CREATE TABLE quality_facts(id INTEGER PRIMARY KEY AUTOINCREMENT,raw_id INTEGER NOT NULL,target_id TEXT NOT NULL REFERENCES quality_targets(id) ON DELETE CASCADE,series_id INTEGER NOT NULL REFERENCES quality_series(id) ON DELETE CASCADE,session_key INTEGER NOT NULL REFERENCES quality_sessions(id) ON DELETE CASCADE,seq INTEGER NOT NULL,sample_time INTEGER NOT NULL,config_revision INTEGER NOT NULL,slot_revision INTEGER NOT NULL,resolved_ip TEXT NOT NULL,eligible INTEGER NOT NULL,rtt REAL,interval_ms INTEGER NOT NULL)`,
		`CREATE INDEX quality_fact_neighbor ON quality_facts(target_id,sample_time,seq,id)`,
		`CREATE INDEX quality_fact_series ON quality_facts(series_id)`,
		`CREATE INDEX quality_fact_session ON quality_facts(session_key)`,
		`CREATE INDEX quality_fact_retention ON quality_facts(sample_time,id)`,
		`CREATE INDEX quality_fact_raw ON quality_facts(raw_id)`,
		`CREATE TABLE quality_backfill(id INTEGER PRIMARY KEY CHECK(id=1),last_raw_id INTEGER NOT NULL,complete INTEGER NOT NULL CHECK(complete IN (0,1)))`,
		`INSERT INTO quality_backfill VALUES(1,0,NOT EXISTS(SELECT 1 FROM quality_raw))`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(27,unixepoch())`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for _, name := range []string{"receipts", "buckets", "facts", "series", "sessions"} {
		for _, q := range []string{
			fmt.Sprintf(`INSERT INTO quality_totals(kind,value) VALUES('%s',(SELECT COUNT(*) FROM quality_%s))`, name, name),
			fmt.Sprintf(`CREATE TRIGGER quality_%s_total_insert AFTER INSERT ON quality_%s BEGIN UPDATE quality_totals SET value=value+1 WHERE kind='%s'; END`, name, name, name),
			fmt.Sprintf(`CREATE TRIGGER quality_%s_total_delete AFTER DELETE ON quality_%s BEGIN UPDATE quality_totals SET value=value-1 WHERE kind='%s'; END`, name, name, name),
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
	}
	return nil
}
