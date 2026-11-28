package storage

import (
	"context"
	"database/sql"
	"hash/fnv"
	"slices"
	"strings"
	"time"
)

var tagColors = [...]string{"blue", "sky", "cyan", "teal", "indigo", "purple", "pink", "gray"}

func validTagColor(color string) bool {
	return slices.Contains(tagColors[:], color)
}

func defaultTagColor(nameKey string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(nameKey))
	return tagColors[hash.Sum32()%uint32(len(tagColors))]
}

func migrateSystemCheckIgnoresAndTagColors47Tx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE agent_system_check_ignores (
		agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		kind TEXT NOT NULL CHECK(kind IN ('service', 'service_restart', 'certificate', 'time')),
		item TEXT NOT NULL CHECK(length(CAST(item AS BLOB)) BETWEEN 1 AND 512),
		created_at INTEGER NOT NULL,
		PRIMARY KEY (agent_id, kind, item)
	) WITHOUT ROWID`); err != nil {
		return err
	}
	colorValues := "'" + strings.Join(tagColors[:], "','") + "'"
	if _, err := tx.ExecContext(ctx, `ALTER TABLE tags ADD COLUMN color TEXT NOT NULL DEFAULT 'blue'
		CHECK(color IN (`+colorValues+`))`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT name_key FROM tags`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var nameKeys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		nameKeys = append(nameKeys, key)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, key := range nameKeys {
		if _, err := tx.ExecContext(ctx, `UPDATE tags SET color=? WHERE name_key=?`, defaultTagColor(key), key); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(47, ?)`, time.Now().UnixMilli())
	return err
}
