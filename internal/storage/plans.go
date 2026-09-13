package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
	_ "time/tzdata"

	"404-probe/internal/protocol"
)

type AgentPlan struct {
	AgentID             string  `json:"agent_id"`
	TrafficMode         string  `json:"traffic_mode,omitempty"`
	QuotaValue          string  `json:"quota_value,omitempty"`
	QuotaUnit           string  `json:"quota_unit,omitempty"`
	QuotaBytes          *uint64 `json:"quota_bytes,omitempty"`
	CycleKind           string  `json:"cycle_kind,omitempty"`
	CycleCount          int     `json:"cycle_count,omitempty"`
	CycleAnchor         string  `json:"cycle_anchor,omitempty"`
	Timezone            string  `json:"timezone,omitempty"`
	UsageBytes          uint64  `json:"usage_bytes"`
	UsageStatus         string  `json:"usage_status,omitempty"`
	CycleStart          *int64  `json:"cycle_start,omitempty"`
	CycleEnd            *int64  `json:"cycle_end,omitempty"`
	BandwidthValue      string  `json:"bandwidth_value,omitempty"`
	BandwidthUnit       string  `json:"bandwidth_unit,omitempty"`
	BandwidthBPS        *uint64 `json:"bandwidth_bps,omitempty"`
	Currency            string  `json:"currency,omitempty"`
	PurchasePrice       string  `json:"purchase_price,omitempty"`
	PurchasePricePeriod string  `json:"purchase_price_period,omitempty"`
	RenewalPrice        string  `json:"renewal_price,omitempty"`
	RenewalPricePeriod  string  `json:"renewal_price_period,omitempty"`
	PurchaseDate        string  `json:"purchase_date,omitempty"`
	RenewalDate         string  `json:"renewal_date,omitempty"`
	ExpiryDate          string  `json:"expiry_date,omitempty"`
	CountryCodeOverride string  `json:"country_code_override,omitempty"`
	UpdatedAt           int64   `json:"updated_at"`
	ObserveAfter        int64   `json:"-"`
}

