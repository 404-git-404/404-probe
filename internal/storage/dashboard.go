package storage

import (
	"404-probe/internal/protocol"
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type DashboardRecord struct {
	Snapshot                       AgentSnapshot
	Plan                           *AgentPlan
	CountryCode, CountrySource     string
	Google                         *GoogleStatusSnapshot
	GoogleSupported, GooglePending bool
	Outbound                       *OutboundSnapshot
	TrafficTotals                  *TrafficTotals
}
type DashboardData struct {
	Catalog OrganizationCatalog
	Records []DashboardRecord
}
type dashboardScanner interface{ Scan(...any) error }

// ReadDashboardData uses a fixed number of queries in one read transaction.
// It never projects an expired billing cycle into a misleading fresh zero.
func (s *Store) ReadDashboardData(ctx context.Context, now time.Time, offlineTimeout time.Duration, includeRevoked bool) (DashboardData, error) {
	out := DashboardData{Records: []DashboardRecord{}}
	nowMS, offlineMS, err := validateAgentSnapshotTime(now, offlineTimeout)
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out.Catalog, err = organizationCatalogTx(ctx, tx)
	if err != nil {
		return out, err
	}
	agents := []Agent{}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,revoked,disabled_at,created_at FROM agents ORDER BY created_at DESC,id DESC`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var a Agent
		var disabled sql.NullInt64
		if err = rows.Scan(&a.ID, &a.Name, &a.Revoked, &disabled, &a.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		if disabled.Valid {
			v := disabled.Int64
			a.DisabledAt = &v
		}
		agents = append(agents, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	states := map[string]State{}
	rows, err = tx.QueryContext(ctx, `SELECT agent_id,epoch,session_id,sequence,boot_id,hostname,os,arch,agent_version,agent_upgrade_capable,agent_upgrade_v2,uptime,cpu,cpu_cores,load1,load5,load15,ram_used,ram_total,ram_percent,swap_used,swap_total,swap_percent,disk_used,disk_total,disk_percent,raw_rx,raw_tx,rx_rate,tx_rate,rx_total,tx_total,collected_at,last_seen,cpu_steal,disk_read_rate,disk_write_rate,disk_busy,COALESCE(country_code,''),network_counters_version,network_counters_json FROM agent_state`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		state, scanErr := scanDashboardState(rows)
		if scanErr != nil {
			rows.Close()
			return out, scanErr
		}
		states[state.AgentID] = state
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	plans := map[string]AgentPlan{}
	rows, err = tx.QueryContext(ctx, `SELECT agent_id,COALESCE(traffic_mode,''),COALESCE(quota_value,''),COALESCE(quota_unit,''),quota_bytes,
		COALESCE(cycle_kind,''),COALESCE(cycle_count,0),COALESCE(cycle_anchor,''),COALESCE(timezone,''),usage_bytes,usage_status,cycle_start,cycle_end,
		COALESCE(bandwidth_value,''),COALESCE(bandwidth_unit,''),bandwidth_bps,COALESCE(currency,''),COALESCE(purchase_price,''),COALESCE(purchase_price_period,''),
		COALESCE(renewal_price,''),COALESCE(renewal_price_period,''),COALESCE(purchase_date,''),COALESCE(renewal_date,''),COALESCE(expiry_date,''),COALESCE(country_code_override,''),updated_at,observe_after,
		cycle_rx_bytes,cycle_tx_bytes,cycle_dir_basis,cycle_dir_since
		 FROM agent_plans`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		plan, scanErr := scanDashboardPlan(rows)
		if scanErr != nil {
			rows.Close()
			return out, scanErr
		}
		plans[plan.AgentID] = plan
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT e.agent_id,e.payload FROM agent_public_egress e JOIN agent_public_egress_control c ON c.agent_id=e.agent_id WHERE c.enabled=1`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return out, err
		}
		var saved EgressState
		if len(raw) <= 4096 && json.Unmarshal([]byte(raw), &saved) == nil && saved.Version == 1 {
			saved.IPv4 = protocol.NormalizeStoredEgress(saved.IPv4, "ipv4", nowMS, nowMS)
			saved.IPv6 = protocol.NormalizeStoredEgress(saved.IPv6, "ipv6", nowMS, nowMS)
			state, ok := states[id]
			if ok {
				state.PublicEgress = saved
				states[id] = state
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	countries := map[string]string{}
	rows, err = tx.QueryContext(ctx, `SELECT agent_id,COALESCE(last_country_code,'') FROM agent_country_code_lookups`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id, code string
		if err = rows.Scan(&id, &code); err != nil {
			rows.Close()
			return out, err
		}
		countries[id] = code
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	cards, err := readDashboardCards(ctx, tx, now)
	if err != nil {
		return out, err
	}
	for _, agent := range agents {
		if agent.Revoked && !includeRevoked {
			continue
		}
		record := cards[agent.ID]
		record.Snapshot = AgentSnapshot{Agent: agent, StateStale: true}
		record.CountrySource = "unknown"

		if state, ok := states[agent.ID]; ok {
			state.Name = agent.Name
			record.Snapshot.State = &state
			record.Snapshot.Online = !agent.Revoked && agent.DisabledAt == nil && state.LastSeen > 0 && state.LastSeen >= nowMS-offlineMS
			record.Snapshot.StateStale = !record.Snapshot.Online
			if state.CountryCode != "" {
				record.CountryCode, record.CountrySource = state.CountryCode, "automatic"
			}
		}
		if code := countries[agent.ID]; code != "" {
			record.CountryCode, record.CountrySource = code, "lookup"
		}
		if plan, ok := plans[agent.ID]; ok {
			copy := plan
			record.Plan = &copy
			if plan.CountryCodeOverride != "" {
				record.CountryCode, record.CountrySource = plan.CountryCodeOverride, "manual"
			}
		}
		out.Records = append(out.Records, record)
	}
	return out, tx.Commit()
}

func scanDashboardState(row dashboardScanner) (State, error) {
	s := State{PublicEgress: blankEgressState()}
	var epoch, sequence, uptime, cpuCores, ramUsed, ramTotal, swapUsed, swapTotal, diskUsed, diskTotal, rawRX, rawTX, rxTotal, txTotal, networkCountersVersion int64
	var networkCountersJSON string
	var upgradeCapable, upgradeV2 int
	var cpuSteal, diskReadRate, diskWriteRate, diskBusy sql.NullFloat64
	err := row.Scan(&s.AgentID,
		&epoch, &s.SessionID, &sequence, &s.BootID, &s.Hostname, &s.OS, &s.Arch, &s.AgentVersion, &upgradeCapable, &upgradeV2, &uptime, &s.CPUPercent, &cpuCores, &s.Load1, &s.Load5, &s.Load15, &ramUsed, &ramTotal, &s.RAMPercent, &swapUsed, &swapTotal, &s.SwapPercent, &diskUsed, &diskTotal, &s.DiskPercent, &rawRX, &rawTX, &s.RXRate, &s.TXRate, &rxTotal, &txTotal, &s.CollectedAt, &s.LastSeen, &cpuSteal, &diskReadRate, &diskWriteRate, &diskBusy, &s.CountryCode, &networkCountersVersion, &networkCountersJSON)
	if err != nil {
		return State{}, err
	}
	assignUnsigned(&s, epoch, sequence, uptime, ramUsed, ramTotal, swapUsed, swapTotal, diskUsed, diskTotal, rawRX, rawTX, rxTotal, txTotal)
	s.NetworkCountersVersion, s.NetworkCounters, err = validateStoredNetworkCounters(networkCountersVersion, networkCountersJSON, s.RXBytes, s.TXBytes)
	if err != nil {
		return State{}, err
	}
	s.AgentUpgradeCapable = upgradeCapable == 1
	s.AgentUpgradeV2 = upgradeV2 == 1
	s.CPUCores = uint32(cpuCores)
	s.CPUStealPercent = nullableFloat(cpuSteal)
	s.DiskReadRate = nullableFloat(diskReadRate)
	s.DiskWriteRate = nullableFloat(diskWriteRate)
	s.DiskBusyPercent = nullableFloat(diskBusy)
	return s, nil
}
func scanDashboardPlan(row dashboardScanner) (AgentPlan, error) {
	var plan AgentPlan
	var quotaBytes, bandwidthBPS, cycleStart, cycleEnd, dirSince sql.NullInt64
	err := row.Scan(
		&plan.AgentID, &plan.TrafficMode, &plan.QuotaValue, &plan.QuotaUnit, &quotaBytes,
		&plan.CycleKind, &plan.CycleCount, &plan.CycleAnchor, &plan.Timezone, &plan.UsageBytes, &plan.UsageStatus, &cycleStart, &cycleEnd,
		&plan.BandwidthValue, &plan.BandwidthUnit, &bandwidthBPS, &plan.Currency, &plan.PurchasePrice, &plan.PurchasePricePeriod,
		&plan.RenewalPrice, &plan.RenewalPricePeriod, &plan.PurchaseDate, &plan.RenewalDate, &plan.ExpiryDate, &plan.CountryCodeOverride, &plan.UpdatedAt, &plan.ObserveAfter,
		&plan.CycleRXBytes, &plan.CycleTXBytes, &plan.CycleDirBasis, &dirSince)
	if err != nil {
		return AgentPlan{}, err
	}
	assignNullablePlanValues(&plan, quotaBytes, bandwidthBPS, cycleStart, cycleEnd)
	assignDirectionSince(&plan, dirSince)
	return plan, nil
}
