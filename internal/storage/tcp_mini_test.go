package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"404-probe/internal/protocol"
)

func miniFixture(t *testing.T, path string) (*Store, string, protocol.QualityConfig, time.Time) {
	t.Helper()
	s, id, old, _ := qualityFixture(t, path)
	now := time.Date(2026, 10, 16, 20, 30, 0, 0, time.UTC)
	caps := a1Capabilities()
	caps.Semantics = protocol.QualityStagedSemantics
	a1Report(t, s, id, 2, now, caps, "debian 13")
	cfg, err := s.UpdateQualityConfigV2(context.Background(), id, a1LaneConfig(old.Revision), qualityTestResolver, now.Add(-6*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT OR REPLACE INTO tcp_mini_state(agent_id,evidence_start,scanned_at,initialized) VALUES(?,?,?,1)`, id, now.Add(-48*time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return s, id, cfg, now
}
func miniSave(t *testing.T, s *Store, id string, c protocol.QualityConfig, target protocol.QualityTarget, seq int64, at, received time.Time, ms float64, outcome string) {
	t.Helper()
	sample := a2TCP(target, c.Revision, seq, at, ms, outcome)
	if ms > sample.DurationMS {
		sample.DurationMS = math.Ceil(ms)
		sample.FinishedAt = sample.StartedAt + int64(math.Ceil(ms))
	}
	ack, err := s.SaveQualityBatch(context.Background(), id, protocol.QualityBatch{Version: 2, Epoch: "1", SessionID: "quality_session", Samples: []protocol.QualitySample{sample}}, received)
	if err != nil || len(ack.Results) != 1 || ack.Results[0][2] != "committed" {
		t.Fatal("save", ack, err)
	}
}
func miniRead(t *testing.T, s *Store, id, zone string, now time.Time) TCPMiniStatus {
	t.Helper()
	if err := s.MaintainTCPMini(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	o, err := s.TCPMini(context.Background(), id, zone, now)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func TestTCPMiniThresholdsPersistedP95(t *testing.T) {
	for _, v := range []float64{0, 1999, 2000, 2001, 4700, 5999.75} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			s, id, c, now := miniFixture(t, ":memory:")
			target := a1Target(t, c, "mobile", "ipv4", "tcp")
			miniSave(t, s, id, c, target, 1, now.Add(-time.Minute), now, v, "success")
			o := miniRead(t, s, id, "America/Los_Angeles", now)
			want := "normal"
			if v == 2000 {
				want = "unknown"
			}
			if v > 2000 {
				want = "alert"
			}
			if o.State != want || o.Level != tcpMiniLevel(v) {
				t.Fatalf("%+v", o)
			}
			h, err := s.QualityHistoryRollup(context.Background(), id, target.ID, "1day", now)
			if err != nil || len(h.Groups) != 1 {
				t.Fatal(h, err)
			}
			p := h.Groups[0].Points[1438]
			if p.P95 == nil || math.Abs(*p.P95-v) > 1e-8 {
				t.Fatal("real SQLite minute", p)
			}
			t.Logf("RTT=%g status=%+v", v, o)
		})
	}
}
func TestTCPMiniSameDayRevisionMidnightAndLate(t *testing.T) {
	s, id, c, now := miniFixture(t, ":memory:")
	a := a1Target(t, c, "telecom", "ipv4", "tcp")
	b := a1Target(t, c, "unicom", "ipv4", "tcp")
	miniSave(t, s, id, c, a, 1, now.Add(-time.Minute), now, 4700, "success")
	miniSave(t, s, id, c, a, 2, now.Add(-50*time.Second), now, 100, "success")
	miniSave(t, s, id, c, b, 3, now.Add(-2*time.Minute), now, 100, "success")
	if o := miniRead(t, s, id, "UTC", now); o.State != "alert" || o.Peak != 4700 {
		t.Fatal(o)
	}
	// Advance Server maintenance while the browser can remain closed indefinitely.
	next := time.Date(2026, 10, 17, 0, 10, 0, 0, time.UTC)
	if o := miniRead(t, s, id, "UTC", next); o.State != "alert" {
		t.Fatal("midnight no data", o)
	}
	miniSave(t, s, id, c, b, 4, next.Add(-time.Minute), next, 2000, "success")
	if o := miniRead(t, s, id, "UTC", next); o.State != "alert" {
		t.Fatal("equal cannot clear", o)
	}
	miniSave(t, s, id, c, b, 5, next.Add(-2*time.Minute), next, 0, "success")
	if o := miniRead(t, s, id, "UTC", next); o.State != "normal" || o.Level != 2000 {
		t.Fatal("zero clears", o)
	}
	// Yesterday's legal late high raises yesterday's stored peak, not today's alert.
	miniSave(t, s, id, c, a, 6, time.Date(2026, 10, 16, 23, 50, 0, 0, time.UTC), next, 5900, "success")
	if o := miniRead(t, s, id, "UTC", next); o.State != "normal" {
		t.Fatal("late yesterday high", o)
	}
	miniSave(t, s, id, c, a, 7, next.Add(-3*time.Minute), next, 2001, "success")
	if o := miniRead(t, s, id, "UTC", next); o.State != "alert" || o.Peak != 2001 || o.Level != 5000 {
		t.Fatal("today locks without yesterday peak", o)
	}
}
func TestTCPMiniFutureMaturityRestartReadOnlyAndRetention(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mini.db")
	s, id, c, now := miniFixture(t, file)
	target := a1Target(t, c, "mobile", "ipv4", "tcp")
	miniSave(t, s, id, c, target, 1, now.Add(20*time.Second), now, 4700, "success")
	if o := miniRead(t, s, id, "UTC", now); o.LastHigh != 0 {
		t.Fatal("future triggered", o)
	}
	if o, e := s.TCPMini(context.Background(), id, "UTC", now.Add(time.Minute)); e != nil || o.State != "unknown" {
		t.Fatal("unprocessed future must not certify state", o, e)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if o := miniRead(t, s, id, "UTC", now.Add(time.Minute)); o.State != "alert" || o.Peak != 4700 {
		t.Fatal("restart maturity", o)
	}
	if o, e := s.TCPMini(context.Background(), id, "UTC", now.Add(120*time.Hour)); e != nil || o.State != "alert" {
		t.Fatal("idle time must not expire evidence", o, e)
	}
	var before int64
	s.db.QueryRow(`SELECT total_changes()`).Scan(&before)
	for i := 0; i < 3; i++ {
		if _, err = s.TCPMini(context.Background(), id, "UTC", now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var after int64
	s.db.QueryRow(`SELECT total_changes()`).Scan(&after)
	if before != after {
		t.Fatal("GET wrote", before, after)
	}
	for step := 1; step <= 10; step++ {
		at := now.Add(time.Duration(step) * 12 * time.Hour)
		if _, _, err = s.CleanupQualityBatch(context.Background(), at); err != nil {
			t.Fatal(err)
		}
	}
	if o := miniRead(t, s, id, "UTC", now.Add(120*time.Hour)); o.State != "alert" || o.Peak != 4700 {
		t.Fatal("old alert expired", o)
	}
	var rows int
	s.db.QueryRow(`SELECT count(*) FROM tcp_mini_high WHERE agent_id=?`, id).Scan(&rows)
	if rows != 1 {
		t.Fatal(rows)
	}
}
func TestTCPMiniValidityFailuresMixedAndTargetSwitch(t *testing.T) {
	s, id, c, now := miniFixture(t, ":memory:")
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	miniSave(t, s, id, c, target, 1, now.Add(-time.Minute), now, 0, "timeout")
	if o := miniRead(t, s, id, "UTC", now); o.LastLow != 0 || o.LastHigh != 0 {
		t.Fatal("failure became value", o)
	}
	miniSave(t, s, id, c, target, 2, now.Add(-2*time.Minute), now, 4700, "success")
	u := a1LaneConfig(c.Revision)
	for i := range u.Lanes {
		u.Lanes[i].Host = "127.0.0.2"
	}
	next, err := s.UpdateQualityConfigV2(context.Background(), id, u, qualityTestResolver, now)
	if err != nil {
		t.Fatal(err)
	}
	if o := miniRead(t, s, id, "UTC", now); o.State != "alert" {
		t.Fatal("configuration cleared VPS", o)
	}
	miniSave(t, s, id, next, a1Target(t, next, "mobile", "ipv4", "tcp"), 3, now.Add(time.Minute), now.Add(2*time.Minute), 100, "success")
	if o := miniRead(t, s, id, "UTC", now.Add(2*time.Minute)); o.State != "alert" {
		t.Fatal("same-day replacement low", o)
	}
	// Real identity-mixed minute remains excluded on first observation.
	if _, err = s.db.Exec(`UPDATE tcp_mini_state SET last_high=0,last_low=0; DELETE FROM tcp_mini_high`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE quality_buckets SET data=data WHERE resolution=60000`); err != nil {
		t.Fatal(err)
	}
	var series int64
	s.db.QueryRow(`SELECT id FROM quality_series WHERE target_id=?`, target.ID).Scan(&series)
	var wire []byte
	s.db.QueryRow(`SELECT data FROM quality_buckets WHERE series_id=? AND resolution=60000 AND bucket_start=?`, series, now.Add(-2*time.Minute).UnixMilli()).Scan(&wire)
	bucket, err := qualityDecodeBucket(wire)
	if err != nil {
		t.Fatal(err)
	}
	bucket.ConfigMax++
	wire, err = qualityEncodeBucket(bucket)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`UPDATE quality_buckets SET data=? WHERE series_id=? AND resolution=60000 AND bucket_start=?`, wire, series, now.Add(-2*time.Minute).UnixMilli())
	tx, _ := s.db.Begin()
	err = tcpMiniObserveMinuteTx(context.Background(), tx, id, series, now.Add(-2*time.Minute).UnixMilli(), now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	o, err := s.TCPMini(context.Background(), id, "UTC", now.Add(2*time.Minute))
	if err != nil || o.LastHigh != 0 {
		t.Fatal(o, err)
	}
}
func TestTCPMiniDSTProofAndInitialization(t *testing.T) {
	for _, zone := range []string{"America/Los_Angeles", "Europe/Berlin", "Australia/Lord_Howe", "Pacific/Apia", "Asia/Kathmandu", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		if !tcpMiniZone(loc, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatal(zone)
		}
	}
	la, _ := time.LoadLocation("America/Los_Angeles")
	fall := time.Date(2026, 11, 1, 0, 0, 0, 0, la)
	spring := time.Date(2026, 3, 8, 0, 0, 0, 0, la)
	if fall.AddDate(0, 0, 1).Sub(fall) != 25*time.Hour || spring.AddDate(0, 0, 1).Sub(spring) != 23*time.Hour {
		t.Fatal("DST fixture")
	}
	// An explicit offset counterexample is rejected, not silently interpreted.
	if tcpMiniZone(time.FixedZone("invalid", 86401), fall, fall.Add(time.Hour)) {
		t.Fatal("offset proof")
	}
	s, id, c, now := miniFixture(t, ":memory:")
	s.db.Exec(`UPDATE tcp_mini_state SET evidence_start=?`, now.Add(-time.Hour).UnixMilli())
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	miniSave(t, s, id, c, target, 1, now.Add(-time.Minute), now, 100, "success")
	if o := miniRead(t, s, id, "UTC", now); o.State != "unknown" {
		t.Fatal("initial day invented normal", o)
	}
	for _, v := range []float64{2000, 2001, 4700, 7200, 20001, 100001} {
		t.Log(v, tcpMiniLevel(v))
	}
}
func dropPublicEgressControlFixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	// Migration 33 added the premium notice column; rewind it when the table is still there.
	var hasPremiumNotice int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_google_status') WHERE name='youtube_premium_notice'`).Scan(&hasPremiumNotice); err != nil {
		t.Fatal(err)
	}
	if hasPremiumNotice > 0 {
		if _, err := db.Exec(`ALTER TABLE agent_google_status DROP COLUMN youtube_premium_notice`); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`DELETE FROM schema_migrations WHERE version=33`, `DELETE FROM schema_migrations WHERE version=32`, `DROP TABLE IF EXISTS agent_enrollment_tokens`, `DELETE FROM schema_migrations WHERE version=31`, `DROP INDEX IF EXISTS idx_probe_jobs_one_pending_public_egress`, `DROP TABLE IF EXISTS agent_public_egress_control`, `DELETE FROM schema_migrations WHERE version=30`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}
func dropTCPMiniFixtureSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	dropPublicEgressControlFixtureSchema(t, db)
	for _, q := range []string{`DROP TABLE IF EXISTS tcp_mini_high`, `DROP TABLE IF EXISTS tcp_mini_state`, `DELETE FROM schema_migrations WHERE version=29`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}
func TestTCPMiniMigration28AndBackfill(t *testing.T) {
	file := filepath.Join(t.TempDir(), "migrate.db")
	s, id, c, now := miniFixture(t, file)
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	miniSave(t, s, id, c, target, 1, now.Add(-time.Minute), now, 4700, "success")
	dropTCPMiniFixtureSchema(t, s.db)
	s.Close()
	s, err := Open(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.SchemaVersion(context.Background())
	if err != nil || v != currentSchemaVersion {
		t.Fatal(v, err)
	}
	if err = s.MaintainTCPMini(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	o, err := s.TCPMini(context.Background(), id, "UTC", now)
	if err != nil || o.LastHigh == 0 || o.State != "unknown" {
		t.Fatal("backfill must expose incomplete history", o, err)
	}
	if out := os.Getenv("M05_OUTPUT"); out != "" {
		b, _ := json.MarshalIndent(o, "", "  ")
		if err = os.WriteFile(filepath.Join(out, "migration-backfill.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTCPMiniDST25HourDayKeepsEarlyPeak(t *testing.T) {
	s, id, c, _ := miniFixture(t, ":memory:")
	loc, _ := time.LoadLocation("America/Los_Angeles")
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	s.db.Exec(`UPDATE tcp_mini_state SET evidence_start=?,scanned_at=?`, start.Add(-48*time.Hour).UnixMilli(), start.Add(-time.Hour).UnixMilli())
	miniSave(t, s, id, c, target, 1, start.Add(5*time.Minute), start.Add(6*time.Minute), 4700, "success")
	for i := 1; i <= 2; i++ {
		miniRead(t, s, id, loc.String(), start.Add(time.Duration(i)*12*time.Hour))
	}
	late := start.Add(24*time.Hour + 50*time.Minute)
	miniSave(t, s, id, c, target, 2, late, late.Add(time.Minute), 2001, "success")
	o := miniRead(t, s, id, loc.String(), late.Add(time.Minute))
	if o.SourceDay != "2026-11-01" || o.State != "alert" || o.Peak != 4700 {
		t.Fatal("25h day lost first high", o)
	}
	next := start.AddDate(0, 0, 1).Add(10 * time.Minute)
	miniSave(t, s, id, c, target, 3, next.Add(-time.Minute), next, 100, "success")
	if o = miniRead(t, s, id, loc.String(), next); o.State != "normal" {
		t.Fatal(o)
	}
}

func TestTCPMiniBoundedWindowAndRollback(t *testing.T) {
	s, id, c, now := miniFixture(t, ":memory:")
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	miniSave(t, s, id, c, target, 1, now.Add(-time.Minute), now, 4700, "success")
	var series int64
	s.db.QueryRow(`SELECT id FROM quality_series WHERE target_id=?`, target.ID).Scan(&series)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	start := now.UnixMilli() / 60000 * 60000
	for i := int64(0); i < 4325; i++ {
		at := start + i*60000
		b, e := qualityContribution(a2TCP(target, c.Revision, i+100, time.UnixMilli(at), 2001, "success"), "tcp")
		if e != nil {
			t.Fatal(e)
		}
		b.ConfigMin = 2
		b.ConfigMax = 2
		b.SlotMin = 1
		b.SlotMax = 1
		wire, e := qualityEncodeBucket(b)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(`INSERT OR REPLACE INTO quality_buckets VALUES(?,60000,?,?)`, series, at, wire); e != nil {
			t.Fatal(e)
		}
		if e = tcpMiniObserveMinuteTx(context.Background(), tx, id, series, at, at+60000); e != nil {
			t.Fatal(e)
		}
	}
	var n int
	tx.QueryRow(`SELECT count(*) FROM tcp_mini_high WHERE agent_id=?`, id).Scan(&n)
	if n != 4322 {
		t.Fatal("window rows", n)
	}
	// A rollback must not publish any of those candidate facts.
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT count(*) FROM tcp_mini_high WHERE agent_id=?`, id).Scan(&n)
	if n != 1 {
		t.Fatal("transaction leaked", n)
	}
}

func TestTCPMiniNeverTurnsRawPeakIntoMinuteEvent(t *testing.T) {
	s, id, c, now := miniFixture(t, ":memory:")
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	at := now.Add(-time.Minute)
	for i := int64(0); i < 30; i++ {
		miniSave(t, s, id, c, target, i+1, at.Add(time.Duration(i)*time.Millisecond), now, 0, "success")
	}
	miniSave(t, s, id, c, target, 31, at.Add(40*time.Millisecond), now, 4700, "success")
	o := miniRead(t, s, id, "UTC", now)
	if o.LastHigh != 0 || o.State != "normal" {
		t.Fatal("raw peak or replay fabricated alert", o)
	}
}

func TestTCPMiniFutureExactCursorAndTransientPeak(t *testing.T) {
	s, id, c, now := miniFixture(t, ":memory:")
	target := a1Target(t, c, "mobile", "ipv4", "tcp")
	at := now.Add(20 * time.Second)
	miniSave(t, s, id, c, target, 1, at, now, 4700, "success")
	for i := int64(0); i < 30; i++ {
		miniSave(t, s, id, c, target, i+2, at.Add(time.Duration(i+1)*time.Second), now, 0, "success")
	}
	if o := miniRead(t, s, id, "UTC", at); o.LastHigh != 0 {
		t.Fatal("sample at exact now is excluded from open bucket", o)
	}
	if o := miniRead(t, s, id, "UTC", now.Add(time.Minute)); o.LastHigh == 0 || o.Peak != 4700 {
		t.Fatal("cursor equality lost matured peak", o)
	}
}

func TestTCPMiniRejectsDateRollbackRules(t *testing.T) {
	// A minimal valid TZif with a 00:30 -> previous-day 23:30 transition.
	var data bytes.Buffer
	data.WriteString("TZif")
	data.Write(make([]byte, 16))
	for _, n := range []int32{0, 0, 0, 1, 2, 4} {
		binary.Write(&data, binary.BigEndian, n)
	}
	transition := time.Date(2026, 1, 2, 0, 30, 0, 0, time.UTC)
	binary.Write(&data, binary.BigEndian, int32(transition.Unix()))
	data.WriteByte(1)
	binary.Write(&data, binary.BigEndian, int32(0))
	data.Write([]byte{0, 0})
	binary.Write(&data, binary.BigEndian, int32(-3600))
	data.Write([]byte{0, 2})
	data.Write([]byte{'X', 0, 'Y', 0})
	loc, err := time.LoadLocationFromTZData("test/rollback", data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if tcpMiniZone(loc, transition.Add(-time.Hour), transition.Add(time.Hour)) {
		t.Fatal("non-monotonic local dates accepted")
	}
}

func TestTCPMiniMigrationFailureRollsBack29(t *testing.T) {
	file := filepath.Join(t.TempDir(), "atomic.db")
	s, id, c, now := miniFixture(t, file)
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	miniSave(t, s, id, c, target, 1, now.Add(-time.Minute), now, 100, "success")
	dropTCPMiniFixtureSchema(t, s.db)
	if _, err := s.db.Exec(`CREATE TABLE tcp_mini_high(block INTEGER)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if bad, err := Open(context.Background(), file); err == nil {
		bad.Close()
		t.Fatal("expected migration conflict")
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, states, raw int
	db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version)
	db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='tcp_mini_state'`).Scan(&states)
	db.QueryRow(`SELECT count(*) FROM quality_raw`).Scan(&raw)
	if version != 28 || states != 0 || raw != 1 {
		t.Fatal(version, states, raw)
	}
}

func TestTCPMiniBackfillBatchesBeforeRetention(t *testing.T) {
	s, id, c, now := miniFixture(t, ":memory:")
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	miniSave(t, s, id, c, target, 1, now.Add(-time.Minute), now, 4700, "success")
	var series int64
	s.db.QueryRow(`SELECT id FROM quality_series WHERE target_id=?`, target.ID).Scan(&series)
	var wire []byte
	s.db.QueryRow(`SELECT data FROM quality_buckets WHERE series_id=? AND resolution=60000`, series).Scan(&wire)
	for i := int64(2); i <= 300; i++ {
		if _, err := s.db.Exec(`INSERT INTO quality_buckets VALUES(?,60000,?,?)`, series, now.UnixMilli()-i*60000, wire); err != nil {
			t.Fatal(err)
		}
	}
	s.db.Exec(`UPDATE tcp_mini_state SET initialized=0,backfill_minute=0,backfill_series=0`)
	_, more, err := s.CleanupQualityBatch(context.Background(), now)
	if err != nil || !more {
		t.Fatal("backfill should yield before retention", more, err)
	}
	var initialized bool
	var minute int64
	s.db.QueryRow(`SELECT initialized,backfill_minute FROM tcp_mini_state WHERE agent_id=?`, id).Scan(&initialized, &minute)
	if initialized || minute == 0 {
		t.Fatal(initialized, minute)
	}
	if err = s.MaintainTCPMini(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT initialized FROM tcp_mini_state WHERE agent_id=?`, id).Scan(&initialized)
	if !initialized {
		t.Fatal("backfill did not resume")
	}
}

func TestTCPMiniSixHourLateAndFirstFutureSample(t *testing.T) {
	s, id, c, now := miniFixture(t, ":memory:")
	target := a1Target(t, c, "telecom", "ipv4", "tcp")
	miniSave(t, s, id, c, target, 1, now.Add(-6*time.Hour), now, 2001, "success")
	if o := miniRead(t, s, id, "UTC", now); o.State != "alert" {
		t.Fatal("six-hour legal late", o)
	}
	s.db.Exec(`DELETE FROM tcp_mini_state; DELETE FROM tcp_mini_high; DELETE FROM quality_raw; DELETE FROM quality_buckets`)
	miniSave(t, s, id, c, target, 2, now.Add(20*time.Second), now, 2001, "success")
	var start int64
	s.db.QueryRow(`SELECT evidence_start FROM tcp_mini_state WHERE agent_id=?`, id).Scan(&start)
	if start != now.UnixMilli() {
		t.Fatal("first future sample did not persist activation", start)
	}
	miniRead(t, s, id, "UTC", now)
	if o := miniRead(t, s, id, "UTC", now.Add(time.Minute)); o.LastHigh == 0 {
		t.Fatal("first future event lost", o)
	}
}
