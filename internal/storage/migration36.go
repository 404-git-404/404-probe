package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Test-only fault injection, never configured by production code.
var migration36FailAt func(string) error

func migrateOrganization36Tx(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE organization_meta(id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991))`,
		`CREATE TABLE groups(id TEXT NOT NULL PRIMARY KEY CHECK(length(id)=32 AND id NOT GLOB '*[^0-9a-f]*'),name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 40),name_key TEXT NOT NULL UNIQUE CHECK(length(name_key)>0),sort_order INTEGER NOT NULL CHECK(typeof(sort_order)='integer' AND sort_order>=0),revision INTEGER NOT NULL CHECK(revision>0))`,
		`CREATE TABLE tags(id TEXT NOT NULL PRIMARY KEY CHECK(length(id)=32 AND id NOT GLOB '*[^0-9a-f]*'),name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 20),name_key TEXT NOT NULL UNIQUE CHECK(length(name_key)>0),revision INTEGER NOT NULL CHECK(revision>0))`,
		`CREATE TABLE agent_organization(agent_id TEXT NOT NULL PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,group_id TEXT REFERENCES groups(id) ON DELETE SET NULL,revision INTEGER NOT NULL CHECK(revision>0))`,
		`CREATE INDEX idx_agent_organization_group ON agent_organization(group_id,agent_id)`,
		`CREATE TABLE agent_tags(agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,PRIMARY KEY(agent_id,tag_id))`,
		`CREATE INDEX idx_agent_tags_tag ON agent_tags(tag_id,agent_id)`,
		`CREATE TABLE expense_exchange_rates(currency TEXT NOT NULL PRIMARY KEY CHECK(length(currency)=3 AND currency NOT GLOB '*[^A-Z]*'),units_per_usd TEXT NOT NULL CHECK(typeof(units_per_usd)='text' AND length(units_per_usd) BETWEEN 1 AND 25 AND units_per_usd NOT GLOB '*[^0-9.]*' AND length(units_per_usd)-length(replace(units_per_usd,'.',''))<=1 AND substr(units_per_usd,1,1)<>'.' AND substr(units_per_usd,-1,1)<>'.' AND (substr(units_per_usd,1,1)<>'0' OR substr(units_per_usd,2,1)='.') AND (instr(units_per_usd,'.')=0 AND length(units_per_usd)<=12 OR instr(units_per_usd,'.') BETWEEN 2 AND 13 AND length(units_per_usd)-instr(units_per_usd,'.')<=12) AND replace(replace(units_per_usd,'.',''),'0','')<>'' AND (currency<>'USD' OR units_per_usd='1')),updated_at INTEGER NOT NULL CHECK(updated_at>=0),revision INTEGER NOT NULL CHECK(revision>0))`,
		`CREATE TRIGGER expense_usd_no_update BEFORE UPDATE ON expense_exchange_rates WHEN OLD.currency='USD' BEGIN SELECT RAISE(ABORT,'USD is fixed'); END`,
		`CREATE TRIGGER expense_usd_no_delete BEFORE DELETE ON expense_exchange_rates WHEN OLD.currency='USD' BEGIN SELECT RAISE(ABORT,'USD is fixed'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if migration36FailAt != nil {
		if err := migration36FailAt("tables"); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`INSERT INTO organization_meta(id,revision) VALUES(1,1)`,
		`INSERT INTO expense_exchange_rates(currency,units_per_usd,updated_at,revision) VALUES('USD','1',0,1)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if migration36FailAt != nil {
		if err := migration36FailAt("seed"); err != nil {
			return err
		}
	}
	for _, table := range []string{"agent_organization", "agent_tags"} {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
		if err != nil {
			return err
		}
		bad := rows.Next()
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if bad {
			return fmt.Errorf("migration 36 foreign key violation in %s", table)
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(36,unixepoch())`)
	return err
}
