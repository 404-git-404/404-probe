package storage

import (
	"context"
	"database/sql"
	"time"

	"404-probe/internal/protocol"
)

// Plan seeks one aggregate per series and partition. It seeks hour buckets
// only for partitions whose aggregate is missing or too stale. SQLite can
// otherwise end up walking every source hour to exclude already cached rows.
const networkFleetRollupSelect = `WITH RECURSIVE
	lanes AS MATERIALIZED (
		SELECT s.id,s.target_id,json_extract(t.payload,'$.slot') slot,
			json_extract(t.payload,'$.family') family,json_extract(t.payload,'$.protocol') protocol
		FROM quality_series s JOIN quality_targets t ON t.id=s.target_id
		WHERE s.agent_id=?1 AND s.semantics=?2
	), starts(at) AS (
		VALUES(?3) UNION ALL SELECT at+?4 FROM starts WHERE at+?4<?5
	), plan AS MATERIALIZED (
		SELECT l.id,l.slot,l.family,l.protocol,l.target_id,s.at,f.data,f.evening,f.last_start,f.last_data
		FROM lanes l CROSS JOIN starts s
		LEFT JOIN quality_fleet_dirty d ON d.series_id=l.id AND d.day_start=s.at/86400000*86400000
		LEFT JOIN quality_fleet_rollups f ON f.series_id=l.id AND f.resolution=?4 AND f.bucket_start=s.at
			AND (d.dirty_since IS NULL OR (d.dirty_since>?8 AND d.dirty_since<=?9))
		WHERE (?6='' OR l.family=?6) AND (?7='' OR l.protocol=?7)
	)
	SELECT slot,family,protocol,target_id,at,data,evening,last_start,last_data FROM plan WHERE data IS NOT NULL
	UNION ALL
	SELECT p.slot,p.family,p.protocol,p.target_id,b.bucket_start,b.data,NULL,b.bucket_start,NULL
	FROM plan p CROSS JOIN quality_buckets b ON b.series_id=p.id AND b.resolution=3600000
		AND b.bucket_start>=p.at AND b.bucket_start<p.at+?4
	WHERE p.data IS NULL
	ORDER BY at,target_id`

type networkFleetBucket struct {
	at, lastAt     int64
	whole, evening qualityBucketData
	lastWire       []byte
}

type networkFleetBucketReader struct {
	raw, rollup *sql.Stmt
	staleBefore int64
}

func (r networkFleetBucketReader) scanRaw(ctx context.Context, id string, filter NetworkLine, span networkBucketSpan,
	consume func(NetworkLine, string, int64, qualityBucketData)) error {
	rows, err := r.raw.QueryContext(ctx, networkBucketArgs(id, filter, span.start, span.end, span.resolution)...)
	return scanNetworkBucketRows(rows, err, consume)
}

func (r networkFleetBucketReader) scan(ctx context.Context, id string, filter NetworkLine, span networkBucketSpan,
	consume func(NetworkLine, networkFleetBucket)) error {
	if span.resolution <= qualityHourMS {
		return r.scanRaw(ctx, id, filter, span, func(line NetworkLine, _ string, at int64, b qualityBucketData) {
			row := networkFleetBucket{at: at, lastAt: at, whole: b}
			if networkEvening(at) {
				row.evening = b
			}
			consume(line, row)
		})
	}
	rows, err := r.rollup.QueryContext(ctx, id, protocol.QualityStagedSemantics, span.start, span.resolution, span.end,
		filter.Family, filter.Protocol, r.staleBefore, r.staleBefore+networkFleetMaxStale.Milliseconds())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row networkFleetBucket
		var line NetworkLine
		var target string
		var whole, evening []byte
		if err := rows.Scan(&line.Slot, &line.Family, &line.Protocol, &target, &row.at, &whole, &evening, &row.lastAt, &row.lastWire); err != nil {
			return err
		}
		row.whole, err = qualityDecodeBucket(whole)
		if err != nil {
			return err
		}
		if len(evening) > 0 {
			row.evening, err = qualityDecodeBucket(evening)
			if err != nil {
				return err
			}
		} else if (row.lastWire == nil || span.resolution == networkFleetShortMS) && networkEvening(row.at) {
			row.evening = row.whole
		}
		consume(line, row)
	}
	return rows.Err()
}

func networkFleetStaleBefore() int64 {
	// Dirty marks use SQLite's wall clock. The requested window can use any
	// valid historic end time and therefore cannot supply this cutoff.
	return time.Now().Add(-networkFleetMaxStale).UnixMilli()
}
