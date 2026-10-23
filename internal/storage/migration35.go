package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"
)

// migration35FailAt lets tests inject a failure at a named stage; it is nil in production.
var migration35FailAt func(stage string) error

// migration35Clock is the clock of the migration (the "since" mark of plans that cannot be backfilled).
var migration35Clock = time.Now

func migration35Stage(stage string) error {
	if migration35FailAt != nil {
		return migration35FailAt(stage)
	}
	return nil
}

const (
	// QualitySlowThresholdMin, QualitySlowThresholdMax and QualitySlowThresholdDefault bound the per card
	// "slow connection" threshold in whole milliseconds (design 1.7).
	QualitySlowThresholdMin     = 50
	QualitySlowThresholdMax     = 3000
	QualitySlowThresholdDefault = 500

	planDirExact    = "exact"
	planDirBackfill = "backfill"
	planDirSince    = "since"

	// planBackfillMinTolerance is the floor of the reconciliation tolerance of the per-direction backfill.
	planBackfillMinTolerance = 16 << 20
)

// Migration 35 (v1.0.7), in one transaction in four layers:
//
//	0 structure   the per-direction cycle counters of agent_plans and the slow threshold of quality_config;
//	1 backfill    per-direction counters of the plans that have a cycle (design 2.5);
//	2 retire      the TCP Mini tables (schema 29) go away: R25 judges the stored minute buckets instead;
//	3 validation  invariants of the counters, threshold range, foreign keys of the two altered tables.
//
// Samples, buckets, series, gaps and every other table are never touched.
func migrateV107Tx(ctx context.Context, tx *sql.Tx) error {
	for _, column := range []struct{ table, name, ddl string }{
		{"agent_plans", "cycle_rx_bytes", `ALTER TABLE agent_plans ADD COLUMN cycle_rx_bytes INTEGER NOT NULL DEFAULT 0 CHECK(cycle_rx_bytes >= 0)`},
		{"agent_plans", "cycle_tx_bytes", `ALTER TABLE agent_plans ADD COLUMN cycle_tx_bytes INTEGER NOT NULL DEFAULT 0 CHECK(cycle_tx_bytes >= 0)`},
		{"agent_plans", "cycle_dir_basis", `ALTER TABLE agent_plans ADD COLUMN cycle_dir_basis TEXT NOT NULL DEFAULT 'exact' CHECK(cycle_dir_basis IN ('exact','backfill','since'))`},
		{"agent_plans", "cycle_dir_since", `ALTER TABLE agent_plans ADD COLUMN cycle_dir_since INTEGER`},
		{"quality_config", "slow_threshold_ms", `ALTER TABLE quality_config ADD COLUMN slow_threshold_ms INTEGER NOT NULL DEFAULT 500 CHECK(slow_threshold_ms BETWEEN 50 AND 3000)`},
	} {
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('`+column.table+`') WHERE name=?`, column.name).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := tx.ExecContext(ctx, column.ddl); err != nil {
				return err
			}
		}
	}
	if err := migration35Stage("structure"); err != nil {
		return err
	}

	if err := backfillPlanDirectionsTx(ctx, tx, migration35Clock().UnixMilli()); err != nil {
		return err
	}
	if err := migration35Stage("backfill"); err != nil {
		return err
	}

	for _, table := range []string{"tcp_mini_high", "tcp_mini_state"} {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
			return err
		}
	}
	if err := migration35Stage("retire"); err != nil {
		return err
	}

	if err := verifyPlanDirectionsTx(ctx, tx); err != nil {
		return err
	}
	var outside int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_config WHERE slow_threshold_ms NOT BETWEEN ? AND ?`, QualitySlowThresholdMin, QualitySlowThresholdMax).Scan(&outside); err != nil {
		return err
	}
	if outside != 0 {
		return errors.New("migration 35 check failed: slow threshold out of range")
	}
	for _, table := range []string{"agent_plans", "quality_config"} {
		violations, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
		if err != nil {
			return err
		}
		found := violations.Next()
		err = violations.Err()
		violations.Close()
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("migration 35 foreign key violation in %s", table)
		}
	}
	if err := migration35Stage("validation"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(35,unixepoch())`)
	return err
}

// planDirectionError reports why the per-direction counters of one plan break the invariant of design 2.3
// ("" when they hold). exact and backfill rows are equal to the billed usage; a since row can only be below
// it (the rest of the usage has no direction on record).
func planDirectionError(mode, basis string, usage, rx, tx uint64) string {
	if usage > math.MaxInt64 || rx > math.MaxInt64 || tx > math.MaxInt64 {
		return "a counter exceeds the storage range"
	}
	var billed uint64
	switch mode {
	case "rx":
		billed = rx
	case "tx":
		billed = tx
	case "sum":
		billed = rx + tx
	case "":
		if rx != 0 || tx != 0 {
			return "counters without a billing mode"
		}
		return ""
	default:
		return "unknown billing mode"
	}
	switch basis {
	case planDirExact, planDirBackfill:
		if billed != usage {
			return fmt.Sprintf("billed direction %d is not the usage %d", billed, usage)
		}
	case planDirSince:
		if billed > usage {
			return fmt.Sprintf("billed direction %d is above the usage %d", billed, usage)
		}
	default:
		return "unknown basis"
	}
	return ""
}

func verifyPlanDirectionsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,COALESCE(traffic_mode,''),usage_bytes,cycle_rx_bytes,cycle_tx_bytes,cycle_dir_basis FROM agent_plans`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, mode, basis string
		var usage, rx, tx int64
		if err := rows.Scan(&id, &mode, &usage, &rx, &tx, &basis); err != nil {
			return err
		}
		if usage < 0 || rx < 0 || tx < 0 {
			return fmt.Errorf("migration 35 check failed: negative plan counter of %s", id)
		}
		if reason := planDirectionError(mode, basis, uint64(usage), uint64(rx), uint64(tx)); reason != "" {
			return fmt.Errorf("migration 35 check failed: plan %s: %s", id, reason)
		}
	}
	return rows.Err()
}