func (s *Store) GetAgentPlan(ctx context.Context, agentID string) (AgentPlan, bool, error) {
	var plan AgentPlan
	var quotaBytes, bandwidthBPS, cycleStart, cycleEnd sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,COALESCE(traffic_mode,''),COALESCE(quota_value,''),COALESCE(quota_unit,''),quota_bytes,
		COALESCE(cycle_kind,''),COALESCE(cycle_count,0),COALESCE(cycle_anchor,''),COALESCE(timezone,''),usage_bytes,usage_status,cycle_start,cycle_end,
		COALESCE(bandwidth_value,''),COALESCE(bandwidth_unit,''),bandwidth_bps,COALESCE(currency,''),COALESCE(purchase_price,''),COALESCE(purchase_price_period,''),
		COALESCE(renewal_price,''),COALESCE(renewal_price_period,''),COALESCE(purchase_date,''),COALESCE(renewal_date,''),COALESCE(expiry_date,''),COALESCE(country_code_override,''),updated_at,observe_after
		FROM agent_plans WHERE agent_id=?`, agentID).Scan(
		&plan.AgentID, &plan.TrafficMode, &plan.QuotaValue, &plan.QuotaUnit, &quotaBytes,
		&plan.CycleKind, &plan.CycleCount, &plan.CycleAnchor, &plan.Timezone, &plan.UsageBytes, &plan.UsageStatus, &cycleStart, &cycleEnd,
		&plan.BandwidthValue, &plan.BandwidthUnit, &bandwidthBPS, &plan.Currency, &plan.PurchasePrice, &plan.PurchasePricePeriod,
		&plan.RenewalPrice, &plan.RenewalPricePeriod, &plan.PurchaseDate, &plan.RenewalDate, &plan.ExpiryDate, &plan.CountryCodeOverride, &plan.UpdatedAt, &plan.ObserveAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentPlan{}, false, nil
	}
	if err != nil {
		return AgentPlan{}, false, err
	}
	assignNullablePlanValues(&plan, quotaBytes, bandwidthBPS, cycleStart, cycleEnd)
	return plan, true, nil
}

func (s *Store) DeleteAgentPlan(ctx context.Context, agentID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_plans WHERE agent_id=?`, agentID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func assignNullablePlanValues(plan *AgentPlan, quotaBytes, bandwidthBPS, cycleStart, cycleEnd sql.NullInt64) {
	if quotaBytes.Valid {
		value := uint64(quotaBytes.Int64)
		plan.QuotaBytes = &value
	}
	if bandwidthBPS.Valid {
		value := uint64(bandwidthBPS.Int64)
		plan.BandwidthBPS = &value
	}
	if cycleStart.Valid {
		value := cycleStart.Int64
		plan.CycleStart = &value
	}
	if cycleEnd.Valid {
		value := cycleEnd.Int64
		plan.CycleEnd = &value
	}
}

func (s *Store) PutAgentPlan(ctx context.Context, plan AgentPlan, calibration *uint64, now time.Time) (AgentPlan, error) {
	if plan.AgentID == "" {
		return AgentPlan{}, errors.New("agent ID is required")
	}
	if err := validatePlanStorageRange(plan); err != nil {
		return AgentPlan{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentPlan{}, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND revoked=0)`, plan.AgentID).Scan(&exists); err != nil {
		return AgentPlan{}, err
	}
	if !exists {
		return AgentPlan{}, ErrAgentNotFound
	}
	previous, previousExists, err := getAgentPlanTx(ctx, tx, plan.AgentID)
	if err != nil {
		return AgentPlan{}, err
	}
	start, end, active, err := PlanCycleBounds(plan, now)
	if err != nil {
		return AgentPlan{}, err
	}
	usage, status, observeAfter := uint64(0), "partial", int64(0)
	policyUnchanged := previousExists && previous.TrafficMode == plan.TrafficMode && previous.CycleKind == plan.CycleKind && previous.CycleCount == plan.CycleCount && previous.CycleAnchor == plan.CycleAnchor && previous.Timezone == plan.Timezone && sameInt64(previous.CycleStart, start)
	if policyUnchanged {
		usage, status, observeAfter = previous.UsageBytes, previous.UsageStatus, previous.ObserveAfter
	}
	if calibration != nil {
		usage, status = *calibration, "calibrated"
	}
	if plan.TrafficMode != "" && (!policyUnchanged || calibration != nil) {
		// A report delta spanning this edit cannot be split honestly between
		// traffic before and after the user's accounting boundary.
		observeAfter = now.UnixMilli()
	}
	if !active {
		start, end = nil, nil
	}
	if usage > math.MaxInt64 {
		return AgentPlan{}, errors.New("traffic calibration exceeds storage range")
	}
	plan.UsageBytes, plan.UsageStatus, plan.CycleStart, plan.CycleEnd, plan.UpdatedAt, plan.ObserveAfter = usage, status, start, end, now.UnixMilli(), observeAfter
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_plans(agent_id,traffic_mode,quota_value,quota_unit,quota_bytes,cycle_kind,cycle_count,cycle_anchor,timezone,usage_bytes,usage_status,cycle_start,cycle_end,bandwidth_value,bandwidth_unit,bandwidth_bps,currency,purchase_price,purchase_price_period,renewal_price,renewal_price_period,purchase_date,renewal_date,expiry_date,country_code_override,updated_at,observe_after)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(agent_id) DO UPDATE SET traffic_mode=excluded.traffic_mode,quota_value=excluded.quota_value,quota_unit=excluded.quota_unit,quota_bytes=excluded.quota_bytes,cycle_kind=excluded.cycle_kind,cycle_count=excluded.cycle_count,cycle_anchor=excluded.cycle_anchor,timezone=excluded.timezone,usage_bytes=excluded.usage_bytes,usage_status=excluded.usage_status,cycle_start=excluded.cycle_start,cycle_end=excluded.cycle_end,bandwidth_value=excluded.bandwidth_value,bandwidth_unit=excluded.bandwidth_unit,bandwidth_bps=excluded.bandwidth_bps,currency=excluded.currency,purchase_price=excluded.purchase_price,purchase_price_period=excluded.purchase_price_period,renewal_price=excluded.renewal_price,renewal_price_period=excluded.renewal_price_period,purchase_date=excluded.purchase_date,renewal_date=excluded.renewal_date,expiry_date=excluded.expiry_date,country_code_override=excluded.country_code_override,updated_at=excluded.updated_at,observe_after=excluded.observe_after`,
		plan.AgentID, nullable(plan.TrafficMode), nullable(plan.QuotaValue), nullable(plan.QuotaUnit), nullableUint(plan.QuotaBytes), nullable(plan.CycleKind), nullableInt(plan.CycleCount), nullable(plan.CycleAnchor), nullable(plan.Timezone), int64(plan.UsageBytes), plan.UsageStatus, nullableInt64(plan.CycleStart), nullableInt64(plan.CycleEnd), nullable(plan.BandwidthValue), nullable(plan.BandwidthUnit), nullableUint(plan.BandwidthBPS), nullable(plan.Currency), nullable(plan.PurchasePrice), nullable(plan.PurchasePricePeriod), nullable(plan.RenewalPrice), nullable(plan.RenewalPricePeriod), nullable(plan.PurchaseDate), nullable(plan.RenewalDate), nullable(plan.ExpiryDate), nullable(plan.CountryCodeOverride), plan.UpdatedAt, plan.ObserveAfter)
	if err != nil {
		return AgentPlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentPlan{}, err
	}
	return plan, nil
}

func accountPlanTrafficTx(ctx context.Context, tx *sql.Tx, old, current State, existed bool, now time.Time) error {
	plan, found, err := getAgentPlanTx(ctx, tx, current.AgentID)
	if err != nil || !found || plan.TrafficMode == "" || plan.CycleKind == "" || plan.CycleKind == "none" {
		return err
	}
	start, end, active, err := PlanCycleBounds(plan, now)
	if err != nil || !active {
		return err
	}
	usage, status := plan.UsageBytes, plan.UsageStatus
	if !sameInt64(plan.CycleStart, start) {
		usage, status = 0, "partial"
	}
	spansAccountingBoundary := existed && plan.ObserveAfter > 0 && old.LastSeen < plan.ObserveAfter
	if existed && old.LastSeen >= *start && !spansAccountingBoundary {
		rxDelta, txDelta := current.RXTotal-old.RXTotal, current.TXTotal-old.TXTotal
		var increment uint64
		switch plan.TrafficMode {
		case "rx":
			increment = rxDelta
		case "tx":
			increment = txDelta
		case "sum":
			if rxDelta > math.MaxUint64-txDelta {
				return errors.New("traffic increment overflow")
			}
			increment = rxDelta + txDelta
		}
		if usage > math.MaxInt64-increment {
			return errors.New("billing traffic counter exceeds storage range")
		}
		usage += increment
		if old.BootID != current.BootID || current.RXBytes < old.RXBytes || current.TXBytes < old.TXBytes {
			status = "partial"
		}
	}
	if spansAccountingBoundary {
		status = "partial"
	}
	observeAfter := plan.ObserveAfter
	if observeAfter > 0 && current.LastSeen >= observeAfter {
		observeAfter = 0
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_plans SET usage_bytes=?,usage_status=?,cycle_start=?,cycle_end=?,updated_at=?,observe_after=? WHERE agent_id=?`, int64(usage), status, *start, *end, now.UnixMilli(), observeAfter, current.AgentID)
	return err
}

// PlanCycleBounds returns calendar boundaries in the plan's IANA timezone.
// Month-end anchors are clamped per month without changing the original day.
func PlanCycleBounds(plan AgentPlan, now time.Time) (*int64, *int64, bool, error) {
	if plan.CycleKind == "" || plan.CycleKind == "none" || plan.TrafficMode == "" {
		return nil, nil, false, nil
	}
	location, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return nil, nil, false, errors.New("invalid billing timezone")
	}
	anchor, err := time.ParseInLocation("2006-01-02", plan.CycleAnchor, location)
	if err != nil || plan.CycleCount < 1 {
		return nil, nil, false, errors.New("invalid billing cycle")
	}
	localNow := now.In(location)
	if localNow.Before(anchor) {
		return nil, nil, false, nil
	}
	var start, end time.Time
	switch plan.CycleKind {
	case "monthly", "yearly":
		step := plan.CycleCount
		if plan.CycleKind == "yearly" {
			step *= 12
		}
		months := (localNow.Year()-anchor.Year())*12 + int(localNow.Month()-anchor.Month())
		index := months / step
		start = anchoredMonth(anchor, index*step)
		if start.After(localNow) {
			index--
			start = anchoredMonth(anchor, index*step)
		}
		end = anchoredMonth(anchor, (index+1)*step)
	case "days":
		anchorOrdinal := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
		nowOrdinal := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
		days := int(nowOrdinal.Sub(anchorOrdinal).Hours() / 24)
		index := days / plan.CycleCount
		start = anchor.AddDate(0, 0, index*plan.CycleCount)
		end = anchor.AddDate(0, 0, (index+1)*plan.CycleCount)
	default:
		return nil, nil, false, errors.New("invalid billing cycle kind")
	}
	startMillis, endMillis := start.UTC().UnixMilli(), end.UTC().UnixMilli()
	return &startMillis, &endMillis, true, nil
}

func AgentPlanAt(plan AgentPlan, now time.Time) (AgentPlan, error) {
	start, end, active, err := PlanCycleBounds(plan, now)
	if err != nil {
		return AgentPlan{}, err
	}
	if !active {
		plan.CycleStart, plan.CycleEnd = nil, nil
		return plan, nil
	}
	if !sameInt64(plan.CycleStart, start) {
		plan.UsageBytes = 0
		plan.UsageStatus = "partial"
	}
	plan.CycleStart, plan.CycleEnd = start, end
	return plan, nil
}

func anchoredMonth(anchor time.Time, offset int) time.Time {
	first := time.Date(anchor.Year(), anchor.Month()+time.Month(offset), 1, 0, 0, 0, 0, anchor.Location())
	lastDay := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, anchor.Location()).Day()
	day := anchor.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, anchor.Location())
}

func getAgentPlanTx(ctx context.Context, tx *sql.Tx, agentID string) (AgentPlan, bool, error) {
	var plan AgentPlan
	var start, end sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT agent_id,COALESCE(traffic_mode,''),COALESCE(cycle_kind,''),COALESCE(cycle_count,0),COALESCE(cycle_anchor,''),COALESCE(timezone,''),usage_bytes,usage_status,cycle_start,cycle_end,observe_after FROM agent_plans WHERE agent_id=?`, agentID).Scan(&plan.AgentID, &plan.TrafficMode, &plan.CycleKind, &plan.CycleCount, &plan.CycleAnchor, &plan.Timezone, &plan.UsageBytes, &plan.UsageStatus, &start, &end, &plan.ObserveAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentPlan{}, false, nil
	}
	if err != nil {
		return AgentPlan{}, false, err
	}
	assignNullablePlanValues(&plan, sql.NullInt64{}, sql.NullInt64{}, start, end)
	return plan, true, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableUint(value *uint64) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func sameInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func validatePlanStorageRange(plan AgentPlan) error {
	if plan.CountryCodeOverride != "" && !protocol.ValidCountryCode(plan.CountryCodeOverride) {
		return errors.New("country override must be a supported uppercase ISO alpha-2 code")
	}
	for _, value := range []*uint64{plan.QuotaBytes, plan.BandwidthBPS} {
		if value != nil && *value > math.MaxInt64 {
			return fmt.Errorf("plan value exceeds storage range")
		}
	}
	return nil
}
