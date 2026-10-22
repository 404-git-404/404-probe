package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"runtime"
	"time"
	_ "time/tzdata" // Browser IANA zones also work on hosts without zoneinfo.

	"404-probe/internal/protocol"
)

const tcpMiniWindow = int64((72*time.Hour + time.Minute) / time.Millisecond)
const tcpMiniMinute = int64(60000)

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

func tcpMiniEnsureTx(ctx context.Context, tx *sql.Tx, id string, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tcp_mini_state(agent_id,evidence_start) VALUES(?,?)`, id, now)
	return err
}

// Observe only an identity-safe staged TCP minute, not a raw peak. The open
// minute is rebuilt from actual matured samples so accepted future skew cannot
// trigger early. Partial coverage with genuine valid RTTs remains eligible.
func tcpMiniObserveMinuteTx(ctx context.Context, tx *sql.Tx, id string, series, minute, now int64) error {
	if minute >= now {
		return nil
	}
	var wire []byte
	var targetID, targetJSON string
	err := tx.QueryRowContext(ctx, `SELECT b.data,t.id,t.payload FROM quality_buckets b JOIN quality_series s ON s.id=b.series_id JOIN quality_targets t ON t.id=s.target_id WHERE b.series_id=? AND b.resolution=60000 AND b.bucket_start=? AND s.agent_id=? AND s.semantics='staged_probe_v2'`, series, minute, id).Scan(&wire, &targetID, &targetJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	target, err := qualityTargetFromPayload(targetID, targetJSON)
	if err != nil {
		return err
	}
	if target.Protocol != "tcp" || target.Family != "ipv4" {
		return nil
	}
	b, err := qualityDecodeBucket(wire)
	if err != nil {
		return err
	}
	if now < minute+tcpMiniMinute {
		b = qualityNewBucket()
		rows, e := tx.QueryContext(ctx, `SELECT `+qualitySampleColumns+` FROM quality_raw WHERE agent_id=? AND target_id=? AND sample_time>=? AND sample_time<? AND received_at<=? ORDER BY sample_time,seq`, id, target.ID, minute, now, now)
		if e != nil {
			return e
		}
		for rows.Next() {
			sample, e := qualityReadSample(rows)
			if e != nil {
				rows.Close()
				return e
			}
			if !sample.Staged() {
				continue
			}
			part, e := qualityContribution(sample, "tcp")
			if e != nil {
				rows.Close()
				return e
			}
			cfg, _ := protocol.QualityInteger(sample.ConfigRevision)
			slot, _ := protocol.QualityInteger(sample.SlotRevision)
			part.ConfigMin, part.ConfigMax, part.SlotMin, part.SlotMax = cfg, cfg, slot, slot
			qualityMergeBucket(&b, part)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	stats := qualityBucketStats(b)
	if stats.Count == 0 || stats.ValidRTTs == 0 || stats.P95 == nil || b.ConfigMin != b.ConfigMax || b.SlotMin != b.SlotMax || b.SlotMin == 0 {
		return nil
	}
	if err = tcpMiniEnsureTx(ctx, tx, id, now); err != nil {
		return err
	}
	p := *stats.P95
	if p > 2000 {
		source, e := json.Marshal(struct {
			Target       protocol.QualityTarget `json:"target"`
			Config, Slot string
			ObservedAt   int64
		}{target, qualityRevisionString(b.ConfigMin), qualityRevisionString(b.SlotMin), now})
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tcp_mini_state SET last_high=MAX(last_high,?),revision=revision+1 WHERE agent_id=?`, minute, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO tcp_mini_high SELECT ?,?,?,? WHERE ?>=(SELECT last_high-? FROM tcp_mini_state WHERE agent_id=?) ON CONFLICT(agent_id,minute) DO UPDATE SET peak_ms=MAX(peak_ms,excluded.peak_ms),source=CASE WHEN excluded.peak_ms>peak_ms THEN excluded.source ELSE source END`, id, minute, p, string(source), minute, tcpMiniWindow, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM tcp_mini_high WHERE agent_id=? AND minute<(SELECT last_high-? FROM tcp_mini_state WHERE agent_id=?)`, id, tcpMiniWindow, id)
	} else if p < 2000 {
		_, err = tx.ExecContext(ctx, `UPDATE tcp_mini_state SET last_low=MAX(last_low,?),revision=revision+1 WHERE agent_id=? AND last_low<?`, minute, id, minute)
	}
	return err
}

