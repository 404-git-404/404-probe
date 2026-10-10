package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func renewalNow() time.Time { return time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC) }
func renewalPlan(id string) AgentPlan {
	return AgentPlan{AgentID: id, RenewalDate: "2024-01-31", ExpiryDate: "2024-02-02", PurchaseDate: "2024-01-31", RenewalPricePeriod: "monthly", RenewalPrice: "9", Currency: "USD", Timezone: "UTC"}
}
func renewalSetup(t *testing.T, path string) (*Store, string) {
	t.Helper()
	s, id, _ := testStore(t, path)
	if _, err := s.PutAgentPlan(context.Background(), renewalPlan(id), nil, renewalNow()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, id
}
func renewalRequest(t *testing.T, s *Store, id, request string, now time.Time) PlanRenewalApply {
	t.Helper()
	p, err := s.PreviewPlanRenewal(context.Background(), id, "", now)
	if err != nil || !p.Eligible {
		t.Fatalf("preview=%+v err=%v", p, err)
	}
	return PlanRenewalApply{request, p.ExpectedRevision, p.Field, p.FromDate, p.ToDate, p.NewOverdue, p.DueToday}
}
func expectRenewalError(t *testing.T, err error, code string) {
	t.Helper()
	var e *PlanRenewalError
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s got %v", code, err)
	}
}
func renewalApply(t *testing.T, s *Store, id string, q PlanRenewalApply, now time.Time) PlanRenewalResult {
	t.Helper()
	r, err := s.ApplyPlanRenewal(context.Background(), id, q, now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func tableRenewalSnapshot(t *testing.T, s *Store, table string) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT * FROM ` + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var data [][]any
	for rows.Next() {
		v := make([]any, len(columns))
		ptr := make([]any, len(v))
		for i := range v {
			ptr[i] = &v[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			t.Fatal(err)
		}
		data = append(data, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPlanRenewalCalendarVectors(t *testing.T) {
	for _, v := range []struct{ purchase, date, period, want, source string }{
		{"2024-01-31", "2024-01-31", "monthly", "2024-02-29", "purchase_date"},
		{"2024-01-31", "2024-02-29", "monthly", "2024-03-31", "purchase_date"},
		{"2025-01-31", "2025-02-28", "monthly", "2025-03-31", "purchase_date"},
		{"2024-02-29", "2027-02-28", "yearly", "2028-02-29", "purchase_date"},
		{"2025-01-31", "2025-02-15", "monthly", "2025-03-15", "current_date"},
		{"", "2025-04-30", "monthly", "2025-05-30", "current_date"},
		{"", "2024-02-29", "yearly", "2025-02-28", "current_date"},
		{"0001-01-31", "0001-01-31", "monthly", "0001-02-28", "purchase_date"},
	} {
		t.Run(v.date+v.period+v.purchase, func(t *testing.T) {
			p := AgentPlan{PurchaseDate: v.purchase, RenewalDate: v.date, RenewalPricePeriod: v.period}
			c := planRenewalConfig{renewalAnchor: planRenewalAnchor(p, p.RenewalDate)}
			out, err := renewalPreview(&p, c, "", renewalNow())
			if err != nil || out.ToDate != v.want || out.AnchorSource != v.source {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
	p := AgentPlan{RenewalDate: "9999-12-31", RenewalPricePeriod: "monthly"}
	_, err := renewalPreview(&p, planRenewalConfig{renewalAnchor: p.RenewalDate}, "", renewalNow())
	expectRenewalError(t, err, "date_out_of_range")
}
func TestPlanRenewalPreviewNoWritesReasonsAndTimezone(t *testing.T) {
	s, id := renewalSetup(t, ":memory:")
	ctx := context.Background()
	before := []string{tableRenewalSnapshot(t, s, "agent_plans"), tableRenewalSnapshot(t, s, "agent_plan_config"), tableRenewalSnapshot(t, s, "agent_plan_renewal_requests")}
	var beforeChanges, afterChanges int
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&beforeChanges); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewPlanRenewal(ctx, id, "", renewalNow()); err != nil {
		t.Fatal(err)
	}
	_, err := s.PreviewPlanRenewal(ctx, id, "quota", renewalNow())
	expectRenewalError(t, err, "invalid_field")
	after := []string{tableRenewalSnapshot(t, s, "agent_plans"), tableRenewalSnapshot(t, s, "agent_plan_config"), tableRenewalSnapshot(t, s, "agent_plan_renewal_requests")}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preview wrote")
	}
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&afterChanges); err != nil || beforeChanges != afterChanges {
		t.Fatalf("preview DB writes %d -> %d: %v", beforeChanges, afterChanges, err)
	}
	other := "dddddddddddddddddddddddddddddddd"
	if err := s.AddAgent(ctx, other, "unconfigured", []byte("hash"), renewalNow()); err != nil {
		t.Fatal(err)
	}
	missing, err := s.PreviewPlanRenewal(ctx, other, "", renewalNow())
	if err != nil || missing.Reason != "plan_missing" {
		t.Fatalf("%+v %v", missing, err)
	}
	var configs int
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_plan_config WHERE agent_id=?`, other).Scan(&configs); err != nil || configs != 0 {
		t.Fatalf("missing preview config %d %v", configs, err)
	}
	for _, v := range []struct {
		p      *AgentPlan
		reason string
	}{{nil, "plan_missing"}, {&AgentPlan{}, "date_missing"}, {&AgentPlan{ExpiryDate: "2024-01-01"}, "period_missing"}, {&AgentPlan{RenewalDate: "2024-01-01", RenewalPricePeriod: "once"}, "one_time"}} {
		out, err := renewalPreview(v.p, planRenewalConfig{}, "", renewalNow())
		if err != nil || out.Eligible || out.Reason != v.reason || !out.EditPlan {
			t.Fatalf("%+v %v", out, err)
		}
	}
	p := AgentPlan{RenewalDate: "2026-09-02", RenewalPricePeriod: "monthly", Timezone: "America/Los_Angeles"}
	c := planRenewalConfig{renewalAnchor: p.RenewalDate}
	now := time.Date(2026, 10, 3, 0, 30, 0, 0, time.UTC)
	out, err := renewalPreview(&p, c, "", now)
	if err != nil || !out.DueToday || out.NewOverdue {
		t.Fatalf("%+v %v", out, err)
	}
	p.Timezone = ""
	out, err = renewalPreview(&p, c, "", now)
	if err != nil || !out.NewOverdue || out.Timezone != "UTC" {
		t.Fatalf("%+v %v", out, err)
	}
	p.Timezone = "invalid"
	_, err = renewalPreview(&p, c, "", now)
	expectRenewalError(t, err, "invalid_timezone")
}
func TestPlanRenewalReplayCurrentAndConflicts(t *testing.T) {
	s, id := renewalSetup(t, ":memory:")
	now := renewalNow()
	q := renewalRequest(t, s, id, "11111111111111111111111111111111", now)
	a := renewalApply(t, s, id, q, now)
	if a.CurrentPlan.RenewalDate != "2024-02-29" || a.CurrentPlan.ExpiryDate != "2024-02-02" || !a.Undo.Available {
		t.Fatalf("%+v", a)
	}
	q2 := renewalRequest(t, s, id, "22222222222222222222222222222222", now)
	b := renewalApply(t, s, id, q2, now)
	r := renewalApply(t, s, id, q, now)
	if !r.Replayed || r.Operation != a.Operation || r.CurrentPlan.RenewalDate != "2024-03-31" || r.CurrentRevision != b.CurrentRevision || r.Undo.OperationID != q2.RequestID {
		t.Fatalf("replay %+v", r)
	}
	changed := q
	changed.ToDate = "2024-03-01"
	_, err := s.ApplyPlanRenewal(context.Background(), id, changed, now)
	expectRenewalError(t, err, "request_id_conflict")
	_, err = s.UndoPlanRenewal(context.Background(), id, PlanRenewalUndo{"88888888888888888888888888888888", q.RequestID, b.CurrentRevision}, now)
	expectRenewalError(t, err, "undo_not_latest")
	_, err = s.UndoPlanRenewal(context.Background(), id, PlanRenewalUndo{q.RequestID, q2.RequestID, b.CurrentRevision}, now)
	expectRenewalError(t, err, "request_id_conflict")
	other := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := s.AddAgent(context.Background(), other, "other", []byte("hash"), now); err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyPlanRenewal(context.Background(), other, q, now)
	expectRenewalError(t, err, "request_id_conflict")
}
func TestPlanRenewalUndoLatestWindowAndReplay(t *testing.T) {
	s, id := renewalSetup(t, ":memory:")
	now := renewalNow()
	q := renewalRequest(t, s, id, "11111111111111111111111111111111", now)
	a := renewalApply(t, s, id, q, now)
	u := PlanRenewalUndo{"33333333333333333333333333333333", q.RequestID, a.CurrentRevision}
	_, err := s.UndoPlanRenewal(context.Background(), id, u, now.Add(5*time.Minute))
	expectRenewalError(t, err, "undo_expired")
	_, err = s.UndoPlanRenewal(context.Background(), id, u, now.Add(-time.Millisecond))
	expectRenewalError(t, err, "undo_expired")
	r, err := s.UndoPlanRenewal(context.Background(), id, u, now.Add(time.Minute))
	if err != nil || r.CurrentPlan.RenewalDate != q.FromDate || r.Undo.Available {
		t.Fatalf("%+v %v", r, err)
	}
	r2, err := s.UndoPlanRenewal(context.Background(), id, u, now.Add(time.Hour))
	if err != nil || !r2.Replayed || r2.Operation != r.Operation {
		t.Fatalf("%+v %v", r2, err)
	}
	u.RequestID = "44444444444444444444444444444444"
	u.ExpectedRevision = r.CurrentRevision
	_, err = s.UndoPlanRenewal(context.Background(), id, u, now)
	expectRenewalError(t, err, "undo_not_latest")
}
func TestPlanRenewalOldPutClearABAAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "renewal.db")
	s, id := renewalSetup(t, path)
	now := renewalNow()
	q := renewalRequest(t, s, id, "11111111111111111111111111111111", now)
	p, _, _ := s.GetAgentPlan(context.Background(), id)
	if _, err := s.PutAgentPlan(context.Background(), p, nil, now); err != nil {
		t.Fatal(err)
	}
	_, err := s.ApplyPlanRenewal(context.Background(), id, q, now)
	expectRenewalError(t, err, "revision_conflict")
	q = renewalRequest(t, s, id, q.RequestID, now)
	a := renewalApply(t, s, id, q, now)
	if _, err := s.DeleteAgentPlan(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAgentPlan(context.Background(), p, nil, now); err != nil {
		t.Fatal(err)
	}
	_, err = s.UndoPlanRenewal(context.Background(), id, PlanRenewalUndo{"22222222222222222222222222222222", q.RequestID, a.CurrentRevision}, now)
	expectRenewalError(t, err, "revision_conflict")
	q = renewalRequest(t, s, id, "55555555555555555555555555555555", now)
	s.Close()
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	r := renewalApply(t, s2, id, q, now)
	if r.CurrentPlan.RenewalDate != q.ToDate {
		t.Fatal(r)
	}
	if _, err := s2.db.Exec(`DELETE FROM agents WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := s2.AddAgent(context.Background(), id, "new", []byte("hash"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.PutAgentPlan(context.Background(), p, nil, now); err != nil {
		t.Fatal(err)
	}
	_, err = s2.ApplyPlanRenewal(context.Background(), id, q, now)
	expectRenewalError(t, err, "revision_conflict")
}
func TestPlanRenewalTelemetryNoConflictAndNoCounterWrites(t *testing.T) {
	s, id := renewalSetup(t, ":memory:")
	ctx := context.Background()
	now := renewalNow()
	p := renewalPlan(id)
	p.TrafficMode = "sum"
	p.CycleKind = "monthly"
	p.CycleCount = 1
	p.CycleAnchor = "2024-01-01"
	p.QuotaValue = "1"
	p.QuotaUnit = "GiB"
	bytes := uint64(1 << 30)
	p.QuotaBytes = &bytes
	if _, err := s.PutAgentPlan(ctx, p, nil, now); err != nil {
		t.Fatal(err)
	}
	q := renewalRequest(t, s, id, "11111111111111111111111111111111", now)
	for i := 1; i <= 2; i++ {
		report := validReport(id, 1, "session", "boot", uint64(i), uint64(1000*i), uint64(2000*i))
		stamp := now.Add(time.Duration(i) * time.Second)
		report.CollectedAt = stamp.UnixMilli()
		if _, accepted, _, err := s.ProcessReport(ctx, id, report, stamp); err != nil || !accepted {
			t.Fatalf("%v %v", accepted, err)
		}
	}
	before, _, _ := s.GetAgentPlan(ctx, id)
	snap := map[string]string{}
	for _, table := range []string{"agent_state", "agent_traffic_baselines", "agent_traffic_reset_requests"} {
		snap[table] = tableRenewalSnapshot(t, s, table)
	}
	r := renewalApply(t, s, id, q, now.Add(3*time.Second))
	after := *r.CurrentPlan
	after.RenewalDate = before.RenewalDate
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("non-date mutation before=%+v after=%+v", before, after)
	}
	_, err := s.UndoPlanRenewal(ctx, id, PlanRenewalUndo{"22222222222222222222222222222222", q.RequestID, r.CurrentRevision}, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for table, want := range snap {
		if got := tableRenewalSnapshot(t, s, table); got != want {
			t.Fatalf("%s changed", table)
		}
	}
}
func TestPlanRenewalCrossMidnightConfirmationAndEligibility(t *testing.T) {
	s, id := renewalSetup(t, ":memory:")
	ctx := context.Background()
	p := renewalPlan(id)
	p.PurchaseDate = ""
	p.RenewalDate = "2024-01-10"
	now := time.Date(2024, 2, 9, 23, 59, 59, 0, time.UTC)
	if _, err := s.PutAgentPlan(ctx, p, nil, now); err != nil {
		t.Fatal(err)
	}
	q := renewalRequest(t, s, id, "11111111111111111111111111111111", now)
	_, err := s.ApplyPlanRenewal(ctx, id, q, now.Add(time.Second))
	expectRenewalError(t, err, "preview_warning_changed")
	q.ToDate = "2024-02-11"
	_, err = s.ApplyPlanRenewal(ctx, id, q, now)
	expectRenewalError(t, err, "confirmation_changed")
	if _, err := s.db.Exec(`UPDATE agents SET disabled_at=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	q = renewalRequest(t, s, id, q.RequestID, now)
	renewalApply(t, s, id, q, now)
	if _, err := s.db.Exec(`UPDATE agents SET revoked=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	_, err = s.PreviewPlanRenewal(ctx, id, "", now)
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatal(err)
	}
}
func TestPlanRenewalReceiptCapacityRetentionAndExpiryField(t *testing.T) {
	s, id := renewalSetup(t, ":memory:")
	ctx := context.Background()
	now := renewalNow()
	for i := 0; i < 62; i++ {
		q := renewalRequest(t, s, id, fmt.Sprintf("%032x", i+1), now)
		renewalApply(t, s, id, q, now)
	}
	preview, err := s.PreviewPlanRenewal(ctx, id, "expiry_date", now)
	if err != nil {
		t.Fatal(err)
	}
	q := PlanRenewalApply{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", preview.ExpectedRevision, preview.Field, preview.FromDate, preview.ToDate, preview.NewOverdue, preview.DueToday}
	r := renewalApply(t, s, id, q, now)
	if r.CurrentPlan.ExpiryDate != q.ToDate {
		t.Fatal(r)
	}
	_, err = s.ApplyPlanRenewal(ctx, id, renewalRequest(t, s, id, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", now), now)
	expectRenewalError(t, err, "receipt_capacity")
	u := PlanRenewalUndo{"cccccccccccccccccccccccccccccccc", q.RequestID, r.CurrentRevision}
	if _, err := s.UndoPlanRenewal(ctx, id, u, now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_plan_renewal_requests`).Scan(&count); err != nil || count != 64 {
		t.Fatalf("count %d %v", count, err)
	}
	_, err = s.ApplyPlanRenewal(ctx, id, q, now.Add(PlanRenewalRetention))
	expectRenewalError(t, err, "revision_conflict")
	q = renewalRequest(t, s, id, "dddddddddddddddddddddddddddddddd", now.Add(PlanRenewalRetention))
	renewalApply(t, s, id, q, now.Add(PlanRenewalRetention))
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_plan_renewal_requests`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count %d %v", count, err)
	}
}
func TestPlanRenewalDoubleStoreConcurrentSameAndDifferentID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	s, id := renewalSetup(t, path)
	other, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	now := renewalNow()
	for _, same := range []bool{true, false} {
		q := renewalRequest(t, s, id, fmt.Sprintf("%032x", 100+map[bool]int{true: 1, false: 2}[same]), now)
		q2 := q
		if !same {
			q2.RequestID = "ffffffffffffffffffffffffffffffff"
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]PlanRenewalResult, 2)
		errs := make([]error, 2)
		for i, store := range []*Store{s, other} {
			wg.Add(1)
			go func(i int, store *Store) {
				defer wg.Done()
				<-start
				req := q
				if i == 1 {
					req = q2
				}
				results[i], errs[i] = store.ApplyPlanRenewal(context.Background(), id, req, now)
			}(i, store)
		}
		close(start)
		wg.Wait()
		t.Logf("dual Store sameID=%v outcomes=%v", same, errs)
		applied := 0
		for i, err := range errs {
			if err == nil && !results[i].Replayed {
				applied++
			}
		}
		if applied != 1 {
			t.Fatalf("applied %d errors %v", applied, errs)
		}
		// Explicit identical-request verification after a possible SQLite busy outcome.
		if same {
			r := renewalApply(t, other, id, q, now)
			if !r.Replayed {
				t.Fatal("not replay")
			}
		} else {
			req := q
			if errs[0] == nil {
				req = q2
			}
			_, err := other.ApplyPlanRenewal(context.Background(), id, req, now)
			expectRenewalError(t, err, "revision_conflict")
		}
	}
}

type failingRenewalRandom struct{}

func (failingRenewalRandom) Read([]byte) (int, error) {
	return 0, errors.New("injected random failure")
}
func TestPlanRenewalFailuresRollbackAndReleaseTransaction(t *testing.T) {
	s, id := renewalSetup(t, ":memory:")
	ctx := context.Background()
	now := renewalNow()
	q := renewalRequest(t, s, id, "11111111111111111111111111111111", now)
	before := tableRenewalSnapshot(t, s, "agent_plans")
	config := tableRenewalSnapshot(t, s, "agent_plan_config")
	func() {
		old := rand.Reader
		rand.Reader = failingRenewalRandom{}
		defer func() { rand.Reader = old }()
		if _, err := s.ApplyPlanRenewal(ctx, id, q, now); err == nil {
			t.Fatal("random failure ignored")
		}
		if _, err := s.DeleteAgentPlan(ctx, id); err == nil {
			t.Fatal("clear failure ignored")
		}
		if _, err := s.PutAgentPlan(ctx, renewalPlan(id), nil, now); err == nil {
			t.Fatal("put failure ignored")
		}
	}()
	if tableRenewalSnapshot(t, s, "agent_plans") != before || tableRenewalSnapshot(t, s, "agent_plan_config") != config {
		t.Fatal("failed random wrote")
	}
	if _, err := s.db.Exec(`CREATE TRIGGER renewal_test_failure BEFORE INSERT ON agent_plan_renewal_requests BEGIN SELECT RAISE(ABORT,'receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPlanRenewal(ctx, id, q, now); err == nil {
		t.Fatal("insert failure ignored")
	}
	if tableRenewalSnapshot(t, s, "agent_plans") != before || tableRenewalSnapshot(t, s, "agent_plan_config") != config {
		t.Fatal("insert failure wrote")
	}
	if _, err := s.db.Exec(`DROP TRIGGER renewal_test_failure`); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ApplyPlanRenewal(cancelCtx, id, q, now); err == nil {
		t.Fatal("cancel ignored")
	}
	renewalApply(t, s, id, q, now)
}
func TestPlanRenewalMigration24BackfillAndAtomicFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	s, id := renewalSetup(t, path)
	ctx := context.Background()
	before := tableRenewalSnapshot(t, s, "agent_plans")
	dropUpgradeV2FixtureSchema(t, s.db)
	for _, statement := range []string{`DROP TABLE agent_plan_renewal_requests`, `DROP TABLE agent_plan_config`, `DELETE FROM schema_migrations WHERE version=24`} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	objects, tables := renewalSchemaSnapshot(t, s)
	data := map[string]string{}
	for _, table := range tables {
		if table != "schema_migrations" {
			data[table] = tableRenewalSnapshot(t, s, table)
		}
	}
	func() {
		old := rand.Reader
		rand.Reader = failingRenewalRandom{}
		defer func() { rand.Reader = old }()
		if err := s.migrate(ctx); err == nil {
			t.Fatal("migration failure ignored")
		}
	}()
	version, err := s.SchemaVersion(ctx)
	if err != nil || version != 23 {
		t.Fatalf("version %d %v", version, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('agent_plan_config','agent_plan_renewal_requests')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial migration %d %v", count, err)
	}
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	version, err = s.SchemaVersion(ctx)
	if err != nil || version != currentSchemaVersion {
		t.Fatalf("version %d %v", version, err)
	}
	// Compare migration24 against its original schema24 contract; schema25 adds
	// intentional upgrade authorization columns to two existing tables.
	dropUpgradeV2FixtureSchema(t, s.db)
	if tableRenewalSnapshot(t, s, "agent_plans") != before {
		t.Fatal("migration changed plan")
	}
	newObjects, _ := renewalSchemaSnapshot(t, s)
	for name, statement := range objects {
		if newObjects[name] != statement {
			t.Fatalf("old schema changed %s", name)
		}
		delete(newObjects, name)
	}
	if len(newObjects) != 6 {
		t.Fatalf("unexpected objects %v", newObjects)
	}
	for table, want := range data {
		if got := tableRenewalSnapshot(t, s, table); got != want {
			t.Fatalf("migration changed %s", table)
		}
	}
	t.Logf("v23->24 retains %d objects / %d old data tables; only 2 tables, 2 indexes, 2 automatic PK indexes added: %v", len(objects), len(data), newObjects)
	q := renewalRequest(t, s, id, "11111111111111111111111111111111", renewalNow())
	renewalApply(t, s, id, q, renewalNow())
}

func renewalSchemaSnapshot(t *testing.T, s *Store) (map[string]string, []string) {
	t.Helper()
	rows, err := s.db.Query(`SELECT name,type,sql FROM sqlite_master ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	objects := map[string]string{}
	var tables []string
	for rows.Next() {
		var name, kind string
		var statement sql.NullString
		if err := rows.Scan(&name, &kind, &statement); err != nil {
			t.Fatal(err)
		}
		objects[name] = kind + ":" + statement.String
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return objects, tables
}
