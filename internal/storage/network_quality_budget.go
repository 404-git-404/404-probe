package storage

import (
	"404-probe/internal/protocol"
	"context"
	"path/filepath"
	"time"
)

type qualityDisk struct{ DB, WAL, Free, Total, RootFree, RootTotal int64 }
type qualityBudget struct {
	Disk                                        qualityDisk
	Bytes, RawBytes, MinuteBytes                int64
	Agents, Lanes, Scale, RawLimit, MinuteLimit int
	Pressure                                    bool
	Degraded                                    bool
	ReceiptBytes, BucketBytes, FactBytes        int64
	MigrationBytes                              int64
}

// S1 forecasts all retained tiers and identities, not 48h of raw alone. Native
// DB/WAL occupancy includes dictionaries, indexes and migration allocations;
// complete per-table/index dbstat verification belongs to offline acceptance.
func qualityRollupDiskPlan(d qualityDisk, used, rawBytes, receiptBytes, bucketBytes, factBytes int64, agents, lanes int, migrationRows ...int64) qualityBudget {
	p := qualityBudget{Disk: d, RawBytes: max(int64(1536), rawBytes), ReceiptBytes: max(int64(160), receiptBytes), BucketBytes: max(int64(1024), bucketBytes), FactBytes: max(int64(192), factBytes), Agents: agents, Lanes: lanes, Scale: 1}
	p.MinuteBytes = p.BucketBytes
	if len(migrationRows) > 0 {
		p.MigrationBytes = max(int64(0), migrationRows[0]) * (p.ReceiptBytes + p.FactBytes + 3*p.BucketBytes)
	}
	reserve := max(int64(64<<20), d.Total/20)
	rootReserve := max(int64(64<<20), d.RootTotal/20)
	headroom := min(d.Free-reserve, d.RootFree-rootReserve)
	p.Pressure = headroom < 64<<20
	p.Bytes = max(int64(0), (used+headroom)/4)
	var rawRounds, minuteRows int64
	for scale := 1; scale <= 10; scale++ {
		p.Scale = scale
		cadence := int64(24 * scale)
		rawRounds = (28800+cadence-1)/cadence + 1
		receiptRounds := (172800 + cadence - 1) / cadence
		minuteRows = min(int64(1440), (86400+cadence-1)/cadence)
		fiveRows := min(int64(2016), (604800+cadence-1)/cadence)
		hourRows := min(int64(720), (2592000+cadence-1)/cadence)
		desired := int64(lanes) * (rawRounds*(p.RawBytes+p.FactBytes) + receiptRounds*p.ReceiptBytes + (minuteRows+fiveRows+hourRows)*p.BucketBytes) * 11 / 10
		if max(used, desired)+p.MigrationBytes <= p.Bytes {
			break
		}
		if scale == 10 {
			p.Degraded = true
		}
	}
	weight := max(int64(1), rawRounds*p.RawBytes+minuteRows*p.MinuteBytes)
	// Identity/history allocation is reserved before dividing the remaining raw
	// and compatibility-minute admission budget proportionally among active lanes.
	reserved := int64(lanes) * ((172800+int64(24*p.Scale)-1)/int64(24*p.Scale)*p.ReceiptBytes + rawRounds*p.FactBytes + 2736*p.BucketBytes)
	available := max(int64(0), p.Bytes-reserved)
	rawAllocation := available * (rawRounds * p.RawBytes) / weight
	p.RawLimit = int(rawAllocation / p.RawBytes)
	p.MinuteLimit = int((available - rawAllocation) / p.MinuteBytes)
	return p
}

