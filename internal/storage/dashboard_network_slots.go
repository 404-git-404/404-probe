package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

type DashboardNetworkSlot struct {
	Slot       int      `json:"slot"`
	TargetName string   `json:"target_name"`
	LatestMS   *float64 `json:"latest_ms"`
	SampledAt  int64    `json:"sampled_at"`
	State      string   `json:"state"`
}

type dashboardSlotQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Exactly one batch query, regardless of device count. Each latest-row lookup
// seeks backwards through the existing quality_raw_target_time index; it never
// scans the global history or averages minute buckets into a fictitious sample.
const dashboardSlotsSQL = `WITH slots(slot,number) AS (VALUES('telecom',1),('unicom',2),('mobile',3))
 SELECT a.id,slots.number,COALESCE(s.kind,''),COALESCE(s.region,''),COALESCE(s.isp,''),COALESCE(s.host,''),
 COALESCE(c.enabled,0),COALESCE(s.slot_enabled,0),COALESCE(s.ipv4_tcp,0),
 COALESCE(cap.supported,0),COALESCE(cap.excluded_reason,''),COALESCE(cap.v2_details,''),
 r.latency,r.sample_time
 FROM agents a CROSS JOIN slots
 LEFT JOIN quality_slot_settings s ON s.agent_id=a.id AND s.slot=slots.slot
 LEFT JOIN quality_config c ON c.agent_id=a.id
 LEFT JOIN agent_state ast ON ast.agent_id=a.id
 LEFT JOIN quality_capability cap ON cap.agent_id=a.id AND cap.epoch=ast.epoch AND cap.session_id=ast.session_id
 LEFT JOIN quality_slots q ON q.agent_id=a.id AND q.slot=slots.slot AND q.family='ipv4' AND q.protocol='tcp'
 LEFT JOIN quality_raw r ON r.id=(SELECT raw.id FROM quality_raw raw
  WHERE raw.agent_id=a.id AND raw.target_id=q.target_id AND raw.sample_time<=?
   AND raw.outcome='success' AND raw.success=1 AND raw.latency>=0
  ORDER BY raw.sample_time DESC,raw.seq DESC LIMIT 1)`

func readDashboardNetworkSlots(ctx context.Context, q dashboardSlotQueryer, now time.Time) (map[string][3]DashboardNetworkSlot, error) {
	rows, err := q.QueryContext(ctx, dashboardSlotsSQL, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][3]DashboardNetworkSlot{}
	for rows.Next() {
		var id, kind, region, isp, host, excluded, details string
		var enabled, slotEnabled, tcp, supported bool
		var latency sql.NullFloat64
		var sampled sql.NullInt64
		var slot DashboardNetworkSlot
		if err := rows.Scan(&id, &slot.Slot, &kind, &region, &isp, &host, &enabled, &slotEnabled, &tcp, &supported, &excluded, &details, &latency, &sampled); err != nil {
			return nil, err
		}
		if kind == "builtin" {
			carrier := map[string]string{"telecom": "电信", "unicom": "联通", "mobile": "移动"}[isp]
			if carrier == "" {
				carrier = isp
			}
			slot.TargetName = region + carrier
		} else {
			slot.TargetName = strings.TrimSpace(region + " " + isp)
		}
		if slot.TargetName == "" {
			slot.TargetName = fmt.Sprintf("卡槽 %d（未标注）", slot.Slot)
		}
		unsupported := !supported || excluded != ""
		if details != "" {
			var caps protocol.QualityCapabilities
			if json.Unmarshal([]byte(details), &caps) == nil {
				for _, lane := range caps.Lanes {
					if lane.Family == "ipv4" && lane.Protocol == "tcp" && !lane.Supported {
						unsupported = true
					}
				}
			}
		}
		switch {
		case unsupported:
			slot.State = "unsupported"
		case !enabled || !slotEnabled || !tcp || host == "":
			slot.State = "no_target"
		case !latency.Valid || !sampled.Valid || math.IsNaN(latency.Float64) || math.IsInf(latency.Float64, 0):
			slot.State = "no_data"
		default:
			slot.LatestMS = &latency.Float64
			slot.SampledAt = sampled.Int64
			slot.State = "ok"
			if now.UnixMilli()-sampled.Int64 > int64((5*time.Minute)/time.Millisecond) {
				slot.State = "stale"
			}
		}
		slots := out[id]
		slots[slot.Slot-1] = slot
		out[id] = slots
	}
	return out, rows.Err()
}
