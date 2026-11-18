package storage

import (
	"context"
	"database/sql"
)

var migration38FailAt func(string) error // Test-only fault injection.

func migrateWebSettings38Tx(ctx context.Context, tx *sql.Tx) error {
	failAt := func(phase string) error {
		if migration38FailAt != nil {
			return migration38FailAt(phase)
		}
		return nil
	}
	var hasRenewalURL bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('agent_plans') WHERE name='renewal_url')`).Scan(&hasRenewalURL); err != nil {
		return err
	}
	if !hasRenewalURL {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_plans ADD COLUMN renewal_url TEXT CHECK(renewal_url IS NULL OR length(renewal_url)<=2048)`); err != nil {
			return err
		}
	}
	if err := failAt("renewal_url"); err != nil {
		return err
	}
	var hasRegions bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='web_youtube_ad_free_regions')`).Scan(&hasRegions); err != nil {
		return err
	}
	if !hasRegions {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE web_youtube_ad_free_regions (
			region TEXT PRIMARY KEY NOT NULL CHECK(region GLOB '[A-Z][A-Z]'),
			created_at INTEGER NOT NULL
		)`); err != nil {
			return err
		}
	}
	if err := failAt("regions_table"); err != nil {
		return err
	}
	// An existing table can be empty because the administrator saved an empty list.
	if !hasRegions {
		defaults, err := encodeYouTubeAdFreeRegions(DefaultYouTubeAdFreeRegions())
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO web_youtube_ad_free_regions(region,created_at)
			SELECT value,unixepoch() FROM json_each(?)`, defaults); err != nil {
			return err
		}
	}
	if err := failAt("regions_seed"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS web_admin_credential (
		id INTEGER PRIMARY KEY CHECK(id=1),
		password_hash TEXT NOT NULL,
		file_sha256 TEXT NOT NULL,
		revision INTEGER NOT NULL CHECK(revision>0),
		updated_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	if err := failAt("credential_table"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(38,unixepoch()) ON CONFLICT(version) DO NOTHING`); err != nil {
		return err
	}
	return failAt("schema_version")
}
