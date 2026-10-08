package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
)

func newPlanRevision() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func migratePlanRenewalTx(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE agent_plan_config(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,revision TEXT NOT NULL CHECK(length(revision)=32 AND revision NOT GLOB '*[^0-9a-f]*'),renewal_anchor TEXT,expiry_anchor TEXT,latest_operation_id TEXT)`,
		`CREATE TABLE agent_plan_renewal_requests(request_id TEXT PRIMARY KEY CHECK(length(request_id)=32 AND request_id NOT GLOB '*[^0-9a-f]*'),agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,kind TEXT NOT NULL CHECK(kind IN ('apply','undo')),fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),field TEXT NOT NULL CHECK(field IN ('renewal_date','expiry_date')),from_date TEXT NOT NULL,to_date TEXT NOT NULL,prior_revision TEXT NOT NULL,result_revision TEXT NOT NULL,operation_id TEXT NOT NULL,created_at INTEGER NOT NULL,undo_until INTEGER NOT NULL,expires_at INTEGER NOT NULL)`,
		`CREATE INDEX idx_plan_renewal_agent ON agent_plan_renewal_requests(agent_id,created_at,request_id)`,
		`CREATE INDEX idx_plan_renewal_expiry ON agent_plan_renewal_requests(expires_at,request_id)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	// Close rows before issuing statements on the single SQLite connection.
	rows, err := tx.QueryContext(ctx, `SELECT agent_id,COALESCE(purchase_date,''),COALESCE(renewal_date,''),COALESCE(expiry_date,''),COALESCE(renewal_price_period,'') FROM agent_plans`)
	if err != nil {
		return err
	}
	var plans []AgentPlan
	for rows.Next() {
		var p AgentPlan
		if err := rows.Scan(&p.AgentID, &p.PurchaseDate, &p.RenewalDate, &p.ExpiryDate, &p.RenewalPricePeriod); err != nil {
			rows.Close()
			return err
		}
		plans = append(plans, p)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, p := range plans {
		if err := replacePlanConfigTx(ctx, tx, p); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(24,unixepoch())`)
	return err
}

func replacePlanConfigTx(ctx context.Context, tx *sql.Tx, p AgentPlan) error {
	revision, err := newPlanRevision()
	if err != nil {
		return err
	}
	// An absent agent's historical empty PUT remains a harmless no-op.
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_plan_config(agent_id,revision,renewal_anchor,expiry_anchor,latest_operation_id)
	 SELECT id,?,?,?,NULL FROM agents WHERE id=? ON CONFLICT(agent_id) DO UPDATE SET revision=excluded.revision,renewal_anchor=excluded.renewal_anchor,expiry_anchor=excluded.expiry_anchor,latest_operation_id=NULL`,
		revision, nullable(planRenewalAnchor(p, p.RenewalDate)), nullable(planRenewalAnchor(p, p.ExpiryDate)), p.AgentID)
	return err
}