func qualityDiskPlan(d qualityDisk, used, rawBytes int64, agents, lanes int) qualityBudget {
	p := qualityBudget{Disk: d, RawBytes: max(int64(1536), rawBytes), Agents: agents, Lanes: lanes, Scale: 1}
	p.MinuteBytes = max(int64(384), p.RawBytes/4)
	reserve := max(int64(64<<20), d.Total/20)
	rootReserve := max(int64(64<<20), d.RootTotal/20)
	headroom := min(d.Free-reserve, d.RootFree-rootReserve)
	// File allocation is already reflected in available space. A large reusable
	// WAL alone is not pressure; qualityPressure separately checks WAL health.
	p.Pressure = headroom < 64<<20
	p.Bytes = max(int64(0), (used+headroom)/4)
	// Each scale is sized independently: at >=60s a new minute bucket can
	// accompany every raw round. Minute storage does not shrink like 30s raw.
	var rounds, minutes int64
	for scale := 1; scale <= 10; scale++ {
		p.Scale = scale
		rounds = (172800 + int64(24*scale) - 1) / int64(24*scale)
		minutes = min(int64(2880), rounds)
		desired := int64(lanes) * (rounds*p.RawBytes + minutes*p.MinuteBytes) * 11 / 10
		if desired <= p.Bytes {
			break
		}
		if scale == 10 {
			p.Degraded = true
		}
	}
	weight := max(int64(1), rounds*p.RawBytes+minutes*p.MinuteBytes)
	rawAllocation := p.Bytes * (rounds * p.RawBytes) / weight
	p.RawLimit = int(rawAllocation / p.RawBytes)
	p.MinuteLimit = int((p.Bytes - rawAllocation) / p.MinuteBytes)
	return p
}
func (s *Store) qualityBudget(ctx context.Context) qualityBudget {
	s.qualityBudgetMu.Lock()
	defer s.qualityBudgetMu.Unlock()
	if time.Since(s.qualityBudgetAt) < 3*time.Second {
		return s.qualityBudgetCache
	}
	p := qualityBudget{RawBytes: 1536, MinuteBytes: 384, Scale: 1, RawLimit: qualityRawGlobal, MinuteLimit: qualityMinuteGlobal}
	if s.databasePath == "" && s.qualityDiskObserve == nil {
		return p
	}
	var d qualityDisk
	var err error
	if s.qualityDiskObserve != nil {
		d, err = s.qualityDiskObserve()
	} else {
		d.DB, err = sqliteFileSize(s.databasePath, "database", false)
		if err == nil {
			d.WAL, err = sqliteFileSize(s.databasePath+"-wal", "WAL", true)
		}
		if err == nil {
			d.Free, d.Total, err = qualityFilesystem(filepath.Dir(s.databasePath))
		}
		if err == nil {
			d.RootFree, d.RootTotal, err = qualityFilesystem(qualityRootFilesystem())
		}
	}
	if err != nil {
		p.Pressure = true
		return p
	}
	var agents, lanes int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT i.agent_id),COUNT(*) FROM quality_intervals i JOIN quality_config q ON q.agent_id=i.agent_id JOIN agents a ON a.id=i.agent_id JOIN agent_state st ON st.agent_id=i.agent_id JOIN quality_capability c ON c.agent_id=i.agent_id AND c.epoch=st.epoch AND c.session_id=st.session_id WHERE i.end_at IS NULL AND q.enabled=1 AND a.revoked=0 AND a.disabled_at IS NULL AND c.supported=1 AND c.excluded_reason='' AND NOT EXISTS(SELECT 1 FROM agent_removal_operations o WHERE o.agent_id=a.id)`).Scan(&agents, &lanes)
	if err != nil {
		p.Pressure = true
		return p
	}
	// Native file lengths conservatively charge all allocated DB pages and WAL,
	// including automatic indexes, migration peaks and reusable free pages. A
	// dbstat traversal must never consume a configuration/report request budget.
	used := max(int64(0), d.DB+d.WAL)
	// No dbstat on a request: an aggregate virtual-table step can read an
	// entire cold btree without checking cancellation, even with a short context.
	// Finite conservative forecast costs and live physical occupancy bound the
	// allocation; full per-table/index measurements belong to offline acceptance.
	rowBytes := func(table string, floor int64) int64 {
		cached := map[string]int64{"quality_raw": s.qualityBudgetCache.RawBytes, "quality_receipts": s.qualityBudgetCache.ReceiptBytes, "quality_buckets": s.qualityBudgetCache.BucketBytes, "quality_facts": s.qualityBudgetCache.FactBytes}[table]
		cost := max(floor, cached)
		return cost
	}
	var migrationRows int64
	if err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN complete=0 THEN MAX(0,(SELECT value FROM quality_totals WHERE kind='raw')-(SELECT value FROM quality_totals WHERE kind='receipts')) ELSE 0 END FROM quality_backfill WHERE id=1`).Scan(&migrationRows); err != nil {
		p.Pressure = true
		return p
	}
	p = qualityRollupDiskPlan(d, used, rowBytes("quality_raw", 1536), rowBytes("quality_receipts", 160), rowBytes("quality_buckets", 1024), rowBytes("quality_facts", 192), agents, lanes, migrationRows)
	s.qualityBudgetCache = p
	s.qualityBudgetAt = time.Now()
	return p
}

func (p qualityBudget) apply(c *protocol.QualityConfig) {
	if p.Pressure {
		c.Enabled = false
		if c.Version == 2 {
			c.State = "storage_pressure"
		}
		return
	}
	if c.Semantics == protocol.QualityStagedSemantics {
		// The capacity plan only slows a lane down; it never lowers what the user chose.
		if slowed := 30000 * p.Scale; slowed > c.IntervalMS {
			c.IntervalMS = slowed
			c.State = "capacity_throttled"
		}
		if p.Degraded {
			c.State = "retention_degraded"
		}
	} else if p.Scale > 1 {
		c.Enabled = false
	}
}
func (p qualityBudget) limits(lanes int) (int, int) {
	total := max(1, p.Lanes)
	return max(1, p.RawLimit*max(1, lanes)/total), max(1, p.MinuteLimit*max(1, lanes)/total)
}