type planBackfillRow struct {
	agentID    string
	mode       string
	usage      uint64
	status     string
	cycleStart sql.NullInt64
}

// backfillPlanDirectionsTx fills the per-direction counters of every plan (design 2.5). A plan whose
// minute data cannot be reconciled with its billed usage starts counting from now ("since") instead of
// receiving invented numbers; a plan is never left in a state the final check would reject.
func backfillPlanDirectionsTx(ctx context.Context, tx *sql.Tx, nowMillis int64) error {
	var plans []planBackfillRow
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,COALESCE(traffic_mode,''),usage_bytes,usage_status,cycle_start FROM agent_plans ORDER BY agent_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var row planBackfillRow
		var usage int64
		if err := rows.Scan(&row.agentID, &row.mode, &usage, &row.status, &row.cycleStart); err != nil {
			rows.Close()
			return err
		}
		if usage < 0 {
			rows.Close()
			return fmt.Errorf("plan %s has a negative usage", row.agentID)
		}
		row.usage = uint64(usage)
		plans = append(plans, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, plan := range plans {
		if plan.mode == "" {
			continue // no billing: nothing is counted, the defaults (0, 0, exact) stay
		}
		rx, txBytes, basis, since, err := planDirectionsFromMinutesTx(ctx, tx, plan, nowMillis)
		if err != nil {
			return err
		}
		if basis == planDirExact {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_plans SET cycle_rx_bytes=?,cycle_tx_bytes=?,cycle_dir_basis=?,cycle_dir_since=? WHERE agent_id=?`,
			int64(rx), int64(txBytes), basis, since, plan.agentID); err != nil {
			return err
		}
	}
	return nil
}

// planDirectionsFromMinutesTx decides the starting counters of one plan: reconciled from minute_metrics
// ("backfill") or counting from now ("since"). "exact" is returned only for a plan with no usage to explain.
func planDirectionsFromMinutesTx(ctx context.Context, tx *sql.Tx, plan planBackfillRow, nowMillis int64) (rx, txBytes uint64, basis string, since int64, err error) {
	if !plan.cycleStart.Valid {
		if plan.usage == 0 && plan.status != "calibrated" {
			return 0, 0, planDirExact, 0, nil
		}
		return 0, 0, planDirSince, nowMillis, nil
	}
	fallback := func() (uint64, uint64, string, int64, error) { return 0, 0, planDirSince, nowMillis, nil }
	if plan.status == "calibrated" {
		return fallback()
	}
	var endRX, endTX int64
	err = tx.QueryRowContext(ctx, `SELECT rx_total,tx_total FROM agent_state WHERE agent_id=?`, plan.agentID).Scan(&endRX, &endTX)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback()
	}
	if err != nil {
		return 0, 0, "", 0, err
	}
	var anchorBucket, anchorRX, anchorTX int64
	err = tx.QueryRowContext(ctx, `SELECT bucket,rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket<? ORDER BY bucket DESC LIMIT 1`, plan.agentID, plan.cycleStart.Int64).Scan(&anchorBucket, &anchorRX, &anchorTX)
	if errors.Is(err, sql.ErrNoRows) {
		// The cycle began before the kept history (or the device joined inside it): the first minute of the
		// cycle is the best anchor and the numbers start there.
		err = tx.QueryRowContext(ctx, `SELECT bucket,rx_total,tx_total FROM minute_metrics WHERE agent_id=? AND bucket>=? ORDER BY bucket LIMIT 1`, plan.agentID, plan.cycleStart.Int64).Scan(&anchorBucket, &anchorRX, &anchorTX)
		if errors.Is(err, sql.ErrNoRows) {
			return fallback()
		}
	}
	if err != nil {
		return 0, 0, "", 0, err
	}
	if endRX < anchorRX || endTX < anchorTX || anchorRX < 0 || anchorTX < 0 {
		return fallback() // a counter went backwards: no honest per-direction number
	}
	rx, txBytes, ok := reconcilePlanDirections(plan.mode, plan.usage, uint64(endRX-anchorRX), uint64(endTX-anchorTX))
	if !ok || planDirectionError(plan.mode, planDirBackfill, plan.usage, rx, txBytes) != "" {
		return fallback()
	}
	return rx, txBytes, planDirBackfill, anchorBucket, nil
}

// reconcilePlanDirections turns the traffic seen in the minute data (rxB, txB) into per-direction counters
// that agree with the billed usage, or reports that they cannot be reconciled. Everything is integer
// arithmetic; the proportional share uses a 128-bit-safe big integer and rounds down (design 2.5).
func reconcilePlanDirections(mode string, usage, rxB, txB uint64) (rx, tx uint64, ok bool) {
	if usage > math.MaxInt64 || rxB > math.MaxInt64 || txB > math.MaxInt64 {
		return 0, 0, false
	}
	tolerance := usage / 100
	if tolerance < planBackfillMinTolerance {
		tolerance = planBackfillMinTolerance
	}
	within := func(a, b uint64) bool {
		if a > b {
			return a-b <= tolerance
		}
		return b-a <= tolerance
	}
	switch mode {
	case "rx":
		if !within(rxB, usage) {
			return 0, 0, false
		}
		return usage, txB, true
	case "tx":
		if !within(txB, usage) {
			return 0, 0, false
		}
		return rxB, usage, true
	case "sum":
		sum := rxB + txB // both are at most MaxInt64, so this cannot wrap
		if sum == 0 {
			// No direction evidence at all: an empty usage is reconciled as it is, a used one is not.
			return 0, 0, usage == 0
		}
		if !within(sum, usage) {
			return 0, 0, false
		}
		residual := new(big.Int).Sub(new(big.Int).SetUint64(usage), new(big.Int).SetUint64(sum))
		share := new(big.Int).Mul(residual, new(big.Int).SetUint64(rxB))
		// Euclidean division by a positive divisor is the floor division the design asks for.
		share.Div(share, new(big.Int).SetUint64(sum))
		share.Add(share, new(big.Int).SetUint64(rxB))
		if share.Sign() < 0 || !share.IsUint64() || share.Uint64() > usage {
			return 0, 0, false
		}
		rx = share.Uint64()
		return rx, usage - rx, true
	}
	return 0, 0, false
}
