package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"404-probe/internal/protocol"
)

const (
	networkFleetShortMS      = 3 * qualityHourMS
	networkFleetDayMS        = 24 * qualityHourMS
	networkFleetRefreshLimit = 192
	networkFleetMaxStale     = 30 * time.Second
)

func migrateNetworkFleet43Tx(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE quality_fleet_rollups (
			series_id INTEGER NOT NULL REFERENCES quality_series(id) ON DELETE CASCADE,
			resolution INTEGER NOT NULL CHECK(resolution IN (10800000,86400000)),
			bucket_start INTEGER NOT NULL,data BLOB NOT NULL,evening BLOB,
			last_start INTEGER NOT NULL,last_data BLOB NOT NULL,
			PRIMARY KEY(series_id,resolution,bucket_start)
		) WITHOUT ROWID`,
		`CREATE TABLE quality_fleet_dirty (
			series_id INTEGER NOT NULL REFERENCES quality_series(id) ON DELETE CASCADE,
			day_start INTEGER NOT NULL,
			dirty_since INTEGER NOT NULL DEFAULT (CAST(unixepoch('subsec')*1000 AS INTEGER)),
			PRIMARY KEY(series_id,day_start)
		) WITHOUT ROWID`,
		`CREATE INDEX quality_fleet_dirty_order ON quality_fleet_dirty(dirty_since,series_id,day_start)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quality_fleet_dirty(series_id,day_start)
		SELECT DISTINCT b.series_id,b.bucket_start/86400000*86400000 FROM quality_series s
		CROSS JOIN quality_buckets b ON b.series_id=s.id AND b.resolution=3600000
		WHERE s.semantics=?`, protocol.QualityStagedSemantics); err != nil {
		return err
	}
	for _, event := range []struct{ name, action, row string }{
		{"quality_fleet_hour_insert", "INSERT", "NEW"},
		{"quality_fleet_hour_update", "UPDATE OF data", "NEW"},
		{"quality_fleet_hour_delete", "DELETE", "OLD"},
	} {
		stmt := fmt.Sprintf(`CREATE TRIGGER %s AFTER %s ON quality_buckets
			WHEN %s.resolution=3600000 AND EXISTS (
				SELECT 1 FROM quality_series WHERE id=%s.series_id AND semantics='%s'
			) BEGIN
				INSERT INTO quality_fleet_dirty(series_id,day_start)
				VALUES(%s.series_id,%s.bucket_start/%d*%d) ON CONFLICT DO NOTHING;
			END`, event.name, event.action, event.row, event.row, protocol.QualityStagedSemantics,
			event.row, event.row, networkFleetDayMS, networkFleetDayMS)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(43,?)`, time.Now().UnixMilli())
	return err
}

type networkFleetRollup struct {
	whole, evening qualityBucketData
	lastAt         int64
	lastWire       []byte
}

func (r *networkFleetRollup) add(at int64, bucket qualityBucketData, wire []byte, daily bool) {
	if r.whole.Histogram == nil {
		r.whole = qualityNewBucket()
	}
	qualityMergeBucket(&r.whole, bucket)
	if daily && networkEvening(at) {
		if r.evening.Histogram == nil {
			r.evening = qualityNewBucket()
		}
		qualityMergeBucket(&r.evening, bucket)
	}
	r.lastAt, r.lastWire = at, wire
}

type networkFleetDirtyDay struct {
	series, day int64
	rollups     [1 + networkFleetDayMS/networkFleetShortMS]networkFleetRollup
}

// RefreshNetworkFleet drains dirty series-days in bounded writer transactions.
func (s *Store) RefreshNetworkFleet(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := s.refreshNetworkFleetBatch(ctx)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
}

// The materialized buckets and dirty-marker removal commit together.
func (s *Store) refreshNetworkFleetBatch(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	days, err := readNetworkFleetDirtyDays(ctx, tx)
	if err != nil || len(days) == 0 {
		return 0, err
	}
	prune, err := tx.PrepareContext(ctx, `DELETE FROM quality_fleet_rollups WHERE series_id=? AND bucket_start>=? AND bucket_start<?`)
	if err != nil {
		return 0, err
	}
	defer prune.Close()
	save, err := tx.PrepareContext(ctx, `INSERT INTO quality_fleet_rollups(series_id,resolution,bucket_start,data,evening,last_start,last_data)
		VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer save.Close()
	done, err := tx.PrepareContext(ctx, `DELETE FROM quality_fleet_dirty WHERE series_id=? AND day_start=?`)
	if err != nil {
		return 0, err
	}
	defer done.Close()
	for _, day := range days {
		if _, err := prune.ExecContext(ctx, day.series, day.day, day.day+networkFleetDayMS); err != nil {
			return 0, err
		}
		for i, r := range day.rollups {
			if r.lastWire == nil {
				continue
			}
			width, start := networkFleetDayMS, day.day
			if i > 0 {
				width, start = networkFleetShortMS, day.day+int64(i-1)*networkFleetShortMS
			}
			wire, err := qualityEncodeBucket(r.whole)
			if err != nil {
				return 0, err
			}
			var evening []byte
			if r.evening.Stats.Count > 0 {
				evening, err = qualityEncodeBucket(r.evening)
				if err != nil {
					return 0, err
				}
			}
			if _, err := save.ExecContext(ctx, day.series, width, start, wire, evening, r.lastAt, r.lastWire); err != nil {
				return 0, err
			}
		}
		if _, err := done.ExecContext(ctx, day.series, day.day); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(days), nil
}

func readNetworkFleetDirtyDays(ctx context.Context, tx *sql.Tx) ([]*networkFleetDirtyDay, error) {
	rows, err := tx.QueryContext(ctx, `WITH queue AS MATERIALIZED (
		SELECT series_id,day_start FROM quality_fleet_dirty ORDER BY dirty_since,series_id,day_start LIMIT ?
	) SELECT q.series_id,q.day_start,b.bucket_start,b.data FROM queue q
	LEFT JOIN quality_buckets b ON b.series_id=q.series_id AND b.resolution=3600000
		AND b.bucket_start>=q.day_start AND b.bucket_start<q.day_start+86400000
	ORDER BY q.series_id,q.day_start,b.bucket_start`, networkFleetRefreshLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := make([]*networkFleetDirtyDay, 0, networkFleetRefreshLimit)
	var recent *networkFleetDirtyDay
	for rows.Next() {
		var series, day int64
		var at sql.NullInt64
		var wire []byte
		if err := rows.Scan(&series, &day, &at, &wire); err != nil {
			return nil, err
		}
		if recent == nil || recent.series != series || recent.day != day {
			recent = &networkFleetDirtyDay{series: series, day: day}
			days = append(days, recent)
		}
		if at.Valid {
			b, err := qualityDecodeBucket(wire)
			if err != nil {
				return nil, err
			}
			recent.rollups[0].add(at.Int64, b, wire, true)
			recent.rollups[1+(at.Int64-day)/networkFleetShortMS].add(at.Int64, b, wire, false)
		}
	}
	return days, rows.Err()
}
