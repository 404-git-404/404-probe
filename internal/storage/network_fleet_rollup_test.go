package storage

import (
	"reflect"
	"testing"
	"time"
)

func TestFleetRollupsMatchRawAndExpireStaleData(t *testing.T) {
	c := newCardKit(t)
	series := c.series("telecom", "ipv4", "tcp")
	now := time.Date(2027, 1, 18, 12, 7, 0, 0, time.UTC)
	day := now.Truncate(24 * time.Hour).Add(-48 * time.Hour)
	putHour := func(at time.Time, fast, slow, failed int) {
		t.Helper()
		b := c.tcp(fast, slow, failed, 900)
		b.Stats.Categories["success"] = fast + slow
		b.Stats.Categories["timeout"] = failed
		wire, err := qualityEncodeBucket(b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.s.db.Exec(`INSERT INTO quality_buckets VALUES(?,?,?,?)
			ON CONFLICT(series_id,resolution,bucket_start) DO UPDATE SET data=excluded.data`,
			series, qualityHourMS, at.UnixMilli(), wire); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 24 {
		putHour(day.Add(time.Duration(i)*time.Hour), i+1, 24-i, i%3)
	}
	// A copied schema 42 database has source buckets but no derived rows.
	if _, err := c.s.db.Exec(`DROP TRIGGER quality_fleet_hour_insert; DROP TRIGGER quality_fleet_hour_update;
		DROP TRIGGER quality_fleet_hour_delete; DROP TABLE quality_fleet_dirty; DROP TABLE quality_fleet_rollups;
		DELETE FROM schema_migrations WHERE version>=43`); err != nil {
		t.Fatal(err)
	}
	if err := c.s.migrate(c.ctx); err != nil {
		t.Fatal(err)
	}
	load := func(start, end time.Time) NetworkFleetLine {
		t.Helper()
		out, err := c.s.NetworkFleet(c.ctx, c.id, "ipv4", "tcp", start.UnixMilli(), end.UnixMilli(), now, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return out.Machines[0].Lines[0]
	}
	wideStart := now.Add(-30 * 24 * time.Hour)
	shortStart, shortEnd := day, day.Add(24*time.Hour)
	wantWide, wantShort := load(wideStart, now), load(shortStart, shortEnd)
	refresh := func() {
		t.Helper()
		if n, err := c.s.RefreshNetworkFleet(c.ctx); err != nil || n != 1 {
			t.Fatalf("refresh: days=%d err=%v", n, err)
		}
	}
	refresh()
	if got := load(wideStart, now); !reflect.DeepEqual(got, wantWide) || got.EveningP50 == nil || got.LatestBucketAt == nil {
		t.Fatalf("daily fleet disagrees with source: stats=%+v want=%+v", got.Stats, wantWide.Stats)
	}
	if got := load(shortStart, shortEnd); !reflect.DeepEqual(got, wantShort) {
		t.Fatalf("three-hour fleet disagrees with source: stats=%+v want=%+v", got.Stats, wantShort.Stats)
	}
	putHour(day.Add(13*time.Hour), 100, 0, 0)
	old := load(wideStart, now)
	if old.Stats.Samples != wantWide.Stats.Samples {
		t.Fatalf("recent dirty day cache sample count=%d want=%d", old.Stats.Samples, wantWide.Stats.Samples)
	}
	if _, err := c.s.db.Exec(`UPDATE quality_fleet_dirty SET dirty_since=?`, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	fallback := load(wideStart, now)
	if fallback.Stats.Samples <= old.Stats.Samples {
		t.Fatalf("stale day did not fall back to updated hourly data: old=%d new=%d", old.Stats.Samples, fallback.Stats.Samples)
	}
	refresh()
	if got := load(wideStart, now); !reflect.DeepEqual(got, fallback) {
		t.Fatalf("refreshed daily fleet differs from fallback: %+v", got.Stats)
	}
	if _, err := c.s.db.Exec(`DELETE FROM quality_buckets WHERE series_id=?`, series); err != nil {
		t.Fatal(err)
	}
	refresh()
	got := load(wideStart, now)
	if got.Stats.Samples != 0 || got.Stats.P50 != nil || got.EveningP50 != nil || got.LatestBucketAt != nil {
		t.Fatalf("expired source still visible: %+v", got.Stats)
	}

	putHour(day, 1, 1, 0)
	refresh()
	putHour(day.Add(time.Hour), 1, 0, 0)
	if _, err := c.s.db.Exec(`DELETE FROM agents WHERE id=?`, c.id); err != nil {
		t.Fatal(err)
	}
	var leftovers int
	if err := c.s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM quality_fleet_rollups) + (SELECT COUNT(*) FROM quality_fleet_dirty)`).Scan(&leftovers); err != nil || leftovers != 0 {
		t.Fatalf("Agent deletion left %d derived rows: %v", leftovers, err)
	}
}
