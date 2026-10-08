package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const PlanRenewalRetention = 7 * 24 * time.Hour
const PlanRenewalUndoWindow = 5 * time.Minute

// CleanupPlanRenewalRequestsBatch never scans or deletes more than one indexed
// expiry batch. It does not change configuration fences or any accounting data.
func (s *Store) CleanupPlanRenewalRequestsBatch(ctx context.Context, now time.Time) (int64, bool, error) {
	if now.IsZero() {
		return 0, false, renewalError("invalid_request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM agent_plan_renewal_requests WHERE request_id IN (SELECT request_id FROM agent_plan_renewal_requests INDEXED BY idx_plan_renewal_expiry WHERE expires_at<=? ORDER BY expires_at,request_id LIMIT 500)`, now.UnixMilli())
	if err != nil {
		return 0, false, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return deleted, deleted == 500, nil
}

type PlanRenewalError struct {
	Code       string
	RetryAfter int64
}

func (e *PlanRenewalError) Error() string { return e.Code }
func renewalError(code string) error      { return &PlanRenewalError{Code: code} }

type PlanRenewalPreview struct {
	Eligible         bool   `json:"eligible"`
	Reason           string `json:"reason,omitempty"`
	EditPlan         bool   `json:"edit_plan,omitempty"`
	Field            string `json:"field,omitempty"`
	FromDate         string `json:"from_date,omitempty"`
	ToDate           string `json:"to_date,omitempty"`
	UnchangedField   string `json:"unchanged_field,omitempty"`
	UnchangedDate    string `json:"unchanged_date,omitempty"`
	Period           string `json:"period,omitempty"`
	Timezone         string `json:"timezone,omitempty"`
	AnchorSource     string `json:"anchor_source,omitempty"`
	ExpectedRevision string `json:"expected_revision,omitempty"`
	NewOverdue       bool   `json:"new_overdue"`
	DueToday         bool   `json:"due_today"`
}

type PlanRenewalApply struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision string `json:"expected_revision"`
	Field            string `json:"field"`
	FromDate         string `json:"from_date"`
	ToDate           string `json:"to_date"`
	NewOverdue       bool   `json:"new_overdue"`
	DueToday         bool   `json:"due_today"`
}
type PlanRenewalUndo struct {
	RequestID        string `json:"request_id"`
	OperationID      string `json:"operation_id"`
	ExpectedRevision string `json:"expected_revision"`
}
type PlanRenewalOperation struct {
	RequestID string `json:"request_id"`
	Kind      string `json:"kind"`
	Field     string `json:"field"`
	FromDate  string `json:"from_date"`
	ToDate    string `json:"to_date"`
	AppliedAt int64  `json:"applied_at"`
	UndoUntil int64  `json:"undo_until,omitempty"`
}
type PlanRenewalUndoAvailability struct {
	Available   bool   `json:"available"`
	OperationID string `json:"operation_id,omitempty"`
}
type PlanRenewalResult struct {
	Operation       PlanRenewalOperation        `json:"operation"`
	Replayed        bool                        `json:"replayed"`
	CurrentPlan     *AgentPlan                  `json:"current_plan"`
	CurrentRevision string                      `json:"current_revision"`
	Undo            PlanRenewalUndoAvailability `json:"undo"`
}
type planRenewalConfig struct{ revision, renewalAnchor, expiryAnchor, latest string }
type planRenewalReceipt struct {
	operation                                      PlanRenewalOperation
	agent, fingerprint, prior, result, operationID string
	expires                                        int64
}

func planDate(value string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", value)
	if err != nil || t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}, renewalError("invalid_date")
	}
	return t, nil
}
func planRenewalAnchor(p AgentPlan, date string) string {
	d, err := planDate(date)
	if err != nil {
		return ""
	}
	a, err := planDate(p.PurchaseDate)
	if err != nil || a.After(d) {
		return date
	}
	months := (d.Year()-a.Year())*12 + int(d.Month()-a.Month())
	if (p.RenewalPricePeriod == "monthly" || (p.RenewalPricePeriod == "yearly" && months%12 == 0)) && anchoredMonth(a, months).Equal(d) {
		return p.PurchaseDate
	}
	return date
}
func planSelectedDate(p AgentPlan, field string) string {
	if field == "renewal_date" {
		return p.RenewalDate
	}
	return p.ExpiryDate
}
func renewalPreview(p *AgentPlan, c planRenewalConfig, field string, now time.Time) (PlanRenewalPreview, error) {
	out := PlanRenewalPreview{EditPlan: true}
	if field != "" && field != "renewal_date" && field != "expiry_date" {
		return out, renewalError("invalid_field")
	}
	if p == nil {
		out.Reason = "plan_missing"
		return out, nil
	}
	if field == "" {
		field = "renewal_date"
		if p.RenewalDate == "" {
			field = "expiry_date"
		}
	}
	out.Field = field
	out.FromDate = planSelectedDate(*p, field)
	if out.FromDate == "" {
		out.Reason = "date_missing"
		return out, nil
	}
	if p.RenewalPricePeriod == "" {
		out.Reason = "period_missing"
		return out, nil
	}
	if p.RenewalPricePeriod == "once" {
		out.Reason = "one_time"
		return out, nil
	}
	step := 1
	if p.RenewalPricePeriod == "yearly" {
		step = 12
	} else if p.RenewalPricePeriod != "monthly" {
		return out, renewalError("invalid_period")
	}
	d, err := planDate(out.FromDate)
	if err != nil {
		return out, err
	}
	anchor := c.renewalAnchor
	if field == "expiry_date" {
		anchor = c.expiryAnchor
	}
	a, err := planDate(anchor)
	if err != nil {
		return out, renewalError("invalid_anchor")
	}
	out.AnchorSource = "current_date"
	if anchor == p.PurchaseDate {
		out.AnchorSource = "purchase_date"
	}
	out.Timezone = p.Timezone
	if out.Timezone == "" {
		out.Timezone = "UTC"
	}
	location, err := time.LoadLocation(out.Timezone)
	if err != nil {
		return out, renewalError("invalid_timezone")
	}
	months := (d.Year()-a.Year())*12 + int(d.Month()-a.Month())
	target := anchoredMonth(a, months+step)
	if target.Year() < 1 || target.Year() > 9999 {
		return out, renewalError("date_out_of_range")
	}
	out.ToDate = target.Format("2006-01-02")
	today := now.In(location).Format("2006-01-02")
	out.NewOverdue = out.ToDate < today
	out.DueToday = out.ToDate == today
	out.UnchangedField = "expiry_date"
	out.UnchangedDate = p.ExpiryDate
	if field == "expiry_date" {
		out.UnchangedField = "renewal_date"
		out.UnchangedDate = p.RenewalDate
	}
	out.Period = p.RenewalPricePeriod
	out.ExpectedRevision = c.revision
	out.Eligible = true
	out.EditPlan = false
	return out, nil
}

