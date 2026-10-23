package storage

import (
	"context"
	"database/sql"
)

// migrateTCPMiniTx is the schema 29 migration. Schema 35 drops both tables again (migrateV107Tx), but a
// new database and any database below schema 29 still walk the whole chain 1 -> 35 in order, so the
// historical step must stay. Nothing else in the code base refers to tcp_mini_* any more.
func migrateTCPMiniTx(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE tcp_mini_state(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,evidence_start INTEGER NOT NULL,scanned_at INTEGER NOT NULL DEFAULT 0,initialized INTEGER NOT NULL DEFAULT 0,last_high INTEGER NOT NULL DEFAULT 0,last_low INTEGER NOT NULL DEFAULT 0,revision INTEGER NOT NULL DEFAULT 0,backfill_minute INTEGER NOT NULL DEFAULT 0,backfill_series INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE tcp_mini_high(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,minute INTEGER NOT NULL,peak_ms REAL NOT NULL CHECK(peak_ms>2000 AND peak_ms<=6000),source TEXT NOT NULL,PRIMARY KEY(agent_id,minute)) WITHOUT ROWID`,
		`INSERT INTO tcp_mini_state(agent_id,evidence_start) SELECT id,unixepoch()*1000 FROM agents`,
		`INSERT INTO schema_migrations(version,applied_at) VALUES(29,unixepoch())`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