// Called before existing quality retention, including at restart. No browser
// needs to have visited. Pending future samples are recoverable from Raw; the
// cursor and facts commit together. Idle wall time never expires evidence:
// retention cannot delete an unprocessed future sample (this runs first).
// Existing minute buckets provide conservative initial backfill.
func (s *Store) MaintainTCPMini(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := now.UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO tcp_mini_state(agent_id,evidence_start) SELECT id,? FROM agents`, at); err != nil {
		return err
	}
	type state struct {
		id             string
		scan, start    int64
		initialized    bool
		minute, series int64
	}
	var states []state
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,scanned_at,evidence_start,initialized,backfill_minute,backfill_series FROM tcp_mini_state ORDER BY scanned_at,agent_id LIMIT 8`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var x state
		if err = rows.Scan(&x.id, &x.scan, &x.start, &x.initialized, &x.minute, &x.series); err != nil {
			rows.Close()
			return err
		}
		states = append(states, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, x := range states {
		if at < x.scan { // Clock rollback is not silently certified continuous.
			if _, err = tx.ExecContext(ctx, `UPDATE tcp_mini_state SET evidence_start=?,scanned_at=? WHERE agent_id=?`, at, at, x.id); err != nil {
				return err
			}
			continue
		}
		type pending struct{ series, minute, stamp int64 }
		var work []pending
		// Replaying matured time prefixes preserves a temporary high even when
		// several future observations mature during one maintenance interval.
		rows, err = tx.QueryContext(ctx, `SELECT DISTINCT s.id,r.sample_time/60000*60000,r.sample_time FROM quality_raw r JOIN quality_series s ON s.target_id=r.target_id AND s.semantics='staged_probe_v2' WHERE r.agent_id=? AND r.received_at<r.sample_time AND r.sample_time>=? AND r.sample_time<? ORDER BY r.sample_time`, x.id, max(x.scan, x.start), at)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p pending
			if err = rows.Scan(&p.series, &p.minute, &p.stamp); err != nil {
				rows.Close()
				return err
			}
			work = append(work, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, p := range work {
			if err = tcpMiniObserveMinuteTx(ctx, tx, x.id, p.series, p.minute, p.stamp+1); err != nil {
				return err
			}
		}
		if !x.initialized {
			work = nil
			rows, err = tx.QueryContext(ctx, `SELECT b.series_id,b.bucket_start FROM quality_buckets b JOIN quality_series s ON s.id=b.series_id WHERE s.agent_id=? AND s.semantics='staged_probe_v2' AND b.resolution=60000 AND b.bucket_start<? AND (b.bucket_start,b.series_id)>(?,?) ORDER BY b.bucket_start,b.series_id LIMIT 256`, x.id, at, x.minute, x.series)
			if err != nil {
				return err
			}
			for rows.Next() {
				var p pending
				if err = rows.Scan(&p.series, &p.minute); err != nil {
					rows.Close()
					return err
				}
				work = append(work, p)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, p := range work {
				if err = tcpMiniObserveMinuteTx(ctx, tx, x.id, p.series, p.minute, at); err != nil {
					return err
				}
				x.minute, x.series = p.minute, p.series
			}
			x.initialized = len(work) < 256
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tcp_mini_state SET scanned_at=?,initialized=?,backfill_minute=?,backfill_series=? WHERE agent_id=?`, at, x.initialized, x.minute, x.series, x.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) tcpMiniPending(ctx context.Context, now time.Time) (bool, error) {
	var pending bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tcp_mini_state WHERE initialized=0 OR scanned_at<?)`, now.Add(-30*time.Second).UnixMilli()).Scan(&pending)
	return pending, err
}

type TCPMiniStatus struct {
	Now           int64   `json:"now"`
	Timezone      string  `json:"timezone"`
	Rules         string  `json:"rules"`
	State         string  `json:"state"`
	Coverage      string  `json:"coverage"`
	EvidenceStart int64   `json:"evidence_start"`
	LastHigh      int64   `json:"last_high"`
	LastLow       int64   `json:"last_low"`
	SourceDay     string  `json:"source_day"`
	Today         string  `json:"today"`
	Peak          float64 `json:"peak_ms"`
	Level         float64 `json:"level_ms"`
	Revision      int64   `json:"revision"`
	// CurrentTCP is read-only display gating (R07, N6): true while this Agent has an open IPv4 TCP
	// authorization interval and is neither revoked nor paused. It never changes the alert rules.
	CurrentTCP bool `json:"current_tcp"`
}

func tcpMiniLevel(v float64) float64 {
	if v <= 2000 {
		return 2000
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if v <= p*m {
			return p * m
		}
	}
	return p * 10
}

// ZoneBounds walks actual transitions, not sampled hours. A backwards local
// date or offset outside +/-24h invalidates the r3 proof over this evidence
// interval. Every query rechecks the future horizon as rules/time advance.
func tcpMiniZone(loc *time.Location, start, end time.Time) bool {
	for t := start; !t.After(end); {
		_, offset := t.In(loc).Zone()
		if offset < -86400 || offset > 86400 {
			return false
		}
		_, next := t.In(loc).ZoneBounds()
		if next.IsZero() || next.After(end) {
			return true
		}
		if !next.After(t) {
			return false
		}
		if next.Add(-time.Nanosecond).In(loc).Format("2006-01-02") > next.In(loc).Format("2006-01-02") {
			return false
		}
		t = next
	}
	return true
}

// TCPMini is strictly read-only. Coverage is conservative across upgrades,
// restarts and delayed maintenance; missing evidence never means confirmed good.
func (s *Store) TCPMini(ctx context.Context, id, zone string, now time.Time) (TCPMiniStatus, error) {
	o := TCPMiniStatus{Now: now.UnixMilli(), Timezone: zone, Rules: runtime.Version() + " time.LoadLocation (host/embedded tzdata)", State: "unknown", Coverage: "insufficient", Level: 2000}
	if len(zone) > 128 || zone == "Local" || zone == "" {
		return o, ErrQualityInvalid
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return o, ErrQualityInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return o, err
	}
	defer tx.Rollback()
	// Computed first and independently, so every return path below carries it.
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_intervals i JOIN agents a ON a.id=i.agent_id WHERE i.agent_id=? AND i.family='ipv4' AND i.protocol='tcp' AND i.end_at IS NULL AND a.revoked=0 AND a.disabled_at IS NULL AND NOT EXISTS(SELECT 1 FROM agent_removal_operations o WHERE o.agent_id=a.id))`, id).Scan(&o.CurrentTCP); err != nil {
		return o, err
	}
	var scan int64
	var initialized bool
	err = tx.QueryRowContext(ctx, `SELECT evidence_start,scanned_at,initialized,last_high,last_low,revision FROM tcp_mini_state WHERE agent_id=?`, id).Scan(&o.EvidenceStart, &scan, &initialized, &o.LastHigh, &o.LastLow, &o.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=?)`, id).Scan(&exists)
		if err == nil && !exists {
			err = ErrAgentNotFound
		}
		return o, err
	}
	if err != nil {
		return o, err
	}
	start := o.EvidenceStart
	for _, v := range []int64{o.LastHigh - tcpMiniWindow, o.LastLow} {
		if v > 0 && v < start {
			start = v
		}
	}
	if start <= 0 || !tcpMiniZone(loc, time.UnixMilli(start), now.Add(2*time.Minute)) {
		o.Coverage = "unsupported_date_rules"
		return o, nil
	}
	day := func(t int64) string { return time.UnixMilli(t).In(loc).Format("2006-01-02") }
	o.Today = day(o.Now)
	if o.LastHigh > 0 {
		o.SourceDay = day(o.LastHigh)
		rows, e := tx.QueryContext(ctx, `SELECT minute,peak_ms FROM tcp_mini_high WHERE agent_id=?`, id)
		if e != nil {
			return o, e
		}
		for rows.Next() {
			var t int64
			var p float64
			if e = rows.Scan(&t, &p); e != nil {
				rows.Close()
				return o, e
			}
			if day(t) == o.SourceDay {
				o.Peak = math.Max(o.Peak, p)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return o, e
		}
	}
	alert := o.LastHigh > 0 && (o.LastLow == 0 || day(o.LastLow) <= o.SourceDay)
	if alert {
		o.Level = tcpMiniLevel(o.Peak)
	}
	// Idle time alone cannot invalidate a durable alert. Only actual unprocessed
	// matured future samples make this read insufficient. GET never matures them.
	if !initialized || scan > o.Now || o.LastHigh >= o.Now || o.LastLow >= o.Now {
		return o, nil
	}
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM quality_raw r JOIN quality_targets t ON t.id=r.target_id WHERE r.agent_id=? AND r.received_at<r.sample_time AND r.sample_time>=? AND r.sample_time<? AND json_extract(t.payload,'$.family')='ipv4' AND json_extract(t.payload,'$.protocol')='tcp' AND json_extract(r.details_json,'$.semantics')='staged_probe_v2')`, id, scan, o.Now).Scan(&pending); err != nil {
		return o, err
	}
	if pending {
		return o, nil
	}
	if alert {
		if o.SourceDay <= day(o.EvidenceStart) {
			return o, nil
		}
		o.State = "alert"
		o.Coverage = "complete_since_start"
	} else if o.LastLow > 0 && day(o.LastLow) > day(o.EvidenceStart) {
		o.State = "normal"
		o.Coverage = "complete_since_start"
	}
	return o, nil
}
