package storage

import (
	"404-probe/internal/protocol"
	"database/sql"
	"strconv"
)

// Explicit typed record representation; target definitions stay in the target table.
const qualitySampleColumns = `seq,target_id,config_revision,slot_revision,scheduled_at,sample_time,finished_at,duration_ms,outcome,resolved_ip,latency,raw_sent,raw_received`

func qualityReadSample(row interface{ Scan(...any) error }, extra ...any) (protocol.QualitySample, error) {
	var s protocol.QualitySample
	var seq, config, slot int64
	var latency sql.NullFloat64
	dest := []any{&seq, &s.TargetID, &config, &slot, &s.ScheduledAt, &s.StartedAt, &s.FinishedAt, &s.DurationMS, &s.Outcome, &s.ResolvedIP, &latency, &s.Sent, &s.Received}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		return s, err
	}
	s.Sequence = strconv.FormatInt(seq, 10)
	s.ConfigRevision = strconv.FormatInt(config, 10)
	s.SlotRevision = strconv.FormatInt(slot, 10)
	if latency.Valid {
		s.LatencyMS = &latency.Float64
	}
	return s, nil
}