// Full transaction reader, intentionally independent of the traffic-only reader.
func readRenewalPlanTx(ctx context.Context, tx *sql.Tx, id string) (*AgentPlan, planRenewalConfig, error) {
	var p AgentPlan
	var c planRenewalConfig
	var quota, bandwidth, start, end sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT agent_id,COALESCE(traffic_mode,''),COALESCE(quota_value,''),COALESCE(quota_unit,''),quota_bytes,COALESCE(cycle_kind,''),COALESCE(cycle_count,0),COALESCE(cycle_anchor,''),COALESCE(timezone,''),usage_bytes,usage_status,cycle_start,cycle_end,COALESCE(bandwidth_value,''),COALESCE(bandwidth_unit,''),bandwidth_bps,COALESCE(currency,''),COALESCE(purchase_price,''),COALESCE(purchase_price_period,''),COALESCE(renewal_price,''),COALESCE(renewal_price_period,''),COALESCE(purchase_date,''),COALESCE(renewal_date,''),COALESCE(expiry_date,''),COALESCE(country_code_override,''),updated_at,observe_after FROM agent_plans WHERE agent_id=?`, id).Scan(&p.AgentID, &p.TrafficMode, &p.QuotaValue, &p.QuotaUnit, &quota, &p.CycleKind, &p.CycleCount, &p.CycleAnchor, &p.Timezone, &p.UsageBytes, &p.UsageStatus, &start, &end, &p.BandwidthValue, &p.BandwidthUnit, &bandwidth, &p.Currency, &p.PurchasePrice, &p.PurchasePricePeriod, &p.RenewalPrice, &p.RenewalPricePeriod, &p.PurchaseDate, &p.RenewalDate, &p.ExpiryDate, &p.CountryCodeOverride, &p.UpdatedAt, &p.ObserveAfter)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, c, err
	}
	found := err == nil
	err = tx.QueryRowContext(ctx, `SELECT revision,COALESCE(renewal_anchor,''),COALESCE(expiry_anchor,''),COALESCE(latest_operation_id,'') FROM agent_plan_config WHERE agent_id=?`, id).Scan(&c.revision, &c.renewalAnchor, &c.expiryAnchor, &c.latest)
	if errors.Is(err, sql.ErrNoRows) && !found {
		return nil, c, nil
	}
	if err != nil {
		return nil, c, err
	}
	if !found {
		return nil, c, nil
	}
	assignNullablePlanValues(&p, quota, bandwidth, start, end)
	return &p, c, nil
}
func requireRenewalAgentTx(ctx context.Context, tx *sql.Tx, id string) error {
	var revoked bool
	err := tx.QueryRowContext(ctx, `SELECT revoked FROM agents WHERE id=?`, id).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) || revoked {
		return ErrAgentNotFound
	}
	if err != nil {
		return err
	}
	return requireNoAgentRemovalTx(ctx, tx, id)
}
func (s *Store) PreviewPlanRenewal(ctx context.Context, id, field string, now time.Time) (PlanRenewalPreview, error) {
	if !validStorageID(id, 128) || now.IsZero() {
		return PlanRenewalPreview{}, renewalError("invalid_request")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanRenewalPreview{}, err
	}
	defer tx.Rollback()
	if err := requireRenewalAgentTx(ctx, tx, id); err != nil {
		return PlanRenewalPreview{}, err
	}
	p, c, err := readRenewalPlanTx(ctx, tx, id)
	if err != nil {
		return PlanRenewalPreview{}, err
	}
	return renewalPreview(p, c, field, now)
}
func renewalFingerprint(id, kind string, payload any) string {
	b, _ := json.Marshal(struct {
		Agent, Kind string
		Payload     any
	}{id, kind, payload})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func readRenewalReceiptTx(ctx context.Context, tx *sql.Tx, id string) (planRenewalReceipt, bool, error) {
	var r planRenewalReceipt
	err := tx.QueryRowContext(ctx, `SELECT request_id,agent_id,kind,fingerprint,field,from_date,to_date,prior_revision,result_revision,operation_id,created_at,undo_until,expires_at FROM agent_plan_renewal_requests WHERE request_id=?`, id).Scan(&r.operation.RequestID, &r.agent, &r.operation.Kind, &r.fingerprint, &r.operation.Field, &r.operation.FromDate, &r.operation.ToDate, &r.prior, &r.result, &r.operationID, &r.operation.AppliedAt, &r.operation.UndoUntil, &r.expires)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}
func undoReceiptValid(r planRenewalReceipt, p *AgentPlan, c planRenewalConfig, now time.Time) bool {
	stamp := now.UnixMilli()
	return p != nil && r.operation.Kind == "apply" && c.latest == r.operation.RequestID && c.revision == r.result && planSelectedDate(*p, r.operation.Field) == r.operation.ToDate && stamp >= r.operation.AppliedAt && stamp < r.operation.UndoUntil
}
func renewalResultTx(ctx context.Context, tx *sql.Tx, r planRenewalReceipt, replay bool, now time.Time) (PlanRenewalResult, error) {
	p, c, err := readRenewalPlanTx(ctx, tx, r.agent)
	if err != nil {
		return PlanRenewalResult{}, err
	}
	out := PlanRenewalResult{Operation: r.operation, Replayed: replay, CurrentPlan: p, CurrentRevision: c.revision}
	if c.latest != "" {
		latest, found, err := readRenewalReceiptTx(ctx, tx, c.latest)
		if err != nil {
			return out, err
		}
		if found && undoReceiptValid(latest, p, c, now) {
			out.Undo = PlanRenewalUndoAvailability{true, c.latest}
		}
	}
	return out, nil
}
func (s *Store) ApplyPlanRenewal(ctx context.Context, id string, q PlanRenewalApply, now time.Time) (PlanRenewalResult, error) {
	if q.Field != "renewal_date" && q.Field != "expiry_date" {
		return PlanRenewalResult{}, renewalError("invalid_field")
	}
	if _, err := planDate(q.FromDate); err != nil {
		return PlanRenewalResult{}, err
	}
	if _, err := planDate(q.ToDate); err != nil {
		return PlanRenewalResult{}, err
	}
	return s.changePlanRenewal(ctx, id, q.RequestID, q.ExpectedRevision, "apply", renewalFingerprint(id, "apply", q), q, PlanRenewalUndo{}, now)
}
func (s *Store) UndoPlanRenewal(ctx context.Context, id string, q PlanRenewalUndo, now time.Time) (PlanRenewalResult, error) {
	if !validLowerHexStorageID(q.OperationID, 32) {
		return PlanRenewalResult{}, renewalError("invalid_request")
	}
	return s.changePlanRenewal(ctx, id, q.RequestID, q.ExpectedRevision, "undo", renewalFingerprint(id, "undo", q), PlanRenewalApply{}, q, now)
}
func (s *Store) changePlanRenewal(ctx context.Context, id, requestID, expected, kind, fingerprint string, apply PlanRenewalApply, undo PlanRenewalUndo, now time.Time) (PlanRenewalResult, error) {
	if !validStorageID(id, 128) || !validLowerHexStorageID(requestID, 32) || !validLowerHexStorageID(expected, 32) || now.IsZero() {
		return PlanRenewalResult{}, renewalError("invalid_request")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanRenewalResult{}, err
	}
	defer tx.Rollback()
	if err := requireRenewalAgentTx(ctx, tx, id); err != nil {
		return PlanRenewalResult{}, err
	}
	r, found, err := readRenewalReceiptTx(ctx, tx, requestID)
	if err != nil {
		return PlanRenewalResult{}, err
	}
	if found && r.expires > now.UnixMilli() {
		if r.agent != id || r.operation.Kind != kind || r.fingerprint != fingerprint {
			return PlanRenewalResult{}, renewalError("request_id_conflict")
		}
		return renewalResultTx(ctx, tx, r, true, now)
	}
	// Even expired keys from another agent may not be silently reassigned.
	if found && (r.agent != id || r.operation.Kind != kind || r.fingerprint != fingerprint) {
		return PlanRenewalResult{}, renewalError("request_id_conflict")
	}
	p, c, err := readRenewalPlanTx(ctx, tx, id)
	if err != nil {
		return PlanRenewalResult{}, err
	}
	if p == nil {
		return PlanRenewalResult{}, renewalError("plan_missing")
	}
	if c.revision != expected {
		return PlanRenewalResult{}, renewalError("revision_conflict")
	}
	var field, from, to, operationID string
	undoUntil := int64(0)
	if kind == "apply" {
		preview, err := renewalPreview(p, c, apply.Field, now)
		if err != nil {
			return PlanRenewalResult{}, err
		}
		if !preview.Eligible {
			return PlanRenewalResult{}, renewalError(preview.Reason)
		}
		if preview.FromDate != apply.FromDate || preview.ToDate != apply.ToDate {
			return PlanRenewalResult{}, renewalError("confirmation_changed")
		}
		if preview.NewOverdue != apply.NewOverdue || preview.DueToday != apply.DueToday {
			return PlanRenewalResult{}, renewalError("preview_warning_changed")
		}
		field, from, to = preview.Field, preview.FromDate, preview.ToDate
		operationID = requestID
		undoUntil = now.Add(PlanRenewalUndoWindow).UnixMilli()
	} else {
		original, found, err := readRenewalReceiptTx(ctx, tx, undo.OperationID)
		if err != nil {
			return PlanRenewalResult{}, err
		}
		if !found || original.agent != id || original.operation.Kind != "apply" || c.latest != undo.OperationID || original.result != c.revision || planSelectedDate(*p, original.operation.Field) != original.operation.ToDate {
			return PlanRenewalResult{}, renewalError("undo_not_latest")
		}
		if !undoReceiptValid(original, p, c, now) {
			return PlanRenewalResult{}, renewalError("undo_expired")
		}
		field, from, to = original.operation.Field, original.operation.ToDate, original.operation.FromDate
		operationID = undo.OperationID
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_plan_renewal_requests WHERE request_id IN (SELECT request_id FROM agent_plan_renewal_requests WHERE agent_id=? AND expires_at<=? ORDER BY created_at,request_id LIMIT 64)`, id, now.UnixMilli()); err != nil {
		return PlanRenewalResult{}, err
	}
	var count int
	var expiry sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT count(*),min(expires_at) FROM agent_plan_renewal_requests WHERE agent_id=?`, id).Scan(&count, &expiry); err != nil {
		return PlanRenewalResult{}, err
	}
	if (kind == "apply" && count > 62) || count >= 64 {
		retry := (expiry.Int64 - now.UnixMilli() + 999) / 1000
		if retry < 1 {
			retry = 1
		}
		return PlanRenewalResult{}, &PlanRenewalError{Code: "receipt_capacity", RetryAfter: retry}
	}
	revision, err := newPlanRevision()
	if err != nil {
		return PlanRenewalResult{}, err
	}
	latest := any(nil)
	if kind == "apply" {
		latest = requestID
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_plan_config SET revision=?,latest_operation_id=? WHERE agent_id=? AND revision=?`, revision, latest, id, expected)
	if err != nil {
		return PlanRenewalResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return PlanRenewalResult{}, err
	}
	if changed != 1 {
		return PlanRenewalResult{}, renewalError("revision_conflict")
	}
	statement := `UPDATE agent_plans SET renewal_date=?,updated_at=? WHERE agent_id=?`
	if field == "expiry_date" {
		statement = `UPDATE agent_plans SET expiry_date=?,updated_at=? WHERE agent_id=?`
	}
	result, err = tx.ExecContext(ctx, statement, to, maxPlanUpdatedAt(p.UpdatedAt, now.UnixMilli()), id)
	if err != nil {
		return PlanRenewalResult{}, err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return PlanRenewalResult{}, err
	}
	if changed != 1 {
		return PlanRenewalResult{}, renewalError("plan_missing")
	}
	r = planRenewalReceipt{operation: PlanRenewalOperation{requestID, kind, field, from, to, now.UnixMilli(), undoUntil}, agent: id, fingerprint: fingerprint, prior: expected, result: revision, operationID: operationID, expires: now.Add(PlanRenewalRetention).UnixMilli()}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_plan_renewal_requests(request_id,agent_id,kind,fingerprint,field,from_date,to_date,prior_revision,result_revision,operation_id,created_at,undo_until,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, requestID, id, kind, fingerprint, field, from, to, expected, revision, operationID, r.operation.AppliedAt, undoUntil, r.expires)
	if err != nil {
		return PlanRenewalResult{}, err
	}
	out, err := renewalResultTx(ctx, tx, r, false, now)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return PlanRenewalResult{}, err
	}
	return out, nil
}
