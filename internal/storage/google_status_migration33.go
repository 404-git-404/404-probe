package storage

import (
	"context"
	"database/sql"
)

// Migration 33 records, for a YouTube cn result only, whether the page itself said YouTube Premium
// is not available in the visitor's country (1), was read without that sentence (0), or was not
// observed (NULL: older rows, older Agents, results that are not cn). It is additive and nullable.
func migrateGoogleStatusPremiumNoticeTx(ctx context.Context, tx *sql.Tx) error {
	// Re-running over a database that already has the column (a rewound fixture or an interrupted
	// operator copy) must not fail.
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('agent_google_status') WHERE name='youtube_premium_notice'`).Scan(&present); err != nil {
		return err
	}
	statements := []string{`INSERT INTO schema_migrations(version,applied_at) VALUES(33,unixepoch())`}
	if present == 0 {
		statements = append([]string{`ALTER TABLE agent_google_status ADD COLUMN youtube_premium_notice INTEGER
			CHECK(youtube_premium_notice IS NULL OR youtube_premium_notice IN (0,1))`}, statements...)
	}
	for _, q := range statements {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
