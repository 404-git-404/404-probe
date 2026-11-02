package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
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

// Settings/capabilities and sample cache misses are each read in one batch.
// The five-second sample cache includes negative results. Labels, capability,
// enabled state and the five-minute age boundary are recalculated on every read.
const dashboardSlotsSQL = `WITH slots(slot,number) AS (VALUES('telecom',1),('unicom',2),('mobile',3))
 SELECT a.id,slots.number,COALESCE(s.kind,''),COALESCE(s.region,''),COALESCE(s.isp,''),COALESCE(s.host,''),
 COALESCE(c.enabled,0),COALESCE(s.slot_enabled,0),COALESCE(s.ipv4_tcp,0),
 COALESCE(cap.supported,0),COALESCE(cap.excluded_reason,''),COALESCE(cap.v2_details,''),COALESCE(q.target_id,'')
 FROM agents a CROSS JOIN slots
 LEFT JOIN quality_slot_settings s ON s.agent_id=a.id AND s.slot=slots.slot
 LEFT JOIN quality_config c ON c.agent_id=a.id
 LEFT JOIN agent_state ast ON ast.agent_id=a.id
 LEFT JOIN quality_capability cap ON cap.agent_id=a.id AND cap.epoch=ast.epoch AND cap.session_id=ast.session_id
 LEFT JOIN quality_slots q ON q.agent_id=a.id AND q.slot=slots.slot AND q.family='ipv4' AND q.protocol='tcp'`

const dashboardSlotSampleSQL = `WITH targets(target_id) AS (VALUES %s)
 SELECT t.target_id,f.rtt,f.sample_time FROM targets t
 LEFT JOIN quality_facts f ON f.id=(SELECT fact.id FROM quality_facts fact
 WHERE fact.target_id=t.target_id AND fact.sample_time>=? AND fact.sample_time<=? AND fact.eligible=1 AND fact.rtt>=0
 ORDER BY fact.sample_time DESC,fact.seq DESC,fact.id DESC LIMIT 1)`

type dashboardSlotSample struct {
	MS      sql.NullFloat64
	Sampled sql.NullInt64
	ReadAt  time.Time
}
type dashboardSlotCache struct {
	mu      sync.Mutex
	entries map[string]dashboardSlotSample
}
type dashboardSlotPosition struct {
	agent  string
	index  int
	target string
}

func readDashboardNetworkSlots(ctx context.Context, q dashboardSlotQueryer, now time.Time, caches ...*dashboardSlotCache) (map[string][3]DashboardNetworkSlot, error) {
	rows, err := q.QueryContext(ctx, dashboardSlotsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][3]DashboardNetworkSlot{}
	positions := []dashboardSlotPosition{}
	unsupportedAgents := map[string]bool{}
	for rows.Next() {
		var id, kind, region, isp, host, excluded, details string
		var enabled, slotEnabled, tcp, supported bool
		var target string
		var slot DashboardNetworkSlot
		if err := rows.Scan(&id, &slot.Slot, &kind, &region, &isp, &host, &enabled, &slotEnabled, &tcp, &supported, &excluded, &details, &target); err != nil {
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
		unsupported, classified := unsupportedAgents[id]
		if !classified {
			unsupported = !supported || excluded != ""
			if !unsupported && details != "" {
				var caps protocol.QualityCapabilities
				if json.Unmarshal([]byte(details), &caps) == nil {
					for _, lane := range caps.Lanes {
						if lane.Family == "ipv4" && lane.Protocol == "tcp" && !lane.Supported {
							unsupported = true
						}
					}
				}
			}
			unsupportedAgents[id] = unsupported
		}
		switch {
		case unsupported:
			slot.State = "unsupported"
		case !enabled || !slotEnabled || !tcp || host == "":
			slot.State = "no_target"
		default:
			slot.State = "no_data"
			if target != "" {
				positions = append(positions, dashboardSlotPosition{id, slot.Slot - 1, target})
			}
		}
		slots := out[id]
		slots[slot.Slot-1] = slot
		out[id] = slots
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	samples := map[string]dashboardSlotSample{}
	var cache *dashboardSlotCache
	if len(caches) > 0 {
		cache = caches[0]
	}
	if cache != nil {
		cache.mu.Lock()
		for _, p := range positions {
			if v, ok := cache.entries[p.target]; ok && !now.Before(v.ReadAt) && now.Sub(v.ReadAt) < 5*time.Second {
				samples[p.target] = v
			}
		}
		cache.mu.Unlock()
	}
	targets := []string{}
	args := []any{}
	seen := map[string]bool{}
	for _, p := range positions {
		if _, ok := samples[p.target]; !ok && !seen[p.target] {
			seen[p.target] = true
			targets = append(targets, "(?)")
			args = append(args, p.target)
		}
	}
	if len(targets) > 0 {
		args = append(args, now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
		sampleRows, err := q.QueryContext(ctx, fmt.Sprintf(dashboardSlotSampleSQL, strings.Join(targets, ",")), args...)
		if err != nil {
			return nil, err
		}
		for sampleRows.Next() {
			var target string
			var v dashboardSlotSample
			v.ReadAt = now
			if err := sampleRows.Scan(&target, &v.MS, &v.Sampled); err != nil {
				sampleRows.Close()
				return nil, err
			}
			samples[target] = v
		}
		err = sampleRows.Err()
		sampleRows.Close()
		if err != nil {
			return nil, err
		}
		if cache != nil {
			cache.mu.Lock()
			if len(cache.entries) > 2048 {
				cache.entries = nil
			}
			if cache.entries == nil {
				cache.entries = map[string]dashboardSlotSample{}
			}
			for target, v := range samples {
				if !v.ReadAt.Before(cache.entries[target].ReadAt) {
					cache.entries[target] = v
				}
			}
			cache.mu.Unlock()
		}
	}
	for _, p := range positions {
		v := samples[p.target]
		slots := out[p.agent]
		slot := &slots[p.index]
		if v.MS.Valid && v.Sampled.Valid && v.Sampled.Int64 >= now.Add(-time.Hour).UnixMilli() && v.Sampled.Int64 <= now.UnixMilli() && !math.IsNaN(v.MS.Float64) && !math.IsInf(v.MS.Float64, 0) {
			ms := v.MS.Float64
			slot.LatestMS = &ms
			slot.SampledAt = v.Sampled.Int64
			slot.State = "ok"
			if now.UnixMilli()-slot.SampledAt > int64((5*time.Minute)/time.Millisecond) {
				slot.State = "stale"
			}
		}
		out[p.agent] = slots
	}
	return out, nil
}
