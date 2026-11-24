package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"404-probe/internal/protocol"
)

func migrateNetworkTCPLoss46Tx(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE quality_alert_history ADD COLUMN reason TEXT NOT NULL DEFAULT 'latency_failure'`,
		`ALTER TABLE quality_alert_history ADD COLUMN worst_loss REAL`,
		`DROP TRIGGER quality_fleet_hour_insert`,
		`DROP TRIGGER quality_fleet_hour_update`,
		`DROP TRIGGER quality_fleet_hour_delete`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for _, event := range []struct{ name, action, row string }{
		{"quality_fleet_hour_insert", "INSERT", "NEW"},
		{"quality_fleet_hour_update", "UPDATE OF data", "NEW"},
		{"quality_fleet_hour_delete", "DELETE", "OLD"},
	} {
		stmt := fmt.Sprintf(`CREATE TRIGGER %s AFTER %s ON quality_buckets
			WHEN %s.resolution=3600000 AND EXISTS (
				SELECT 1 FROM quality_series WHERE id=%s.series_id AND semantics IN ('%s','%s')
			) BEGIN
				INSERT INTO quality_fleet_dirty(series_id,day_start)
				VALUES(%s.series_id,%s.bucket_start/%d*%d) ON CONFLICT DO NOTHING;
			END`, event.name, event.action, event.row, event.row, protocol.QualityStagedSemantics, protocol.QualityTCPLossSemantics,
			event.row, event.row, networkFleetDayMS, networkFleetDayMS)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(46,?)`, time.Now().UnixMilli())
	return err
}
